package pipeline

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jdwiederstein/mycelium/internal/embed"
	"github.com/jdwiederstein/mycelium/internal/index"
)

// EmbedWorker drains embed_queue in the background and writes results back
// to chunks.embedding + embed_cache. One worker per daemon is plenty; the
// batch size and throttle tune provider throughput.
type EmbedWorker struct {
	Index    *index.Index
	Embedder embed.Embedder
	Logger   Logger

	// BatchSize is how many queued jobs we pull per poll.
	BatchSize int
	// Interval between polls when the queue is empty.
	Interval time.Duration
	// MaxAttempts caps retries per job before we skip it. Exceeded jobs sit
	// in the queue with attempts >= MaxAttempts until InvalidateEmbeddings
	// is called.
	MaxAttempts int
	// RatePerMinute is a soft circuit breaker for hosted providers. We pause
	// further embedding once this many chunks have been processed in the
	// trailing 60s. Zero disables.
	RatePerMinute int
}

// Run blocks until ctx is cancelled. Safe to call once per daemon lifetime.
func (w *EmbedWorker) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = stdoutLogger{w: os.Stderr}
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 8
	}
	if w.Interval == 0 {
		w.Interval = 2 * time.Second
	}
	if w.MaxAttempts == 0 {
		w.MaxAttempts = 3
	}
	// Skip if the configured embedder is a Noop — nothing to do.
	if _, isNoop := w.Embedder.(embed.Noop); isNoop || w.Embedder == nil {
		return
	}

	limiter := newRateLimiter(w.RatePerMinute)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		jobs, err := w.Index.FetchPending(ctx, w.BatchSize)
		if err != nil {
			w.Logger.Printf("[embed] fetch pending: %v", err)
			sleep(ctx, w.Interval)
			continue
		}
		if len(jobs) == 0 {
			sleep(ctx, w.Interval)
			continue
		}
		if !limiter.take(len(jobs)) {
			w.Logger.Printf("[embed] rate limit reached (%d chunks in trailing 60s), pausing", w.RatePerMinute)
			sleep(ctx, 30*time.Second)
			continue
		}
		if err := w.processBatch(ctx, jobs); err != nil {
			w.Logger.Printf("[embed] batch: %v", err)
			sleep(ctx, w.Interval)
		}
	}
}

// processBatch calls the embedder and writes results back to the index.
func (w *EmbedWorker) processBatch(ctx context.Context, jobs []index.PendingJob) error {
	inputs := make([]string, len(jobs))
	for i, j := range jobs {
		inputs[i] = j.Content
	}
	vectors, err := w.Embedder.Embed(ctx, inputs)
	if err != nil {
		for _, j := range jobs {
			_ = w.Index.MarkJobFailed(ctx, j.ChunkID, err.Error())
		}
		return err
	}
	if len(vectors) != len(jobs) {
		return fmt.Errorf("embedder returned %d vectors for %d inputs", len(vectors), len(jobs))
	}
	for i, j := range jobs {
		packed := embed.Pack(vectors[i])
		if err := w.Index.WriteEmbedding(ctx, j.ChunkID, j.ContentHash, packed, w.Embedder.Model()); err != nil {
			w.Logger.Printf("[embed] write chunk %d: %v", j.ChunkID, err)
		}
	}
	w.Logger.Printf("[embed] processed %d chunks", len(jobs))
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// rateLimiter is a trailing-60s token bucket. Zero capacity disables it.
type rateLimiter struct {
	mu      sync.Mutex
	cap     int
	events  []time.Time
}

func newRateLimiter(capPerMinute int) *rateLimiter {
	return &rateLimiter{cap: capPerMinute}
}

func (r *rateLimiter) take(n int) bool {
	if r.cap <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-time.Minute)
	kept := r.events[:0]
	for _, t := range r.events {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	r.events = kept
	if len(r.events)+n > r.cap {
		return false
	}
	now := time.Now()
	for i := 0; i < n; i++ {
		r.events = append(r.events, now)
	}
	return true
}

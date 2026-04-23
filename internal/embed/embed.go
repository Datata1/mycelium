package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/jdwiederstein/mycelium/internal/config"
)

// ErrNotConfigured is returned by the Noop provider. The query layer translates
// this into a friendly "embeddings_not_configured" error for MCP clients.
var ErrNotConfigured = errors.New("embeddings_not_configured")

// Embedder turns text into a fixed-dimension vector. Implementations must be
// safe for concurrent use (the pipeline calls them from a worker pool).
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Model() string
	Dimension() int
}

// New dispatches based on config. Returns Noop{} when provider=none.
func New(cfg config.EmbedderConfig) (Embedder, error) {
	switch cfg.Provider {
	case "", "none":
		return Noop{}, nil
	case "ollama":
		if cfg.Model == "" {
			return nil, fmt.Errorf("ollama embedder: model is required")
		}
		if cfg.Dimension <= 0 {
			return nil, fmt.Errorf("ollama embedder: dimension must be > 0")
		}
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		return NewOllama(endpoint, cfg.Model, cfg.Dimension), nil
	default:
		return nil, fmt.Errorf("unsupported embedder provider: %q", cfg.Provider)
	}
}

// --- serialization ---------------------------------------------------------

// Pack serializes a vector to little-endian float32 bytes for storage.
func Pack(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// Unpack mirrors Pack. Length-checks the buffer against the expected dim.
func Unpack(b []byte, dim int) ([]float32, error) {
	if len(b) != 4*dim {
		return nil, fmt.Errorf("embedding: expected %d bytes (dim=%d), got %d", 4*dim, dim, len(b))
	}
	v := make([]float32, dim)
	for i := 0; i < dim; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

// UnpackInto is the alloc-free variant for hot loops. Caller provides a
// preallocated []float32 of the right length; we fill it in place and
// validate the byte slice width.
func UnpackInto(b []byte, out []float32) error {
	if len(b) != 4*len(out) {
		return fmt.Errorf("embedding: expected %d bytes (dim=%d), got %d", 4*len(out), len(out), len(b))
	}
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return nil
}

// Cosine returns the cosine similarity of two vectors. Returns 0 if either
// vector is all-zero or the dimensions differ.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		na += af * af
		nb += bf * bf
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// vectorsEqual is a tiny helper for tests and migration invalidation.
func vectorsEqual(a, b []float32) bool { return bytes.Equal(Pack(a), Pack(b)) }

var _ = vectorsEqual // retain for future comparison helpers

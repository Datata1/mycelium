package watch

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeSource is a scripted rawSource: tests push rawEvents through it
// to exercise the wrapper's policy layer without a real backend.
type fakeSource struct {
	out chan rawEvent
}

func newFakeSource() *fakeSource                  { return &fakeSource{out: make(chan rawEvent, 1024)} }
func (f *fakeSource) start(context.Context) error { return nil }
func (f *fakeSource) events() <-chan rawEvent     { return f.out }
func (f *fakeSource) close() error                { close(f.out); return nil }

// collect drains the wrapper's output until it closes.
func collect(w *wrapped) <-chan []Event {
	res := make(chan []Event, 1)
	go func() {
		var got []Event
		for ev := range w.Events() {
			got = append(got, ev)
		}
		res <- got
	}()
	return res
}

func startWrapped(t *testing.T, opts Options, src rawSource) *wrapped {
	t.Helper()
	w := newWrapped(opts, src)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return w
}

func TestOverflow_RawEventPassesThroughFiltersAndDebounce(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	w := startWrapped(t, Options{Include: []string{"**/*.go"}, DebounceMS: 5, CoalesceMS: 20}, src)
	res := collect(w)

	src.out <- rawEvent{Overflow: true, Reason: "fsnotify: queue overflow"}
	time.Sleep(60 * time.Millisecond)
	src.close()
	got := <-res

	if len(got) != 1 {
		t.Fatalf("events = %v, want exactly one", got)
	}
	if !got[0].Overflow || got[0].Reason != "fsnotify: queue overflow" {
		t.Errorf("event = %+v, want Overflow with backend reason", got[0])
	}
}

func TestOverflow_BurstAboveThresholdCollapsesToOneEvent(t *testing.T) {
	t.Parallel()
	const threshold = 10
	src := newFakeSource()
	w := startWrapped(t, Options{DebounceMS: 1, CoalesceMS: 40, RescanThreshold: threshold}, src)
	res := collect(w)

	for i := 0; i < threshold+5; i++ {
		src.out <- rawEvent{RelPath: fmt.Sprintf("f%02d.go", i), AbsPath: "/x"}
	}
	time.Sleep(120 * time.Millisecond)
	src.close()
	got := <-res

	if len(got) != 1 {
		t.Fatalf("events = %d (%v), want exactly one overflow", len(got), got)
	}
	if !got[0].Overflow || got[0].Reason != "burst" {
		t.Errorf("event = %+v, want Overflow reason=burst", got[0])
	}
}

func TestOverflow_BurstBelowThresholdEmitsPerFileEvents(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	w := startWrapped(t, Options{DebounceMS: 1, CoalesceMS: 40, RescanThreshold: 10}, src)
	res := collect(w)

	for i := 0; i < 5; i++ {
		src.out <- rawEvent{RelPath: fmt.Sprintf("f%02d.go", i), AbsPath: "/x"}
	}
	time.Sleep(120 * time.Millisecond)
	src.close()
	got := <-res

	if len(got) != 5 {
		t.Fatalf("events = %d (%v), want 5 per-file events", len(got), got)
	}
	for _, ev := range got {
		if ev.Overflow {
			t.Errorf("unexpected overflow event below threshold: %+v", ev)
		}
	}
}

func TestOverflow_ZeroThresholdDisablesBurstEscalation(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	w := startWrapped(t, Options{DebounceMS: 1, CoalesceMS: 40}, src)
	res := collect(w)

	for i := 0; i < 50; i++ {
		src.out <- rawEvent{RelPath: fmt.Sprintf("f%02d.go", i), AbsPath: "/x"}
	}
	time.Sleep(120 * time.Millisecond)
	src.close()
	got := <-res

	if len(got) != 50 {
		t.Fatalf("events = %d, want all 50 (threshold disabled)", len(got))
	}
}

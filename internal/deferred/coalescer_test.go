// file: internal/deferred/coalescer_test.go

package deferred

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.NewLogger(&config.LogConfig{Level: "error", OutputPath: "stdout", Encoding: "console"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	return log
}

// recorder collects the actions an Executor was handed, in order.
type recorder struct {
	mu      sync.Mutex
	got     []string
	err     error
	fired   chan struct{}
	fireOne bool
}

func newRecorder() *recorder {
	return &recorder{fired: make(chan struct{}, 64)}
}

func (r *recorder) exec(_ context.Context, a *rule.Action) error {
	r.mu.Lock()
	if a.NATS != nil {
		r.got = append(r.got, a.NATS.Subject+"|"+a.NATS.Payload)
	}
	err := r.err
	r.mu.Unlock()

	select {
	case r.fired <- struct{}{}:
	default:
	}
	return err
}

func (r *recorder) subjects() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// waitFor blocks until n executions have happened or the deadline passes.
func (r *recorder) waitFor(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		r.mu.Lock()
		have := len(r.got)
		r.mu.Unlock()
		if have >= n {
			return
		}
		select {
		case <-r.fired:
		case <-deadline:
			r.mu.Lock()
			have = len(r.got)
			r.mu.Unlock()
			t.Fatalf("timed out waiting for %d executions, got %d", n, have)
		}
	}
}

func natsBatch(key string, window time.Duration, payloads ...string) rule.DeferredBatch {
	actions := make([]*rule.Action, 0, len(payloads))
	for _, p := range payloads {
		actions = append(actions, &rule.Action{
			NATS:  &rule.NATSAction{Subject: "out." + key, Payload: p},
			Defer: &rule.DeferSpec{Key: key, Window: window},
		})
	}
	return rule.DeferredBatch{Key: key, Window: window, Actions: actions}
}

// TestCoalescer_EmitsLastValueInWindow is the core contract: several submits
// inside one window produce exactly one emission, carrying the last value.
func TestCoalescer_EmitsLastValueInWindow(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	c.Submit(natsBatch("k", 120*time.Millisecond, "first"))
	c.Submit(natsBatch("k", 120*time.Millisecond, "second"))
	c.Submit(natsBatch("k", 120*time.Millisecond, "third"))

	if got := c.Pending(); got != 1 {
		t.Fatalf("expected 1 pending key while the window is open, got %d", got)
	}

	rec.waitFor(t, 1, 2*time.Second)
	// Give any erroneous extra emission a chance to show up.
	time.Sleep(150 * time.Millisecond)

	got := rec.subjects()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 emission for the window, got %d: %v", len(got), got)
	}
	if got[0] != "out.k|third" {
		t.Errorf("expected the last submitted value to win, got %q", got[0])
	}
	if p := c.Pending(); p != 0 {
		t.Errorf("expected no pending entries after the window closed, got %d", p)
	}
}

// TestCoalescer_WindowIsFixedNotReset guards the deliberate choice of a fixed
// window from the first submit: a steady stream must still emit, not starve.
func TestCoalescer_WindowIsFixedNotReset(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)
	defer c.Stop(context.Background())

	window := 200 * time.Millisecond
	start := time.Now()

	// Submit continuously at a rate that would reset a classic debounce forever.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.Submit(natsBatch("k", window, "tick"))
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	rec.waitFor(t, 1, 2*time.Second)
	close(stop)
	wg.Wait()

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("emission took %v under continuous submits; window should be fixed, not reset", elapsed)
	}
}

// TestCoalescer_KeysAreIndependent verifies distinct keys hold distinct windows.
func TestCoalescer_KeysAreIndependent(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	c.Submit(natsBatch("a", 100*time.Millisecond, "a1"))
	c.Submit(natsBatch("b", 100*time.Millisecond, "b1"))
	c.Submit(natsBatch("a", 100*time.Millisecond, "a2"))

	if got := c.Pending(); got != 2 {
		t.Fatalf("expected 2 pending keys, got %d", got)
	}

	rec.waitFor(t, 2, 2*time.Second)

	got := rec.subjects()
	if len(got) != 2 {
		t.Fatalf("expected 2 emissions (one per key), got %d: %v", len(got), got)
	}

	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["out.a|a2"] || !seen["out.b|b1"] {
		t.Errorf("expected last value per key, got %v", got)
	}
}

// TestCoalescer_ForEachBatchStaysWhole verifies a multi-action batch (forEach
// fan-out) is replaced and emitted as a unit — never collapsed to one action.
func TestCoalescer_ForEachBatchStaysWhole(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	c.Submit(natsBatch("k", 100*time.Millisecond, "old1", "old2", "old3"))
	c.Submit(natsBatch("k", 100*time.Millisecond, "new1", "new2", "new3"))

	rec.waitFor(t, 3, 2*time.Second)
	time.Sleep(100 * time.Millisecond)

	got := rec.subjects()
	if len(got) != 3 {
		t.Fatalf("expected the whole 3-action batch to fire, got %d: %v", len(got), got)
	}
	for i, want := range []string{"out.k|new1", "out.k|new2", "out.k|new3"} {
		if got[i] != want {
			t.Errorf("action %d: expected %q, got %q", i, want, got[i])
		}
	}
}

// TestCoalescer_StopFlushesPending verifies shutdown emits the settled value
// rather than discarding it.
func TestCoalescer_StopFlushesPending(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	// A long window so the timer cannot fire on its own during the test.
	c.Submit(natsBatch("k", 10*time.Second, "pending"))
	c.Submit(natsBatch("k", 10*time.Second, "settled"))

	c.Stop(context.Background())

	got := rec.subjects()
	if len(got) != 1 {
		t.Fatalf("expected the pending batch to be flushed on Stop, got %d: %v", len(got), got)
	}
	if got[0] != "out.k|settled" {
		t.Errorf("expected the settled value to be flushed, got %q", got[0])
	}
}

// TestCoalescer_SubmitAfterStopIsRefused verifies a stopped coalescer reports
// the drop instead of silently holding work that will never fire.
func TestCoalescer_SubmitAfterStopIsRefused(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)
	c.Stop(context.Background())

	if c.Submit(natsBatch("k", time.Second, "late")) {
		t.Error("expected Submit after Stop to report false")
	}
	if got := rec.subjects(); len(got) != 0 {
		t.Errorf("expected no execution after Stop, got %v", got)
	}
	if p := c.Pending(); p != 0 {
		t.Errorf("expected nothing pending after Stop, got %d", p)
	}
}

// TestCoalescer_StopIsIdempotent verifies a second Stop neither panics nor
// re-emits, since several shutdown paths may reach it.
func TestCoalescer_StopIsIdempotent(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	c.Submit(natsBatch("k", 10*time.Second, "settled"))
	c.Stop(context.Background())
	c.Stop(context.Background())

	if got := rec.subjects(); len(got) != 1 {
		t.Errorf("expected exactly 1 emission across two Stops, got %d: %v", len(got), got)
	}
}

// TestCoalescer_ExecutorErrorDoesNotBlockBatch verifies a failing action is
// logged and the rest of the batch still runs — the trigger was ACKed long ago,
// so there is nothing to retry into.
func TestCoalescer_ExecutorErrorDoesNotBlockBatch(t *testing.T) {
	rec := newRecorder()
	rec.err = errors.New("publish failed")
	c := New("test", rec.exec, time.Second, testLogger(t), nil)

	c.Submit(natsBatch("k", 50*time.Millisecond, "one", "two"))
	rec.waitFor(t, 2, 2*time.Second)

	if got := rec.subjects(); len(got) != 2 {
		t.Errorf("expected both actions attempted despite errors, got %v", got)
	}
}

// TestCoalescer_EmptyBatchIsNoop guards against arming a timer for nothing.
func TestCoalescer_EmptyBatchIsNoop(t *testing.T) {
	rec := newRecorder()
	c := New("test", rec.exec, time.Second, testLogger(t), nil)
	defer c.Stop(context.Background())

	if !c.Submit(rule.DeferredBatch{Key: "k", Window: time.Second}) {
		t.Error("expected an empty batch to be accepted as a no-op")
	}
	if p := c.Pending(); p != 0 {
		t.Errorf("expected an empty batch not to arm a window, got %d pending", p)
	}
}

// file: internal/broker/rule_kv_manager_test.go

package broker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// fakeKeyWatcher is a controllable jetstream.KeyWatcher for unit tests.
type fakeKeyWatcher struct {
	updates chan jetstream.KeyValueEntry
	stopped atomic.Bool
}

func newFakeKeyWatcher() *fakeKeyWatcher {
	return &fakeKeyWatcher{updates: make(chan jetstream.KeyValueEntry)}
}

func (f *fakeKeyWatcher) Updates() <-chan jetstream.KeyValueEntry { return f.updates }

func (f *fakeKeyWatcher) Stop() error {
	f.stopped.Store(true)
	return nil
}

// fakeKVEntry is a minimal jetstream.KeyValueEntry for driving the watch loop.
type fakeKVEntry struct {
	key string
	op  jetstream.KeyValueOp
}

func (e fakeKVEntry) Bucket() string                  { return "test" }
func (e fakeKVEntry) Key() string                     { return e.key }
func (e fakeKVEntry) Value() []byte                   { return nil }
func (e fakeKVEntry) Revision() uint64                { return 0 }
func (e fakeKVEntry) Created() time.Time              { return time.Time{} }
func (e fakeKVEntry) Delta() uint64                   { return 0 }
func (e fakeKVEntry) Operation() jetstream.KeyValueOp { return e.op }

// sendEntry sends an entry on an unbuffered channel and fails if the watch loop
// does not consume it promptly — proving the loop is actively reading.
func sendEntry(t *testing.T, ch chan jetstream.KeyValueEntry, e jetstream.KeyValueEntry) {
	t.Helper()
	select {
	case ch <- e:
	case <-time.After(2 * time.Second):
		t.Fatal("watch loop did not consume entry within timeout")
	}
}

// TestRuleKVManagerRecreatesWatcherOnUnexpectedClose verifies that when the
// watcher's Updates() channel closes unexpectedly (while the context is alive),
// runWatch re-establishes a fresh watcher with backoff and resumes consuming.
func TestRuleKVManagerRecreatesWatcherOnUnexpectedClose(t *testing.T) {
	// Shorten backoff so the test runs fast; restore afterward.
	origBase := kvWatchRetryBaseDelay
	kvWatchRetryBaseDelay = 5 * time.Millisecond
	defer func() { kvWatchRetryBaseDelay = origBase }()

	first := newFakeKeyWatcher()
	second := newFakeKeyWatcher()

	var calls int32
	recreated := make(chan struct{}, 4)
	// Factory hands out `second` on the first recreate; any further call is an
	// error so a runaway loop surfaces instead of panicking.
	extra := []*fakeKeyWatcher{second}
	newWatcher := func(context.Context) (jetstream.KeyWatcher, error) {
		n := atomic.AddInt32(&calls, 1)
		recreated <- struct{}{}
		if int(n) <= len(extra) {
			return extra[n-1], nil
		}
		return nil, errors.New("no more watchers")
	}

	m := &RuleKVManager{
		kvBucket:     "test",
		logger:       logger.NewNopLogger(),
		currentRules: make(map[string][]rule.Rule),
		ready:        make(chan struct{}),
		newWatcher:   newWatcher,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		m.runWatch(ctx, first)
		close(done)
	}()

	// 1. Loop is consuming from the first watcher. A delete for a key that
	//    isn't tracked is a safe no-op but proves the read happened.
	sendEntry(t, first.updates, fakeKVEntry{key: "missing", op: jetstream.KeyValueDelete})

	// 2. Simulate an unexpected close of the first watcher.
	close(first.updates)

	// 3. The loop must re-establish a watcher via the factory.
	select {
	case <-recreated:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher was not re-established after unexpected close")
	}

	// 4. The loop must now be consuming from the recreated watcher.
	sendEntry(t, second.updates, fakeKVEntry{key: "missing", op: jetstream.KeyValueDelete})

	// 5. Cancelling the context terminates the loop cleanly.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatch did not exit after context cancellation")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 watcher re-establishment, got %d", got)
	}
}

// TestRuleKVManagerCleanShutdownNoRecreate verifies that a context cancellation
// (clean shutdown) terminates the loop without attempting any re-establishment,
// even if the watcher channel closes concurrently.
func TestRuleKVManagerCleanShutdownNoRecreate(t *testing.T) {
	origBase := kvWatchRetryBaseDelay
	kvWatchRetryBaseDelay = 5 * time.Millisecond
	defer func() { kvWatchRetryBaseDelay = origBase }()

	w := newFakeKeyWatcher()

	var calls int32
	m := &RuleKVManager{
		kvBucket:     "test",
		logger:       logger.NewNopLogger(),
		currentRules: make(map[string][]rule.Rule),
		ready:        make(chan struct{}),
		newWatcher: func(context.Context) (jetstream.KeyWatcher, error) {
			atomic.AddInt32(&calls, 1)
			return newFakeKeyWatcher(), nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.runWatch(ctx, w)
	}()

	// Cancel first (clean shutdown), then close the channel. The loop must exit
	// without re-establishing.
	cancel()
	close(w.updates)

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatch did not exit on clean shutdown")
	}

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected no re-establishment on clean shutdown, got %d", got)
	}
}

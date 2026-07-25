// file: internal/deferred/coalescer.go

// Package deferred implements the execution half of a trailing-edge action
// throttle: it holds evaluated actions for the length of their window and emits
// only the last batch submitted within it.
//
// The rule engine deliberately owns no part of this. A Processor turns a message
// into actions and returns them; anything that delays or replaces an action is
// an execution concern, so the Processor only tags a batch with a
// rule.DeferSpec (key + window) and this package does the waiting.
//
// One Coalescer is created per execution site (JetStream subscriptions, core
// subscriptions, inbound HTTP, scheduler), each wired to that site's own
// executor so retry policy, transport, and metrics stay exactly as they are on
// the immediate path.
package deferred

import (
	"context"
	"sync"
	"time"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// flushConcurrency bounds how many pending batches Stop executes at once, so a
// large pending set cannot open an unbounded number of publishes during
// shutdown.
const flushConcurrency = 8

// Executor runs one fully evaluated action. Implementations are the same
// publish/HTTP paths the immediate route uses.
type Executor func(ctx context.Context, action *rule.Action) error

// Coalescer holds trailing-throttle batches and emits the last one per window.
//
// Window semantics are fixed-from-first, not reset-on-each: the first Submit for
// a key arms a timer for exactly its window, later Submits within that window
// replace the pending batch without extending the deadline, and when the timer
// fires the surviving batch executes. A continuously busy key therefore emits
// once per window instead of starving forever, and emission latency is bounded
// by the window.
type Coalescer struct {
	name    string
	exec    Executor
	timeout time.Duration
	logger  *logger.Logger
	metrics *metrics.Metrics

	mu      sync.Mutex
	pending map[string]*entry
	stopped bool

	// wg tracks in-flight executions (timer-fired and flush) so Stop can wait
	// for them instead of racing shutdown of the transport underneath.
	wg sync.WaitGroup
}

type entry struct {
	batch rule.DeferredBatch
	timer *time.Timer
}

// New creates a Coalescer. name labels log lines and metrics so a deployment
// running several features can tell the sites apart. execTimeout bounds each
// deferred batch's execution; pass 0 for no bound.
func New(name string, exec Executor, execTimeout time.Duration, log *logger.Logger, m *metrics.Metrics) *Coalescer {
	return &Coalescer{
		name:    name,
		exec:    exec,
		timeout: execTimeout,
		logger:  log.With("component", "deferred", "site", name),
		metrics: m,
		pending: make(map[string]*entry),
	}
}

// Submit hands a trailing-throttle batch to the coalescer. It returns
// immediately; the batch fires when its window closes, unless a later Submit
// for the same key replaces it first.
//
// After Stop the coalescer refuses new work and reports false, so callers can
// tell that an action was dropped rather than silently delayed forever.
func (c *Coalescer) Submit(batch rule.DeferredBatch) bool {
	if len(batch.Actions) == 0 {
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		c.logger.Warn("dropping deferred action batch: coalescer is stopped",
			"key", batch.Key, "actions", len(batch.Actions))
		if c.metrics != nil {
			c.metrics.IncThrottleDeferred("dropped")
		}
		return false
	}

	if e, ok := c.pending[batch.Key]; ok {
		// Replace the pending value; the deadline is intentionally untouched.
		e.batch = batch
		c.logger.Debug("coalesced deferred action batch", "key", batch.Key)
		if c.metrics != nil {
			c.metrics.IncThrottleDeferred("coalesced")
		}
		return true
	}

	key := batch.Key
	e := &entry{batch: batch}
	e.timer = time.AfterFunc(batch.Window, func() { c.fire(key) })
	c.pending[key] = e

	c.logger.Debug("opened deferred action window", "key", key, "window", batch.Window)
	return true
}

// fire runs the surviving batch for a key once its window has closed.
func (c *Coalescer) fire(key string) {
	c.mu.Lock()
	e, ok := c.pending[key]
	if !ok || c.stopped {
		// Stop already claimed this entry and is flushing it.
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	batch := e.batch
	c.wg.Add(1)
	c.mu.Unlock()

	defer c.wg.Done()
	c.execute(context.Background(), batch, "window_closed")
}

// execute runs every action in a batch, logging and counting failures. Errors
// do not propagate: the triggering message was acknowledged long ago, so there
// is nothing left to retry into beyond the executor's own retry policy.
func (c *Coalescer) execute(parent context.Context, batch rule.DeferredBatch, reason string) {
	ctx := parent
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, c.timeout)
		defer cancel()
	}

	for _, action := range batch.Actions {
		if err := c.exec(ctx, action); err != nil {
			c.logger.Error("deferred action failed",
				"key", batch.Key, "reason", reason, "error", err)
			if c.metrics != nil {
				c.metrics.IncThrottleDeferred("error")
			}
			continue
		}
		if c.metrics != nil {
			c.metrics.IncThrottleDeferred("emitted")
		}
	}

	c.logger.Debug("emitted deferred action batch",
		"key", batch.Key, "actions", len(batch.Actions), "reason", reason)
}

// Pending reports how many keys currently hold a batch. Test and diagnostic aid.
func (c *Coalescer) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// Stop closes the coalescer and flushes everything still pending, so a
// well-behaved shutdown emits the settled value instead of discarding it. It
// must run before the underlying transport closes.
//
// Flushing is bounded by ctx: if it expires, the batches that had not started
// are dropped and counted. Submits after Stop are refused.
func (c *Coalescer) Stop(ctx context.Context) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true

	batches := make([]rule.DeferredBatch, 0, len(c.pending))
	for key, e := range c.pending {
		e.timer.Stop()
		batches = append(batches, e.batch)
		delete(c.pending, key)
	}
	c.mu.Unlock()

	if len(batches) == 0 {
		c.wg.Wait()
		return
	}

	c.logger.Info("flushing pending deferred action batches on shutdown", "batches", len(batches))

	sem := make(chan struct{}, flushConcurrency)
	var flushWg sync.WaitGroup
	for _, batch := range batches {
		if ctx.Err() != nil {
			c.logger.Warn("shutdown deadline reached; dropping pending deferred batch",
				"key", batch.Key, "actions", len(batch.Actions))
			if c.metrics != nil {
				c.metrics.IncThrottleDeferred("dropped")
			}
			continue
		}

		flushWg.Add(1)
		sem <- struct{}{}
		go func(b rule.DeferredBatch) {
			defer flushWg.Done()
			defer func() { <-sem }()
			c.execute(ctx, b, "shutdown_flush")
		}(batch)
	}

	flushWg.Wait()
	c.wg.Wait()
}

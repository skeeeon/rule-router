// file: internal/broker/responder.go

package broker

import (
	"context"
	"fmt"
	"net/textproto"
	"runtime/debug"
	"sync"

	"github.com/nats-io/nats.go"

	"rule-router/internal/deferred"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Responder serves all rules whose NATS trigger uses core transport — reply:true
// request/reply rules and mode:core fire-and-forget rules. Unlike the
// JetStream-based SubscriptionManager, it uses core NATS subscriptions:
// low-latency, at-most-once, no stream required. Reply rules answer via
// msg.Respond; core-mode rules publish their NATS/HTTP actions like the
// JetStream path does (publishes still honor each action's own mode).
// It lives alongside the JetStream subscription path (under features.router),
// not in place of it — the Processor's transport filters keep the two paths
// from double-firing rules on shared or overlapping subjects.
type Responder struct {
	broker    *NATSBroker
	processor *rule.Processor
	logger    *logger.Logger
	metrics   *metrics.Metrics
	publisher *ActionPublisher
	coalescer *deferred.Coalescer

	mu     sync.Mutex
	subs   []*nats.Subscription
	closed bool
}

// NewResponder creates a Responder bound to the broker's core NATS connection.
func NewResponder(b *NATSBroker, processor *rule.Processor, log *logger.Logger, m *metrics.Metrics) *Responder {
	r := &Responder{
		broker:    b,
		processor: processor,
		logger:    log.With("component", "responder"),
		metrics:   m,
		publisher: b.ActionPublisher(),
	}
	// Trailing-throttle batches run through the same side-effect path as the
	// immediate route. Respond actions never reach it: a reply cannot be
	// deferred, and the loader keeps throttle off respond actions entirely.
	r.coalescer = deferred.New("core", r.executeSideEffect, deferredActionTimeout, log, m)
	return r
}

// Rebuild tears down all existing core subscriptions and re-subscribes every
// rule with a core-transport NATS trigger (reply:true or mode:core). It mirrors
// the scheduler's rebuild pattern (teardown + re-add, no diffing) so it is safe
// to call on file load and on every KV rule change.
//
// Subscriptions are deduplicated per subject: the handler evaluates ALL core
// rules for its subject via the Processor, so one subscription per subject is
// both sufficient and required — a second would double-fire every rule.
func (r *Responder) Rebuild(rules []*rule.Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// After Close, ignore late rebuilds (e.g. a KV event racing shutdown) so we
	// don't re-establish subscriptions the lifecycle has already torn down.
	if r.closed {
		return
	}

	r.unsubscribeAll()

	// Collect unique core subjects and their queue group. The first rule's
	// queue wins; differing queues on one subject are ambiguous (there is one
	// subscription) so later conflicts are logged and ignored.
	queues := make(map[string]string)
	order := make([]string, 0)
	for _, rl := range rules {
		if rl.Trigger.NATS == nil || !rl.Trigger.NATS.IsCore() {
			continue
		}
		subject := rl.Trigger.NATS.Subject
		queue, seen := queues[subject]
		if !seen {
			queues[subject] = rl.Trigger.NATS.Queue
			order = append(order, subject)
		} else if rl.Trigger.NATS.Queue != queue {
			r.logger.Warn("conflicting queue groups for core subject; using the first",
				"subject", subject, "using", queue, "ignored", rl.Trigger.NATS.Queue)
		}
	}

	count := 0
	for _, subject := range order {
		queue := queues[subject]
		handler := r.makeHandler(subject)

		var (
			sub *nats.Subscription
			err error
		)
		if queue != "" {
			sub, err = r.broker.GetNATSConn().QueueSubscribe(subject, queue, handler)
		} else {
			sub, err = r.broker.GetNATSConn().Subscribe(subject, handler)
		}
		if err != nil {
			r.logger.Error("failed to subscribe core subject", "subject", subject, "queue", queue, "error", err)
			continue
		}
		r.subs = append(r.subs, sub)
		count++
		r.logger.Info("core subscription established", "subject", subject, "queue", queue)
	}

	r.logger.Info("core subscriptions rebuilt", "subscriptions", count)
}

// makeHandler returns the core NATS message handler for a subject. It
// evaluates the subject's core-transport rules via the shared Processor, then:
//   - respond actions answer the request via msg.Respond (first respond wins);
//     with no reply subject the requester is gone, so they are skipped — the
//     honest NATS signal is the requester's own timeout
//   - NATS actions publish with the shared retrying publisher
//   - HTTP actions run on the gateway's executor when the feature is enabled
func (r *Responder) makeHandler(triggerSubject string) nats.MsgHandler {
	return func(msg *nats.Msg) {
		// nats.go runs subscription callbacks without a recover of its own, so an
		// unguarded panic here takes the process down — unlike the JetStream
		// worker and the inbound HTTP worker, which contain theirs. Core delivery
		// is at-most-once, so there is nothing to redeliver into: log it, count
		// it, drop the message.
		defer func() {
			if rec := recover(); rec != nil {
				r.logger.Error("panic recovered in core subscription handler",
					"panic", rec,
					"subject", msg.Subject,
					"stack", string(debug.Stack()))
				if r.metrics != nil {
					r.metrics.IncMessagesTotal("error")
				}
			}
		}()

		if r.metrics != nil {
			r.metrics.IncMessagesTotal("received")
		}

		headers := make(map[string]string)
		for k, v := range msg.Header {
			if len(v) > 0 {
				headers[textproto.CanonicalMIMEHeaderKey(k)] = v[0]
			}
		}

		outcome, err := r.processor.ProcessForSubscription(triggerSubject, msg.Subject, msg.Data, headers, rule.CoreRuleFilter)
		if err != nil {
			r.logger.Error("failed to process core-delivered message", "subject", msg.Subject, "error", err)
			if r.metrics != nil {
				r.metrics.IncMessagesTotal("error")
			}
			return
		}

		// Trailing-throttle batches fire when their window closes.
		for _, batch := range outcome.Deferred {
			r.coalescer.Submit(batch)
		}

		// Core delivery is at-most-once: there is no ack window to redeliver
		// failed actions into, so failures are logged and counted, not retried
		// beyond the publisher's own retry policy.
		//
		// The deadline matters more here than on the JetStream path, which is
		// already bounded by the consumer's ack wait. nats.go delivers a
		// subscription's messages one at a time, so everything below holds this
		// subject's queue: without a bound, one action multiplying its own
		// retries (3 × ackTimeout, or an HTTP client timeout per attempt) stalls
		// the subject long enough to overflow the pending buffer and drop
		// messages. Capped here, a bad action costs one slow message.
		ctx, cancel := context.WithTimeout(context.Background(), coreActionTimeout)
		defer cancel()

		responded := false
		for _, a := range outcome.Immediate {
			switch {
			case a.Respond != nil:
				if msg.Reply == "" {
					r.logger.Debug("respond action but message has no reply subject; skipping", "subject", msg.Subject)
					continue
				}
				if responded {
					continue // first respond wins
				}
				r.respond(msg, a.Respond)
				responded = true

			case a.NATS != nil:
				if err := r.publisher.PublishWithRetry(ctx, a.NATS); err != nil {
					r.logger.Error("failed to publish NATS action from core subscription",
						"subject", msg.Subject, "actionSubject", a.NATS.Subject, "error", err)
					if r.metrics != nil {
						r.metrics.IncActionsTotal("error")
					}
					continue
				}
				if r.metrics != nil {
					r.metrics.IncActionsTotal("success")
				}

			case a.HTTP != nil:
				exec := r.broker.GetHTTPExecutor()
				if exec == nil {
					r.logger.Warn("HTTP action skipped - gateway feature not enabled",
						"url", a.HTTP.URL,
						"hint", "Enable features.gateway to handle HTTP actions")
					if r.metrics != nil {
						r.metrics.IncActionsTotal("skipped")
					}
					continue
				}
				if err := exec.ExecuteHTTPAction(ctx, a.HTTP); err != nil {
					r.logger.Error("failed to execute HTTP action from core subscription",
						"subject", msg.Subject, "url", a.HTTP.URL, "error", err)
					if r.metrics != nil {
						r.metrics.IncActionsTotal("error")
					}
					continue
				}
				if r.metrics != nil {
					r.metrics.IncActionsTotal("success")
				}
			}
		}

		if r.metrics != nil {
			r.metrics.IncMessagesTotal("processed")
		}
	}
}

// respond answers a request with the evaluated respond action payload.
func (r *Responder) respond(msg *nats.Msg, respond *rule.RespondAction) {
	reply := nats.NewMsg(msg.Reply)
	if respond.Passthrough {
		reply.Data = respond.RawPayload
	} else {
		reply.Data = []byte(respond.Payload)
	}
	if len(respond.Headers) > 0 {
		reply.Header = make(nats.Header)
		for k, v := range respond.Headers {
			reply.Header.Set(k, v)
		}
	}
	if err := msg.RespondMsg(reply); err != nil {
		r.logger.Error("failed to respond to request", "subject", msg.Subject, "error", err)
		if r.metrics != nil {
			r.metrics.IncActionsTotal("error")
		}
		return
	}
	if r.metrics != nil {
		r.metrics.IncActionsTotal("success")
	}
}

// unsubscribeAll removes all current subscriptions. Caller must hold r.mu.
func (r *Responder) unsubscribeAll() {
	for _, sub := range r.subs {
		if err := sub.Unsubscribe(); err != nil {
			r.logger.Debug("failed to unsubscribe core subject", "error", err)
		}
	}
	r.subs = nil
}

// Close unsubscribes all core subscriptions and marks the responder closed so
// any later Rebuild (e.g. a KV event racing shutdown) is a no-op.
func (r *Responder) Close() {
	r.mu.Lock()
	r.closed = true
	r.unsubscribeAll()
	r.mu.Unlock()

	// Flush pending trailing-throttle batches after the subscriptions are gone
	// (so nothing new arrives) but before the NATS connection closes. Held
	// outside r.mu: the flush publishes, and publishing must not take this lock.
	flushCtx, cancel := context.WithTimeout(context.Background(), deferredFlushTimeout)
	defer cancel()
	r.coalescer.Stop(flushCtx)
}

// executeSideEffect runs one deferred NATS or HTTP action from a core-transport
// rule. Mirrors the immediate path in makeHandler, minus respond (which cannot
// be deferred — there is no request left to answer).
func (r *Responder) executeSideEffect(ctx context.Context, action *rule.Action) error {
	switch {
	case action.NATS != nil:
		if err := r.publisher.PublishWithRetry(ctx, action.NATS); err != nil {
			if r.metrics != nil {
				r.metrics.IncActionsTotal("error")
			}
			return fmt.Errorf("failed to publish deferred NATS action to %s: %w", action.NATS.Subject, err)
		}
		if r.metrics != nil {
			r.metrics.IncActionsTotal("success")
		}

	case action.HTTP != nil:
		exec := r.broker.GetHTTPExecutor()
		if exec == nil {
			r.logger.Warn("deferred HTTP action skipped - gateway feature not enabled",
				"url", action.HTTP.URL,
				"hint", "Enable features.gateway to handle HTTP actions")
			if r.metrics != nil {
				r.metrics.IncActionsTotal("skipped")
			}
			return nil
		}
		if err := exec.ExecuteHTTPAction(ctx, action.HTTP); err != nil {
			if r.metrics != nil {
				r.metrics.IncActionsTotal("error")
			}
			return fmt.Errorf("failed to execute deferred HTTP action to %s: %w", action.HTTP.URL, err)
		}
		if r.metrics != nil {
			r.metrics.IncActionsTotal("success")
		}
	}

	return nil
}

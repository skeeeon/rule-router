// file: internal/broker/responder.go

package broker

import (
	"context"
	"net/textproto"
	"sync"

	"github.com/nats-io/nats.go"

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
	publisher *actionPublisher

	mu     sync.Mutex
	subs   []*nats.Subscription
	closed bool
}

// NewResponder creates a Responder bound to the broker's core NATS connection.
func NewResponder(b *NATSBroker, processor *rule.Processor, log *logger.Logger, m *metrics.Metrics) *Responder {
	return &Responder{
		broker:    b,
		processor: processor,
		logger:    log.With("component", "responder"),
		metrics:   m,
		publisher: newActionPublisher(b.GetNATSConn(), b.GetJetStream(), &b.config.NATS.Publish, log, m),
	}
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
		if r.metrics != nil {
			r.metrics.IncMessagesTotal("received")
		}

		headers := make(map[string]string)
		for k, v := range msg.Header {
			if len(v) > 0 {
				headers[textproto.CanonicalMIMEHeaderKey(k)] = v[0]
			}
		}

		actions, err := r.processor.ProcessForSubscription(triggerSubject, msg.Subject, msg.Data, headers, rule.CoreRuleFilter)
		if err != nil {
			r.logger.Error("failed to process core-delivered message", "subject", msg.Subject, "error", err)
			if r.metrics != nil {
				r.metrics.IncMessagesTotal("error")
			}
			return
		}

		// Core delivery is at-most-once: there is no ack window to redeliver
		// failed actions into, so failures are logged and counted, not retried
		// beyond the publisher's own retry policy.
		ctx := context.Background()
		responded := false
		for _, a := range actions {
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
	defer r.mu.Unlock()
	r.closed = true
	r.unsubscribeAll()
}

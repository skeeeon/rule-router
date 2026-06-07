// file: internal/broker/responder.go

package broker

import (
	"net/textproto"
	"sync"

	"github.com/nats-io/nats.go"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Responder answers NATS request/reply traffic for rules whose trigger has
// reply:true. Unlike the JetStream-based SubscriptionManager, it uses core NATS
// subscriptions — low-latency, at-most-once — and replies via msg.Respond, which
// is the correct transport for synchronous request/reply. It lives alongside the
// JetStream subscription path (under features.router), not in place of it.
type Responder struct {
	natsConn  *nats.Conn
	processor *rule.Processor
	logger    *logger.Logger
	metrics   *metrics.Metrics

	mu     sync.Mutex
	subs   []*nats.Subscription
	closed bool
}

// NewResponder creates a Responder bound to the core NATS connection.
func NewResponder(nc *nats.Conn, processor *rule.Processor, log *logger.Logger, m *metrics.Metrics) *Responder {
	return &Responder{
		natsConn:  nc,
		processor: processor,
		logger:    log.With("component", "responder"),
		metrics:   m,
	}
}

// Rebuild tears down all existing reply subscriptions and re-subscribes every
// rule with a reply:true NATS trigger. It mirrors the scheduler's rebuild
// pattern (teardown + re-add, no diffing) so it is safe to call on file load and
// on every KV rule change.
func (r *Responder) Rebuild(rules []*rule.Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// After Close, ignore late rebuilds (e.g. a KV event racing shutdown) so we
	// don't re-establish subscriptions the lifecycle has already torn down.
	if r.closed {
		return
	}

	r.unsubscribeAll()

	count := 0
	for _, rl := range rules {
		if rl.Trigger.NATS == nil || !rl.Trigger.NATS.Reply {
			continue
		}
		subject := rl.Trigger.NATS.Subject
		queue := rl.Trigger.NATS.Queue
		handler := r.makeHandler(subject)

		var (
			sub *nats.Subscription
			err error
		)
		if queue != "" {
			sub, err = r.natsConn.QueueSubscribe(subject, queue, handler)
		} else {
			sub, err = r.natsConn.Subscribe(subject, handler)
		}
		if err != nil {
			r.logger.Error("failed to subscribe reply subject", "subject", subject, "queue", queue, "error", err)
			continue
		}
		r.subs = append(r.subs, sub)
		count++
		r.logger.Info("reply responder subscribed", "subject", subject, "queue", queue)
	}

	r.logger.Info("reply responder rebuilt", "replySubscriptions", count)
}

// makeHandler returns the core NATS message handler for a reply subject. It
// evaluates rules via the shared Processor and answers the request with the
// first matching respond action's payload. No respond action (conditions
// unmatched) leaves the request unanswered — the requester times out, which is
// the honest NATS signal.
func (r *Responder) makeHandler(triggerSubject string) nats.MsgHandler {
	return func(msg *nats.Msg) {
		if msg.Reply == "" {
			r.logger.Debug("reply rule received a message with no reply subject; ignoring", "subject", msg.Subject)
			return
		}

		headers := make(map[string]string)
		for k, v := range msg.Header {
			if len(v) > 0 {
				headers[textproto.CanonicalMIMEHeaderKey(k)] = v[0]
			}
		}

		actions, err := r.processor.ProcessForSubscription(triggerSubject, msg.Subject, msg.Data, headers)
		if err != nil {
			r.logger.Error("failed to process reply request", "subject", msg.Subject, "error", err)
			if r.metrics != nil {
				r.metrics.IncActionsTotal("error")
			}
			return
		}

		for _, a := range actions {
			if a.Respond == nil {
				continue
			}
			reply := nats.NewMsg(msg.Reply)
			if a.Respond.Passthrough {
				reply.Data = a.Respond.RawPayload
			} else {
				reply.Data = []byte(a.Respond.Payload)
			}
			if len(a.Respond.Headers) > 0 {
				reply.Header = make(nats.Header)
				for k, v := range a.Respond.Headers {
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
			return // first respond wins
		}

		r.logger.Debug("reply request matched no respond action; leaving unanswered", "subject", msg.Subject)
	}
}

// unsubscribeAll removes all current subscriptions. Caller must hold r.mu.
func (r *Responder) unsubscribeAll() {
	for _, sub := range r.subs {
		if err := sub.Unsubscribe(); err != nil {
			r.logger.Debug("failed to unsubscribe reply subject", "error", err)
		}
	}
	r.subs = nil
}

// Close unsubscribes all reply subscriptions and marks the responder closed so
// any later Rebuild (e.g. a KV event racing shutdown) is a no-op.
func (r *Responder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.unsubscribeAll()
}

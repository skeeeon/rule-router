// file: internal/broker/publisher.go

package broker

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// maxRetryJitter is the maximum jitter added to retry delays to prevent thundering herd
const maxRetryJitter = 25 * time.Millisecond

// effectivePublishMode resolves the publish transport for an action: the
// per-action mode override when set, the global publish config otherwise.
func effectivePublishMode(action *rule.NATSAction, globalMode string) string {
	if action.Mode != "" {
		return action.Mode
	}
	return globalMode
}

// actionPublisher publishes rule NATS actions with retry and per-action mode
// resolution. Shared by the JetStream SubscriptionManager and the core-NATS
// Responder so both transports get identical publish semantics.
type actionPublisher struct {
	natsConn *nats.Conn
	js       jetstream.JetStream
	cfg      *config.PublishConfig
	logger   *logger.Logger
	metrics  *metrics.Metrics
}

func newActionPublisher(
	nc *nats.Conn,
	js jetstream.JetStream,
	cfg *config.PublishConfig,
	log *logger.Logger,
	m *metrics.Metrics,
) *actionPublisher {
	return &actionPublisher{
		natsConn: nc,
		js:       js,
		cfg:      cfg,
		logger:   log,
		metrics:  m,
	}
}

// PublishWithRetry publishes a NATS action with exponential backoff and jitter.
func (ap *actionPublisher) PublishWithRetry(ctx context.Context, action *rule.NATSAction) error {
	maxRetries := ap.cfg.MaxRetries
	baseDelay := ap.cfg.RetryBaseDelay
	publishMode := effectivePublishMode(action, ap.cfg.Mode)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		var err error
		if publishMode == "core" {
			err = ap.publishCore(action)
		} else {
			err = ap.publishJetStream(ctx, action)
		}

		if err == nil {
			return nil
		}

		lastErr = err
		ap.logger.Warn("action publish failed, will retry",
			"attempt", attempt+1, "maxRetries", maxRetries, "subject", action.Subject, "error", err)
		if ap.metrics != nil {
			ap.metrics.IncActionPublishFailures()
		}

		if attempt == maxRetries-1 {
			break
		}

		// Exponential backoff with jitter
		delay := baseDelay * time.Duration(1<<attempt)
		jitter := time.Duration(rand.Intn(int(maxRetryJitter.Milliseconds()))) * time.Millisecond

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
		case <-time.After(delay + jitter):
		}
	}

	return fmt.Errorf("failed to publish after %d attempts (mode: %s): %w", maxRetries, publishMode, lastErr)
}

// publishJetStream publishes to JetStream using the async model for high throughput.
func (ap *actionPublisher) publishJetStream(ctx context.Context, action *rule.NATSAction) error {
	msg := newActionMsg(action)

	// Publish async
	ackF, err := ap.js.PublishMsgAsync(msg)
	if err != nil {
		// This error occurs if the async buffer is full (backpressure).
		return fmt.Errorf("jetstream async publish failed on send: %w", err)
	}

	// Wait for acknowledgement with timeout
	pubCtx, cancel := context.WithTimeout(ctx, ap.cfg.AckTimeout)
	defer cancel()

	select {
	case <-ackF.Ok():
		return nil // Publish was successful.
	case err := <-ackF.Err():
		// The server returned an error for this publish.
		if errors.Is(err, nats.ErrNoResponders) {
			return fmt.Errorf("jetstream publish failed: no stream is configured for action subject '%s'", action.Subject)
		}
		return fmt.Errorf("jetstream async publish failed on ack: %w", err)
	case <-pubCtx.Done():
		// We timed out waiting for the server's acknowledgement.
		return fmt.Errorf("timeout waiting for publish acknowledgement: %w", pubCtx.Err())
	}
}

// publishCore publishes to core NATS (fire-and-forget, no ack).
func (ap *actionPublisher) publishCore(action *rule.NATSAction) error {
	msg := newActionMsg(action)

	// Fast path: if there are no headers, use the simple Publish method.
	if len(msg.Header) == 0 {
		if err := ap.natsConn.Publish(msg.Subject, msg.Data); err != nil {
			return fmt.Errorf("core nats publish failed: %w", err)
		}
		return nil
	}

	if err := ap.natsConn.PublishMsg(msg); err != nil {
		return fmt.Errorf("core nats publish with headers failed: %w", err)
	}

	return nil
}

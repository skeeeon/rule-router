// file: internal/broker/subscription.go

package broker

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// SubscriptionManager manages JetStream pull subscriptions for rule subjects
// with parallel message processing and configurable action publishing (JetStream or Core).
//
// ARCHITECTURE: This manager uses a "Shared Fetch Channel" pattern for each subscription.
// For each rule subject, it starts:
//   - ONE dedicated "fetcher" goroutine that efficiently pulls large batches of messages from NATS.
//   - A pool of "processingWorker" goroutines that consume from an in-memory channel fed by the fetcher.
// This decouples network I/O from CPU-bound processing, maximizing single-node performance and throughput.
type SubscriptionManager struct {
	natsConn      *nats.Conn
	jetStream     jetstream.JetStream
	logger        *logger.Logger
	metrics       *metrics.Metrics
	processor     *rule.Processor
	consumerCfg   *config.ConsumerConfig
	publishCfg    *config.PublishConfig
	subscriptions []*Subscription
	wg            sync.WaitGroup
	mu            sync.RWMutex
}

// Subscription represents a single JetStream pull consumer.
type Subscription struct {
	Subject      string
	ConsumerName string
	StreamName   string
	Consumer     jetstream.Consumer
	Workers      int // Number of concurrent workers
	cancel       context.CancelFunc
	logger       *logger.Logger
	consumerCfg  *config.ConsumerConfig
}

// NewSubscriptionManager creates a new subscription manager.
func NewSubscriptionManager(
	natsConn *nats.Conn,
	js jetstream.JetStream,
	processor *rule.Processor,
	logger *logger.Logger,
	metrics *metrics.Metrics,
	consumerConfig *config.ConsumerConfig,
	publishConfig *config.PublishConfig,
) *SubscriptionManager {
	return &SubscriptionManager{
		natsConn:      natsConn,
		jetStream:     js,
		logger:        logger,
		metrics:       metrics,
		processor:     processor,
		consumerCfg:   consumerConfig,
		publishCfg:    publishConfig,
		subscriptions: make([]*Subscription, 0),
	}
}

// AddSubscription creates a consumer handle for a subject.
func (sm *SubscriptionManager) AddSubscription(streamName, consumerName, subject string, workers int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	stream, err := sm.jetStream.Stream(context.Background(), streamName)
	if err != nil {
		return fmt.Errorf("failed to get stream '%s': %w", streamName, err)
	}

	consumer, err := stream.Consumer(context.Background(), consumerName)
	if err != nil {
		return fmt.Errorf("failed to get consumer '%s': %w", consumerName, err)
	}

	sub := &Subscription{
		Subject:      subject,
		ConsumerName: consumerName,
		StreamName:   streamName,
		Consumer:     consumer,
		Workers:      workers,
		logger:       sm.logger,
		consumerCfg:  sm.consumerCfg,
	}

	sm.subscriptions = append(sm.subscriptions, sub)

	sm.logger.Info("subscription added",
		"stream", streamName,
		"consumer", consumerName,
		"subject", subject,
		"workers", workers)

	return nil
}

// Start begins consuming messages from all subscriptions.
func (sm *SubscriptionManager) Start(ctx context.Context) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.subscriptions) == 0 {
		return fmt.Errorf("no subscriptions configured")
	}

	sm.logger.Info("starting subscription manager",
		"subscriptions", len(sm.subscriptions),
		"fetchBatchSize", sm.consumerCfg.FetchBatchSize,
		"fetchTimeout", sm.consumerCfg.FetchTimeout)

	for _, sub := range sm.subscriptions {
		subCtx, cancel := context.WithCancel(ctx)
		sub.cancel = cancel

		msgChan := make(chan jetstream.Msg, sm.consumerCfg.FetchBatchSize*2)

		sm.wg.Add(1)
		go sm.fetcher(subCtx, sub, msgChan)

		for i := 0; i < sub.Workers; i++ {
			sm.wg.Add(1)
			go sm.processingWorker(subCtx, msgChan, i, sub.Subject)
		}

		sm.logger.Info("subscription started",
			"subject", sub.Subject,
			"stream", sub.StreamName,
			"consumer", sub.ConsumerName,
			"workers", sub.Workers)
	}

	return nil
}

// fetcher pulls messages from JetStream and feeds them to the processing workers.
func (sm *SubscriptionManager) fetcher(ctx context.Context, sub *Subscription, msgChan chan<- jetstream.Msg) {
	defer sm.wg.Done()
	defer close(msgChan)

	sub.logger.Debug("fetcher started",
		"subject", sub.Subject,
		"batchSize", sub.consumerCfg.FetchBatchSize)

	for {
		select {
		case <-ctx.Done():
			sub.logger.Debug("fetcher shutting down", "subject", sub.Subject)
			return
		default:
		}

		msgs, err := sub.Consumer.FetchNoWait(sub.consumerCfg.FetchBatchSize)
		if err != nil {
			if errors.Is(err, jetstream.ErrNoMessages) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			sub.logger.Error("fetch failed", "subject", sub.Subject, "error", err)
			time.Sleep(time.Second)
			continue
		}

		for msg := range msgs.Messages() {
			select {
			case msgChan <- msg:
			case <-ctx.Done():
				sub.logger.Debug("fetcher shutting down during message queuing", "subject", sub.Subject)
				return
			}
		}
	}
}

// processingWorker consumes messages from the shared channel and executes the rule processor.
// It includes robust panic recovery and intelligent error handling to prevent worker crashes
// and redelivery loops for malformed "poison messages".
func (sm *SubscriptionManager) processingWorker(ctx context.Context, msgChan <-chan jetstream.Msg, workerID int, subject string) {
	defer sm.wg.Done()

	sm.logger.Debug("processing worker started", "subject", subject, "workerID", workerID)

	for msg := range msgChan {
		// Capture the message in a new variable for the deferred function to close over.
		// This is a Go best practice to avoid capturing the loop variable `msg` which changes on each iteration.
		currentMsg := msg

		// Use an anonymous function to scope the defer, ensuring it runs for each message.
		func() {
			// --- START OF PANIC RECOVERY FIX ---
			// This defer block catches any panics that occur during message processing.
			// This prevents a single malformed message from crashing the entire worker goroutine.
			defer func() {
				if r := recover(); r != nil {
					sm.logger.Error("panic recovered in processing worker",
						"panic", r,
						"subject", currentMsg.Subject(),
						"stack", string(debug.Stack()),
					)

					// A message that causes a panic is a "poison message" and should be terminated
					// to prevent it from being redelivered and causing another panic.
					if termErr := currentMsg.Term(); termErr != nil {
						sm.logger.Error("failed to Terminate message after panic", "subject", currentMsg.Subject(), "error", termErr)
					}

					if sm.metrics != nil {
						sm.metrics.IncMessagesTotal("error")
					}
				}
			}()
			// --- END OF PANIC RECOVERY FIX ---

			// Process the message. Any errors or panics will be handled gracefully.
			if err := sm.processMessage(ctx, currentMsg); err != nil {
				sm.logger.Error("failed to process message", "subject", currentMsg.Subject(), "error", err)

				// --- START OF TERMINAL ERROR FIX ---
				// Differentiate between transient and terminal errors. A terminal error (e.g., bad JSON)
				// will never succeed, so we use msg.Term() to stop JetStream from redelivering it.
				// A transient error (e.g., temporary network issue) might succeed on retry, so we use msg.Nak().
				if isTerminalError(err) {
					sm.logger.Warn("terminating malformed message to prevent redelivery loop", "subject", currentMsg.Subject())
					if termErr := currentMsg.Term(); termErr != nil {
						sm.logger.Error("failed to Terminate message", "subject", currentMsg.Subject(), "error", termErr)
					}
				} else {
					// For other, potentially transient errors, Nak the message to allow retries up to `maxDeliver`.
					if nakErr := currentMsg.Nak(); nakErr != nil {
						sm.logger.Error("failed to NAK message", "subject", currentMsg.Subject(), "error", nakErr)
					}
				}
				// --- END OF TERMINAL ERROR FIX ---

				if sm.metrics != nil {
					sm.metrics.IncMessagesTotal("error")
				}
			} else {
				// Message processed successfully, acknowledge it.
				if ackErr := currentMsg.Ack(); ackErr != nil {
					sm.logger.Error("failed to ACK message", "subject", currentMsg.Subject(), "error", ackErr)
				}
				if sm.metrics != nil {
					sm.metrics.IncMessagesTotal("processed")
				}
			}
		}() // Immediately invoke the anonymous function.
	}

	sm.logger.Debug("processing worker finished", "subject", subject, "workerID", workerID)
}

// processMessage handles a single message through the rule engine.
func (sm *SubscriptionManager) processMessage(ctx context.Context, msg jetstream.Msg) error {
	start := time.Now()
	if sm.metrics != nil {
		sm.metrics.IncMessagesTotal("received")
	}

	sm.logger.Debug("processing message", "subject", msg.Subject(), "size", len(msg.Data()))

	// Extract headers
	headers := make(map[string]string)
	if msg.Headers() != nil {
		for key, values := range msg.Headers() {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	// Process through rule engine (uses ProcessNATS internally via backward compatibility layer)
	actions, err := sm.processor.ProcessWithSubject(msg.Subject(), msg.Data(), headers)
	if err != nil {
		// This error will be caught by the caller (processingWorker) and handled appropriately.
		return fmt.Errorf("rule processing failed: %w", err)
	}

	// Publish all matched actions
	for _, action := range actions {
		// PHASE 2 UPDATE: Check for NATS action (new format)
		if action.NATS != nil {
			if err := sm.publishActionWithRetry(ctx, action.NATS); err != nil {
				sm.logger.Error("failed to publish NATS action after retries",
					"actionSubject", action.NATS.Subject,
					"error", err)
				if sm.metrics != nil {
					sm.metrics.IncActionsTotal("error")
				}
				// Return the error to allow the message to be NAK'd, as the action failed.
				return fmt.Errorf("failed to publish NATS action: %w", err)
			}
			if sm.metrics != nil {
				sm.metrics.IncActionsTotal("success")
				sm.metrics.IncRuleMatches()
			}
		} else if action.HTTP != nil {
			// PHASE 3: HTTP actions will be handled by http-gateway
			sm.logger.Warn("HTTP action detected in rule-router - HTTP actions not supported in this application",
				"actionURL", action.HTTP.URL,
				"hint", "Use http-gateway for HTTP actions")
		} else {
			sm.logger.Error("action has neither NATS nor HTTP configuration - this should never happen",
				"subject", msg.Subject())
		}
	}

	duration := time.Since(start)
	sm.logger.Debug("message processed", "subject", msg.Subject(), "duration", duration, "actionsPublished", len(actions))
	return nil
}

// publishActionWithRetry publishes a NATS action with exponential backoff and jitter.
func (sm *SubscriptionManager) publishActionWithRetry(ctx context.Context, action *rule.NATSAction) error {
	maxRetries := sm.publishCfg.MaxRetries
	baseDelay := sm.publishCfg.RetryBaseDelay
	publishMode := sm.publishCfg.Mode

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		var err error
		if publishMode == "core" {
			err = sm.publishCore(ctx, action)
		} else {
			err = sm.publishJetStream(ctx, action)
		}

		if err == nil {
			return nil
		}

		lastErr = err
		sm.logger.Warn("action publish failed, will retry",
			"attempt", attempt+1, "maxRetries", maxRetries, "subject", action.Subject, "error", err)
		if sm.metrics != nil {
			sm.metrics.IncActionPublishFailures()
		}

		if attempt == maxRetries-1 {
			break
		}

		// Exponential backoff with jitter
		delay := baseDelay * time.Duration(1<<attempt)
		jitter := time.Duration(rand.Intn(25)) * time.Millisecond

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
		case <-time.After(delay + jitter):
		}
	}

	return fmt.Errorf("failed to publish after %d attempts (mode: %s): %w", maxRetries, publishMode, lastErr)
}

// publishJetStream publishes to JetStream using the async model for high throughput.
func (sm *SubscriptionManager) publishJetStream(ctx context.Context, action *rule.NATSAction) error {
	// Prepare payload
	var payloadBytes []byte
	if action.Passthrough {
		payloadBytes = action.RawPayload
	} else {
		payloadBytes = []byte(action.Payload)
	}

	// Create NATS message
	msg := nats.NewMsg(action.Subject)
	msg.Data = payloadBytes

	// Add headers if present
	if len(action.Headers) > 0 {
		msg.Header = make(nats.Header)
		for key, value := range action.Headers {
			msg.Header.Set(key, value)
		}
	}

	// Publish async
	ackF, err := sm.jetStream.PublishMsgAsync(msg)
	if err != nil {
		// This error occurs if the async buffer is full (backpressure).
		return fmt.Errorf("jetstream async publish failed on send: %w", err)
	}

	// Wait for acknowledgement with timeout
	pubCtx, cancel := context.WithTimeout(ctx, sm.publishCfg.AckTimeout)
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
func (sm *SubscriptionManager) publishCore(ctx context.Context, action *rule.NATSAction) error {
	// Prepare payload
	var payloadBytes []byte
	if action.Passthrough {
		payloadBytes = action.RawPayload
	} else {
		payloadBytes = []byte(action.Payload)
	}

	// Fast path: if there are no headers, use the simple Publish method.
	if len(action.Headers) == 0 {
		if err := sm.natsConn.Publish(action.Subject, payloadBytes); err != nil {
			return fmt.Errorf("core nats publish failed: %w", err)
		}
		return nil
	}

	// With headers, we must construct a full nats.Msg object.
	msg := nats.NewMsg(action.Subject)
	msg.Data = payloadBytes
	msg.Header = make(nats.Header)
	for key, value := range action.Headers {
		msg.Header.Set(key, value)
	}

	if err := sm.natsConn.PublishMsg(msg); err != nil {
		return fmt.Errorf("core nats publish with headers failed: %w", err)
	}

	return nil
}

// Stop gracefully shuts down all subscriptions.
func (sm *SubscriptionManager) Stop() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.logger.Info("stopping all subscriptions", "count", len(sm.subscriptions))

	// Cancelling the context is the primary signal for all goroutines to stop.
	for _, sub := range sm.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

	// Wait for all goroutines (fetchers and workers) to finish.
	sm.wg.Wait()
	sm.logger.Info("all subscriptions stopped")
	return nil
}

// GetSubscriptionCount returns the number of active subscriptions.
func (sm *SubscriptionManager) GetSubscriptionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.subscriptions)
}

// isTerminalError checks if an error is permanent and should not be retried.
// This is used to decide whether to Terminate or Nak a message.
func isTerminalError(err error) bool {
	// A JSON unmarshalling error is a classic terminal error, as the message
	// payload is fundamentally invalid and will never be successfully processed.
	if strings.Contains(err.Error(), "invalid character") ||
		strings.Contains(err.Error(), "unexpected end of JSON") {
		return true
	}

	// Add more terminal error patterns as needed based on production experience.
	return false
}

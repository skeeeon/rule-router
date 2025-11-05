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
// using the JetStream Messages() iterator pattern for optimal performance.
//
// ARCHITECTURE: This manager uses the "Messages() Iterator" pattern recommended by NATS.
// For each rule subject, it creates:
//   - ONE Messages() iterator with internal pre-buffering and optimization
//   - A pool of "worker" goroutines that call iter.Next() in a blocking loop
// This leverages JetStream's built-in optimizations while maintaining a work queue pattern.
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

// Subscription represents a single JetStream pull consumer with Messages() iterator.
type Subscription struct {
	Subject      string
	ConsumerName string
	StreamName   string
	Consumer     jetstream.Consumer
	Workers      int // Number of concurrent workers
	iterator     jetstream.MessagesContext // Messages() iterator
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

// Start begins consuming messages from all subscriptions using Messages() iterator.
func (sm *SubscriptionManager) Start(ctx context.Context) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.subscriptions) == 0 {
		return fmt.Errorf("no subscriptions configured")
	}

	sm.logger.Info("starting subscription manager with Messages() iterator pattern",
		"subscriptions", len(sm.subscriptions),
		"fetchBatchSize", sm.consumerCfg.FetchBatchSize,
		"fetchTimeout", sm.consumerCfg.FetchTimeout)

	for _, sub := range sm.subscriptions {
		if err := sm.startSubscription(ctx, sub); err != nil {
			return fmt.Errorf("failed to start subscription for '%s': %w", sub.Subject, err)
		}
	}

	sm.logger.Info("all subscriptions started successfully")
	return nil
}

// startSubscription initializes a Messages() iterator and worker pool for a single subscription.
func (sm *SubscriptionManager) startSubscription(ctx context.Context, sub *Subscription) error {
	subCtx, cancel := context.WithCancel(ctx)
	sub.cancel = cancel

	// Calculate heartbeat duration: half of fetch timeout, minimum 1 second
	// This ensures we detect stalled connections before the fetch timeout expires
	heartbeatDuration := sm.consumerCfg.FetchTimeout / 2
	if heartbeatDuration < 1*time.Second {
		heartbeatDuration = 1 * time.Second
	}

	sm.logger.Debug("creating Messages() iterator",
		"subject", sub.Subject,
		"pullMaxMessages", sm.consumerCfg.FetchBatchSize,
		"pullExpiry", sm.consumerCfg.FetchTimeout,
		"heartbeat", heartbeatDuration)

	// Create Messages() iterator with JetStream optimizations
	// This establishes a persistent pull subscription with internal pre-buffering
	iter, err := sub.Consumer.Messages(
		jetstream.PullMaxMessages(sm.consumerCfg.FetchBatchSize),
		jetstream.PullExpiry(sm.consumerCfg.FetchTimeout),
		jetstream.PullHeartbeat(heartbeatDuration),
	)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create Messages() iterator: %w", err)
	}

	// Store iterator for cleanup during shutdown
	sub.iterator = iter

	sm.logger.Info("Messages() iterator created successfully",
		"subject", sub.Subject,
		"stream", sub.StreamName,
		"consumer", sub.ConsumerName,
		"pullMaxMessages", sm.consumerCfg.FetchBatchSize,
		"pullExpiry", sm.consumerCfg.FetchTimeout,
		"heartbeat", heartbeatDuration)

	// Start worker pool - each worker calls iter.Next() in a blocking loop
	// This creates a work queue pattern where multiple workers compete for messages
	for i := 0; i < sub.Workers; i++ {
		sm.wg.Add(1)
		go sm.messageWorker(subCtx, sub, i)
	}

	sm.logger.Info("subscription started with worker pool",
		"subject", sub.Subject,
		"workers", sub.Workers)

	return nil
}

// messageWorker continuously pulls messages from the iterator and processes them.
// This replaces both the fetcher and processingWorker pattern with a simpler approach
// that leverages JetStream's internal optimizations.
func (sm *SubscriptionManager) messageWorker(ctx context.Context, sub *Subscription, workerID int) {
	defer sm.wg.Done()

	sm.logger.Debug("message worker started",
		"subject", sub.Subject,
		"workerID", workerID)

	for {
		// Check for context cancellation before blocking on Next()
		select {
		case <-ctx.Done():
			sm.logger.Debug("message worker context cancelled, shutting down",
				"subject", sub.Subject,
				"workerID", workerID)
			return
		default:
		}

		// Block until message available (key difference from channel-based approach)
		// JetStream handles all buffering, pre-fetching, and optimization internally
		msg, err := sub.iterator.Next()

		if err != nil {
			// Check for normal shutdown conditions first
			if ctx.Err() != nil {
				sm.logger.Debug("message worker detected context cancellation",
					"subject", sub.Subject,
					"workerID", workerID)
				return
			}

			// Check for iterator closed/stopped (normal shutdown)
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				sm.logger.Info("message iterator closed",
					"subject", sub.Subject,
					"workerID", workerID)
				return
			}

			// Log other errors but continue - JetStream will reconnect automatically
			sm.logger.Error("failed to get next message from iterator",
				"subject", sub.Subject,
				"workerID", workerID,
				"error", err,
				"errorType", fmt.Sprintf("%T", err))

			// Brief sleep on persistent errors to avoid tight loop
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Process message with full error handling and panic recovery
		sm.processMessageWithRecovery(ctx, msg, sub.Subject, workerID)
	}
}

// processMessageWithRecovery wraps message processing with panic recovery and error handling.
// This ensures a single malformed message cannot crash the entire worker.
func (sm *SubscriptionManager) processMessageWithRecovery(ctx context.Context, msg jetstream.Msg, subject string, workerID int) {
	// Defer panic recovery to catch any panics during message processing
	defer func() {
		if r := recover(); r != nil {
			sm.logger.Error("panic recovered in message worker",
				"panic", r,
				"subject", subject,
				"workerID", workerID,
				"stack", string(debug.Stack()))

			// Terminate poison message to prevent redelivery loop
			if termErr := msg.Term(); termErr != nil {
				sm.logger.Error("failed to terminate message after panic",
					"subject", subject,
					"error", termErr)
			}

			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("error")
			}
		}
	}()

	// Process the message through the rule engine
	if err := sm.processMessage(ctx, msg); err != nil {
		sm.logger.Error("failed to process message",
			"subject", subject,
			"workerID", workerID,
			"error", err)

		// Differentiate between terminal and transient errors
		if isTerminalError(err) {
			// Terminal error: malformed message that will never succeed
			sm.logger.Warn("terminating malformed message to prevent redelivery loop",
				"subject", subject)
			if termErr := msg.Term(); termErr != nil {
				sm.logger.Error("failed to terminate message",
					"subject", subject,
					"error", termErr)
			}
		} else {
			// Transient error: might succeed on retry
			if nakErr := msg.Nak(); nakErr != nil {
				sm.logger.Error("failed to NAK message",
					"subject", subject,
					"error", nakErr)
			}
		}

		if sm.metrics != nil {
			sm.metrics.IncMessagesTotal("error")
		}
	} else {
		// Message processed successfully, acknowledge it
		if ackErr := msg.Ack(); ackErr != nil {
			sm.logger.Error("failed to ACK message",
				"subject", subject,
				"error", ackErr)
		}
		if sm.metrics != nil {
			sm.metrics.IncMessagesTotal("processed")
		}
	}
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
		// This error will be caught by the caller (processMessageWithRecovery) and handled appropriately.
		return fmt.Errorf("rule processing failed: %w", err)
	}

	// Publish all matched actions
	for _, action := range actions {
		// Check for NATS action 
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
			// HTTP actions will be handled by http-gateway
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

	// Step 1: Stop all iterators gracefully (drains pending messages)
	for _, sub := range sm.subscriptions {
		if sub.iterator != nil {
			sm.logger.Debug("stopping Messages() iterator", "subject", sub.Subject)
			sub.iterator.Stop()
			sm.logger.Debug("Messages() iterator stopped", "subject", sub.Subject)
		}
	}

	// Step 2: Cancel contexts to unblock workers immediately
	for _, sub := range sm.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

	// Step 3: Wait for all workers to finish
	sm.logger.Debug("waiting for all workers to finish")
	sm.wg.Wait()

	sm.logger.Info("all subscriptions stopped successfully")
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

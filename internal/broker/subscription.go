// file: internal/broker/subscription.go

package broker

import (
	"context"
	"errors"
	"fmt"
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
func (sm *SubscriptionManager) AddSubscription(
	streamName string,
	consumerName string,
	subject string,
	workers int,
) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.logger.Info("creating consumer handle",
		"subject", subject,
		"stream", streamName,
		"consumer", consumerName,
		"workers", workers)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	consumer, err := sm.jetStream.Consumer(ctx, streamName, consumerName)
	if err != nil {
		return fmt.Errorf("failed to get consumer handle for %s: %w", subject, err)
	}

	subscription := &Subscription{
		Subject:      subject,
		ConsumerName: consumerName,
		StreamName:   streamName,
		Consumer:     consumer,
		Workers:      workers,
		logger:       sm.logger,
		consumerCfg:  sm.consumerCfg,
	}

	sm.subscriptions = append(sm.subscriptions, subscription)

	sm.logger.Info("consumer handle created",
		"subject", subject,
		"stream", streamName,
		"consumer", consumerName,
		"fetchBatchSize", sm.consumerCfg.FetchBatchSize,
		"fetchTimeout", sm.consumerCfg.FetchTimeout)

	return nil
}

// Start begins processing messages on all subscriptions using the Shared Fetch Channel pattern.
func (sm *SubscriptionManager) Start(ctx context.Context) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.subscriptions) == 0 {
		return fmt.Errorf("no subscriptions configured")
	}

	sm.logger.Info("starting all subscriptions", "count", len(sm.subscriptions))

	for _, sub := range sm.subscriptions {
		subCtx, cancel := context.WithCancel(ctx)
		sub.cancel = cancel

		// Create a buffered channel to decouple fetching from processing.
		// The buffer size acts as an in-memory queue, allowing the fetcher to pull
		// new messages while workers are busy.
		msgChan := make(chan jetstream.Msg, sub.consumerCfg.FetchBatchSize)

		// Start the pool of processing workers. They will block until the fetcher provides messages.
		for i := 0; i < sub.Workers; i++ {
			sm.wg.Add(1)
			go sm.processingWorker(subCtx, msgChan, i, sub.Subject)
		}

		// Start a SINGLE fetcher goroutine for this subscription.
		// This is the only goroutine that communicates with NATS for this consumer.
		sm.wg.Add(1)
		go sm.fetcher(subCtx, sub, msgChan)

		sm.logger.Info("started fetcher and workers for subscription",
			"subject", sub.Subject,
			"workers", sub.Workers)
	}

	return nil
}

// fetcher is a dedicated goroutine that continuously pulls batches of messages from NATS
// and pushes them onto a channel for the processing workers.
func (sm *SubscriptionManager) fetcher(ctx context.Context, sub *Subscription, msgChan chan<- jetstream.Msg) {
	defer sm.wg.Done()
	// Closing the channel is the signal for the processing workers to shut down.
	defer close(msgChan)

	sub.logger.Debug("fetcher started", "subject", sub.Subject, "batchSize", sub.consumerCfg.FetchBatchSize)

	for {
		// Check for shutdown signal before fetching.
		select {
		case <-ctx.Done():
			sub.logger.Debug("fetcher shutting down", "subject", sub.Subject)
			return
		default:
			// Proceed with fetch.
		}

		// Fetch a batch of messages. This is a blocking call.
		msgs, err := sub.Consumer.Fetch(sub.consumerCfg.FetchBatchSize, jetstream.FetchMaxWait(sub.consumerCfg.FetchTimeout))
		if err != nil {
			// A timeout is expected and normal when the stream is idle.
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			// For other errors, log and retry after a short delay.
			sub.logger.Error("failed to fetch messages", "subject", sub.Subject, "error", err)
			time.Sleep(1 * time.Second) // Prevent fast error loops
			continue
		}

		// FIX: Correctly iterate over the message channel from the batch object.
		for msg := range msgs.Messages() {
			select {
			case msgChan <- msg:
				// Message successfully queued for a worker.
			case <-ctx.Done():
				// Application is shutting down, stop trying to queue messages.
				sub.logger.Debug("fetcher shutting down during message queuing", "subject", sub.Subject)
				return
			}
		}

		// NEW: Handle potential errors from the batch iterator itself.
		if msgs.Error() != nil {
			sub.logger.Error("error from message batch iterator", "subject", sub.Subject, "error", msgs.Error())
			time.Sleep(1 * time.Second)
		}
	}
}

// processingWorker consumes messages from the shared channel and executes the rule processor.
func (sm *SubscriptionManager) processingWorker(ctx context.Context, msgChan <-chan jetstream.Msg, workerID int, subject string) {
	defer sm.wg.Done()

	sm.logger.Debug("processing worker started", "subject", subject, "workerID", workerID)

	// This loop will automatically terminate when the fetcher closes the msgChan.
	for msg := range msgChan {
		if err := sm.processMessage(ctx, msg); err != nil {
			sm.logger.Error("failed to process message", "subject", msg.Subject(), "error", err)
			// Attempt to NAK the message for redelivery.
			if nakErr := msg.Nak(); nakErr != nil {
				sm.logger.Error("failed to NAK message", "subject", msg.Subject(), "error", nakErr)
			}
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("error")
			}
		} else {
			// Acknowledge successful processing.
			if ackErr := msg.Ack(); ackErr != nil {
				sm.logger.Error("failed to ACK message", "subject", msg.Subject(), "error", ackErr)
			}
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("processed")
			}
		}
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

	headers := make(map[string]string)
	if msg.Headers() != nil {
		for key, values := range msg.Headers() {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	actions, err := sm.processor.ProcessWithSubject(msg.Subject(), msg.Data(), headers)
	if err != nil {
		return fmt.Errorf("rule processing failed: %w", err)
	}

	for _, action := range actions {
		if err := sm.publishActionWithRetry(ctx, action); err != nil {
			sm.logger.Error("failed to publish action after retries", "actionSubject", action.Subject, "error", err)
			if sm.metrics != nil {
				sm.metrics.IncActionsTotal("error")
			}
			return fmt.Errorf("failed to publish action: %w", err)
		}
		if sm.metrics != nil {
			sm.metrics.IncActionsTotal("success")
			sm.metrics.IncRuleMatches()
		}
	}

	duration := time.Since(start)
	sm.logger.Debug("message processed", "subject", msg.Subject(), "duration", duration, "actionsPublished", len(actions))
	return nil
}

// publishActionWithRetry publishes an action with exponential backoff retry.
// Supports both JetStream and Core NATS publish modes based on configuration.
func (sm *SubscriptionManager) publishActionWithRetry(ctx context.Context, action *rule.Action) error {
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

		delay := baseDelay * time.Duration(1<<attempt)
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("failed to publish after %d attempts (mode: %s): %w", maxRetries, publishMode, lastErr)
}

// publishJetStream publishes to JetStream with ack confirmation.
func (sm *SubscriptionManager) publishJetStream(ctx context.Context, action *rule.Action) error {
	pubCtx, cancel := context.WithTimeout(ctx, sm.publishCfg.AckTimeout)
	defer cancel()

	var payloadBytes []byte
	if action.Passthrough {
		payloadBytes = action.RawPayload
	} else {
		payloadBytes = []byte(action.Payload)
	}

	_, err := sm.jetStream.Publish(pubCtx, action.Subject, payloadBytes)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return fmt.Errorf("jetstream publish failed: no stream is configured for action subject '%s'", action.Subject)
		}
		return fmt.Errorf("jetstream publish failed: %w", err)
	}

	return nil
}

// publishCore publishes to core NATS (fire-and-forget, no ack).
func (sm *SubscriptionManager) publishCore(ctx context.Context, action *rule.Action) error {
	var payloadBytes []byte
	if action.Passthrough {
		payloadBytes = action.RawPayload
	} else {
		payloadBytes = []byte(action.Payload)
	}

	if err := sm.natsConn.Publish(action.Subject, payloadBytes); err != nil {
		return fmt.Errorf("core nats publish failed: %w", err)
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

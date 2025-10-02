//file: internal/broker/subscription.go

package broker

import (
	"context"
	"fmt"
	"sync"
	"time"

	watermillNats "github.com/nats-io/nats.go"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// SubscriptionManager manages direct NATS pull subscriptions for rule subjects
// with parallel batch processing and synchronous action publishing
type SubscriptionManager struct {
	jsCtx         watermillNats.JetStreamContext
	logger        *logger.Logger
	metrics       *metrics.Metrics
	processor     *rule.Processor
	config        *config.ConsumerConfig
	subscriptions []*Subscription
	wg            sync.WaitGroup
	mu            sync.RWMutex
}

// Subscription represents a single NATS pull subscription with parallel processing
type Subscription struct {
	Subject       string
	ConsumerName  string
	StreamName    string
	Sub           *watermillNats.Subscription
	Workers       int // Number of concurrent workers
	cancel        context.CancelFunc
	logger        *logger.Logger
	config        *config.ConsumerConfig
}

// NewSubscriptionManager creates a new subscription manager
func NewSubscriptionManager(
	jsCtx watermillNats.JetStreamContext,
	processor *rule.Processor,
	logger *logger.Logger,
	metrics *metrics.Metrics,
	consumerConfig *config.ConsumerConfig,
) *SubscriptionManager {
	return &SubscriptionManager{
		jsCtx:         jsCtx,
		logger:        logger,
		metrics:       metrics,
		processor:     processor,
		config:        consumerConfig,
		subscriptions: make([]*Subscription, 0),
	}
}

// AddSubscription creates a pull subscription for a subject
func (sm *SubscriptionManager) AddSubscription(
	streamName string,
	consumerName string,
	subject string,
	workers int,
) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.logger.Info("creating pull subscription",
		"subject", subject,
		"stream", streamName,
		"consumer", consumerName,
		"workers", workers)

	// Create pull subscription bound to the consumer
	sub, err := sm.jsCtx.PullSubscribe(
		subject,
		consumerName,
		watermillNats.Bind(streamName, consumerName),
		watermillNats.ManualAck(),
	)
	if err != nil {
		return fmt.Errorf("failed to create pull subscription for %s: %w", subject, err)
	}

	subscription := &Subscription{
		Subject:      subject,
		ConsumerName: consumerName,
		StreamName:   streamName,
		Sub:          sub,
		Workers:      workers,
		logger:       sm.logger,
		config:       sm.config,
	}

	sm.subscriptions = append(sm.subscriptions, subscription)

	sm.logger.Info("pull subscription created",
		"subject", subject,
		"stream", streamName,
		"consumer", consumerName,
		"fetchBatchSize", sm.config.FetchBatchSize,
		"fetchTimeout", sm.config.FetchTimeout)

	return nil
}

// Start begins processing messages on all subscriptions
func (sm *SubscriptionManager) Start(ctx context.Context) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.subscriptions) == 0 {
		return fmt.Errorf("no subscriptions configured")
	}

	sm.logger.Info("starting all subscriptions", "count", len(sm.subscriptions))

	for _, sub := range sm.subscriptions {
		// Create cancellable context for this subscription
		subCtx, cancel := context.WithCancel(ctx)
		sub.cancel = cancel

		// Start worker goroutines for this subscription
		for i := 0; i < sub.Workers; i++ {
			sm.wg.Add(1)
			go sm.worker(subCtx, sub, i)
		}

		sm.logger.Info("started workers for subscription",
			"subject", sub.Subject,
			"workers", sub.Workers)
	}

	return nil
}

// worker processes messages from a pull subscription with parallel batch processing
func (sm *SubscriptionManager) worker(ctx context.Context, sub *Subscription, workerID int) {
	defer sm.wg.Done()

	sub.logger.Debug("worker started",
		"subject", sub.Subject,
		"workerID", workerID,
		"fetchBatchSize", sub.config.FetchBatchSize)

	for {
		select {
		case <-ctx.Done():
			sub.logger.Debug("worker stopping",
				"subject", sub.Subject,
				"workerID", workerID)
			return
		default:
			// Fetch messages from JetStream using configured batch size
			msgs, err := sub.Sub.Fetch(
				sub.config.FetchBatchSize,
				watermillNats.MaxWait(sub.config.FetchTimeout),
			)
			if err != nil {
				// Timeout is normal when no messages available
				if err == watermillNats.ErrTimeout {
					continue
				}
				
				// Log other errors but continue processing
				sub.logger.Debug("fetch error",
					"subject", sub.Subject,
					"workerID", workerID,
					"error", err)
				continue
			}

			// Process batch in parallel
			sm.processBatchParallel(ctx, sub, msgs, workerID)
		}
	}
}

// processBatchParallel processes a batch of messages concurrently
// Each goroutine handles: rule evaluation + action publishing + message ACK
func (sm *SubscriptionManager) processBatchParallel(ctx context.Context, sub *Subscription, msgs []*watermillNats.Msg, workerID int) {
	if len(msgs) == 0 {
		return
	}

	batchStart := time.Now()
	
	sub.logger.Debug("processing batch in parallel",
		"subject", sub.Subject,
		"workerID", workerID,
		"batchSize", len(msgs))

	// Calculate parallelism: min(batchSize, fetchBatchSize)
	// This prevents oversubscription with small batches
	parallelism := len(msgs)
	if parallelism > sub.config.FetchBatchSize {
		parallelism = sub.config.FetchBatchSize
	}

	// Create semaphore to limit concurrent goroutines
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup

	// Process each message in the batch concurrently
	for _, msg := range msgs {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore slot

		go func(m *watermillNats.Msg) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore slot

			// Process single message (includes action publishing)
			if err := sm.processMessage(ctx, sub, m); err != nil {
				sub.logger.Error("failed to process message",
					"subject", sub.Subject,
					"workerID", workerID,
					"error", err)
				
				// Negative ack on processing failure
				m.Nak()
				
				if sm.metrics != nil {
					sm.metrics.IncMessagesTotal("error")
				}
			} else {
				// Ack successful processing (after actions published)
				m.Ack()
				
				if sm.metrics != nil {
					sm.metrics.IncMessagesTotal("processed")
				}
			}
		}(msg)
	}

	// Wait for all messages in batch to complete
	wg.Wait()

	batchDuration := time.Since(batchStart)
	throughput := float64(len(msgs)) / batchDuration.Seconds()
	
	sub.logger.Debug("batch processing complete",
		"subject", sub.Subject,
		"workerID", workerID,
		"batchSize", len(msgs),
		"duration", batchDuration,
		"throughput", fmt.Sprintf("%.0f msg/sec", throughput))
}

// processMessage handles a single message through the rule engine
// and publishes actions synchronously before returning
func (sm *SubscriptionManager) processMessage(ctx context.Context, sub *Subscription, msg *watermillNats.Msg) error {
	start := time.Now()

	// Update metrics
	if sm.metrics != nil {
		sm.metrics.IncMessagesTotal("received")
	}

	sub.logger.Debug("processing message",
		"subject", msg.Subject,
		"size", len(msg.Data))

	// Process through rule engine
	actions, err := sm.processor.ProcessWithSubject(msg.Subject, msg.Data)
	if err != nil {
		return fmt.Errorf("rule processing failed: %w", err)
	}

	// Publish all actions synchronously (with retry)
	for _, action := range actions {
		if err := sm.publishActionWithRetry(ctx, action); err != nil {
			sub.logger.Error("failed to publish action after retries",
				"actionSubject", action.Subject,
				"sourceSubject", msg.Subject,
				"error", err)
			
			if sm.metrics != nil {
				sm.metrics.IncActionsTotal("error")
			}
			
			// Continue processing other actions, but mark message as failed
			// This ensures we don't ACK the message if any action fails
			return fmt.Errorf("failed to publish action: %w", err)
		}

		if sm.metrics != nil {
			sm.metrics.IncActionsTotal("success")
			sm.metrics.IncRuleMatches()
		}
		
		sub.logger.Debug("action published",
			"actionSubject", action.Subject,
			"sourceSubject", msg.Subject)
	}

	duration := time.Since(start)
	sub.logger.Debug("message processed",
		"subject", msg.Subject,
		"duration", duration,
		"actionsPublished", len(actions))

	return nil
}

// publishActionWithRetry publishes an action with exponential backoff retry
func (sm *SubscriptionManager) publishActionWithRetry(ctx context.Context, action *rule.Action) error {
	maxRetries := 3
	baseDelay := 50 * time.Millisecond
	
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Check context cancellation
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}
		
		// Attempt to publish
		_, err := sm.jsCtx.Publish(action.Subject, []byte(action.Payload))
		if err == nil {
			// Success
			if attempt > 0 {
				sm.logger.Info("action published after retry",
					"subject", action.Subject,
					"attempts", attempt+1)
			} else {
				sm.logger.Debug("action published",
					"subject", action.Subject,
					"payloadSize", len(action.Payload))
			}
			return nil
		}
		
		lastErr = err
		
		// Log the failure
		sm.logger.Warn("action publish failed, will retry",
			"attempt", attempt+1,
			"maxRetries", maxRetries,
			"subject", action.Subject,
			"error", err)
		
		// Update failure metrics
		if sm.metrics != nil {
			sm.metrics.IncActionPublishFailures()
		}
		
		// Last attempt - don't sleep
		if attempt == maxRetries-1 {
			sm.logger.Error("action publish failed after all retries",
				"subject", action.Subject,
				"attempts", maxRetries,
				"error", lastErr)
			break
		}
		
		// Exponential backoff: 50ms, 100ms, 200ms
		delay := baseDelay * time.Duration(1<<attempt)
		
		sm.logger.Debug("backing off before retry",
			"attempt", attempt+1,
			"delay", delay)
		
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
		case <-time.After(delay):
			// Continue to next retry
		}
	}
	
	// All retries exhausted
	return fmt.Errorf("failed to publish after %d attempts: %w", maxRetries, lastErr)
}

// Stop gracefully shuts down all subscriptions
func (sm *SubscriptionManager) Stop() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.logger.Info("stopping all subscriptions", "count", len(sm.subscriptions))

	// Cancel all subscription contexts (stops workers from fetching new messages)
	for _, sub := range sm.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

	// Wait for all workers to finish processing in-flight messages
	// This ensures all messages are fully processed (including action publishing) before shutdown
	sm.logger.Info("waiting for workers to complete in-flight messages")
	sm.wg.Wait()

	// Unsubscribe from NATS
	for _, sub := range sm.subscriptions {
		if err := sub.Sub.Unsubscribe(); err != nil {
			sm.logger.Error("failed to unsubscribe",
				"subject", sub.Subject,
				"error", err)
		}
	}

	sm.logger.Info("all subscriptions stopped")
	return nil
}

// GetSubscriptionCount returns the number of active subscriptions
func (sm *SubscriptionManager) GetSubscriptionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.subscriptions)
}

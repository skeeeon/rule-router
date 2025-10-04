// file: internal/broker/subscription.go

package broker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// SubscriptionManager manages JetStream pull subscriptions for rule subjects
// with parallel message processing and synchronous action publishing
type SubscriptionManager struct {
	jetStream     jetstream.JetStream
	logger        *logger.Logger
	metrics       *metrics.Metrics
	processor     *rule.Processor
	config        *config.ConsumerConfig
	subscriptions []*Subscription
	wg            sync.WaitGroup
	mu            sync.RWMutex
}

// Subscription represents a single JetStream pull consumer with parallel processing
type Subscription struct {
	Subject       string
	ConsumerName  string
	StreamName    string
	Consumer      jetstream.Consumer      // NEW: Consumer interface
	Workers       int                      // Number of concurrent workers
	cancel        context.CancelFunc
	logger        *logger.Logger
	config        *config.ConsumerConfig
}

// NewSubscriptionManager creates a new subscription manager
func NewSubscriptionManager(
	js jetstream.JetStream,
	processor *rule.Processor,
	logger *logger.Logger,
	metrics *metrics.Metrics,
	consumerConfig *config.ConsumerConfig,
) *SubscriptionManager {
	return &SubscriptionManager{
		jetStream:     js,
		logger:        logger,
		metrics:       metrics,
		processor:     processor,
		config:        consumerConfig,
		subscriptions: make([]*Subscription, 0),
	}
}

// AddSubscription creates a consumer handle for a subject
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

	// Get consumer handle using the new API
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
		config:       sm.config,
	}

	sm.subscriptions = append(sm.subscriptions, subscription)

	sm.logger.Info("consumer handle created",
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

// worker processes messages from a pull consumer using Messages() iterator
func (sm *SubscriptionManager) worker(ctx context.Context, sub *Subscription, workerID int) {
	defer sm.wg.Done()

	sub.logger.Debug("worker started",
		"subject", sub.Subject,
		"workerID", workerID,
		"fetchBatchSize", sub.config.FetchBatchSize)

	// Create Messages() iterator for continuous message retrieval
	// This provides optimized pull consumer behavior with pre-buffering
	// 
	// Heartbeat configuration:
	// - NATS minimum: 500ms
	// - JetStream rule: heartbeat must be < 50% of expiry
	// - For timeouts <= 1s, these constraints conflict, so we omit heartbeat
	//   (the timeout itself provides liveness detection)
	var messagesOpts []jetstream.PullMessagesOpt
	messagesOpts = append(messagesOpts,
		jetstream.PullMaxMessages(sub.config.FetchBatchSize),
		jetstream.PullExpiry(sub.config.FetchTimeout),
	)
	
	// Only add heartbeat if fetchTimeout is long enough to satisfy both constraints
	// Minimum fetchTimeout for heartbeat: 500ms / 0.5 = 1s, but must be > 1s to have heartbeat < 50%
	// So we require at least 1.5s to safely use heartbeat
	if sub.config.FetchTimeout > 1500*time.Millisecond {
		// Calculate heartbeat as 40% of timeout (safely under 50% requirement)
		heartbeat := time.Duration(float64(sub.config.FetchTimeout) * 0.4)
		
		// Ensure NATS minimum of 500ms
		if heartbeat < 500*time.Millisecond {
			heartbeat = 500 * time.Millisecond
		}
		
		// Final safety check: ensure still under 50% of timeout
		maxHeartbeat := sub.config.FetchTimeout / 2
		if heartbeat >= maxHeartbeat {
			// This shouldn't happen with our 40% calculation, but be defensive
			heartbeat = time.Duration(float64(maxHeartbeat) * 0.95) // 95% of the 50% limit
		}
		
		messagesOpts = append(messagesOpts, jetstream.PullHeartbeat(heartbeat))
		
		sub.logger.Debug("configured heartbeat for message iterator",
			"subject", sub.Subject,
			"workerID", workerID,
			"heartbeat", heartbeat,
			"fetchTimeout", sub.config.FetchTimeout)
	} else {
		sub.logger.Debug("omitting heartbeat due to short fetch timeout",
			"subject", sub.Subject,
			"workerID", workerID,
			"fetchTimeout", sub.config.FetchTimeout,
			"reason", "timeout too short to satisfy NATS minimum (500ms) and JetStream rule (< 50% of expiry)")
	}
	
	iter, err := sub.Consumer.Messages(messagesOpts...)
	if err != nil {
		sub.logger.Error("failed to create message iterator",
			"subject", sub.Subject,
			"workerID", workerID,
			"error", err)
		return
	}
	defer iter.Stop()

	// Start a goroutine to stop the iterator when context is cancelled
	// This unblocks any pending iter.Next() calls
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		// Fetch next message - this will unblock when iter.Stop() is called
		msg, err := iter.Next()
		if err != nil {
			// Check for context cancellation
			if ctx.Err() != nil {
				sub.logger.Debug("worker stopping due to context cancellation",
					"subject", sub.Subject,
					"workerID", workerID)
				return
			}
			
			// Log other errors but continue processing
			sub.logger.Debug("iterator error",
				"subject", sub.Subject,
				"workerID", workerID,
				"error", err)
			
			// Small backoff on error
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Process message (includes rule evaluation + action publishing)
		if err := sm.processMessage(ctx, sub, msg); err != nil {
			sub.logger.Error("failed to process message",
				"subject", sub.Subject,
				"workerID", workerID,
				"error", err)
			
			// Negative ack on processing failure
			msg.Nak()
			
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("error")
			}
		} else {
			// Ack successful processing (after actions published)
			msg.Ack()
			
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("processed")
			}
		}
	}
}

// processMessage handles a single message through the rule engine
// and publishes actions synchronously before returning
func (sm *SubscriptionManager) processMessage(ctx context.Context, sub *Subscription, msg jetstream.Msg) error {
	start := time.Now()

	// Update metrics
	if sm.metrics != nil {
		sm.metrics.IncMessagesTotal("received")
	}

	sub.logger.Debug("processing message",
		"subject", msg.Subject(),
		"size", len(msg.Data()))

	// Process through rule engine
	actions, err := sm.processor.ProcessWithSubject(msg.Subject(), msg.Data())
	if err != nil {
		return fmt.Errorf("rule processing failed: %w", err)
	}

	// Publish all actions synchronously (with retry)
	for _, action := range actions {
		if err := sm.publishActionWithRetry(ctx, action); err != nil {
			sub.logger.Error("failed to publish action after retries",
				"actionSubject", action.Subject,
				"sourceSubject", msg.Subject(),
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
			"sourceSubject", msg.Subject())
	}

	duration := time.Since(start)
	sub.logger.Debug("message processed",
		"subject", msg.Subject(),
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
		
		// Attempt to publish using the new JetStream API
		pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := sm.jetStream.Publish(pubCtx, action.Subject, []byte(action.Payload))
		cancel()
		
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

	sm.logger.Info("all subscriptions stopped")
	return nil
}

// GetSubscriptionCount returns the number of active subscriptions
func (sm *SubscriptionManager) GetSubscriptionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.subscriptions)
}

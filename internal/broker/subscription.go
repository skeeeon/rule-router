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

// ... (NewSubscriptionManager, AddSubscription, Start, and other methods remain unchanged) ...
// SubscriptionManager manages JetStream pull subscriptions for rule subjects
// with parallel message processing and configurable action publishing (JetStream or Core)
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

// Subscription represents a single JetStream pull consumer with parallel processing
type Subscription struct {
	Subject       string
	ConsumerName  string
	StreamName    string
	Consumer      jetstream.Consumer
	Workers       int // Number of concurrent workers
	cancel        context.CancelFunc
	logger        *logger.Logger
	consumerCfg   *config.ConsumerConfig
}

// NewSubscriptionManager creates a new subscription manager
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
		"fetchBatchSize", sub.consumerCfg.FetchBatchSize)

	var messagesOpts []jetstream.PullMessagesOpt
	messagesOpts = append(messagesOpts,
		jetstream.PullMaxMessages(sub.consumerCfg.FetchBatchSize),
		jetstream.PullExpiry(sub.consumerCfg.FetchTimeout),
	)

	if sub.consumerCfg.FetchTimeout > 1500*time.Millisecond {
		heartbeat := time.Duration(float64(sub.consumerCfg.FetchTimeout) * 0.4)
		if heartbeat < 500*time.Millisecond {
			heartbeat = 500 * time.Millisecond
		}
		messagesOpts = append(messagesOpts, jetstream.PullHeartbeat(heartbeat))
	}

	iter, err := sub.Consumer.Messages(messagesOpts...)
	if err != nil {
		sub.logger.Error("failed to create message iterator", "subject", sub.Subject, "error", err)
		return
	}
	defer iter.Stop()

	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if err := sm.processMessage(ctx, sub, msg); err != nil {
			sub.logger.Error("failed to process message", "subject", sub.Subject, "error", err)
			msg.Nak()
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("error")
			}
		} else {
			msg.Ack()
			if sm.metrics != nil {
				sm.metrics.IncMessagesTotal("processed")
			}
		}
	}
}


// processMessage handles a single message through the rule engine
func (sm *SubscriptionManager) processMessage(ctx context.Context, sub *Subscription, msg jetstream.Msg) error {
	start := time.Now()
	if sm.metrics != nil {
		sm.metrics.IncMessagesTotal("received")
	}

	sub.logger.Debug("processing message", "subject", msg.Subject(), "size", len(msg.Data()))

	// **MODIFICATION START**
	// Convert NATS headers to simple map[string]string for the rule engine.
	// We take the first value for any header key with multiple values.
	headers := make(map[string]string)
	if msg.Headers() != nil {
		for key, values := range msg.Headers() {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	// Pass headers to the processor.
	actions, err := sm.processor.ProcessWithSubject(msg.Subject(), msg.Data(), headers)
	// **MODIFICATION END**

	if err != nil {
		return fmt.Errorf("rule processing failed: %w", err)
	}

	for _, action := range actions {
		if err := sm.publishActionWithRetry(ctx, action); err != nil {
			sub.logger.Error("failed to publish action after retries", "actionSubject", action.Subject, "error", err)
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
	sub.logger.Debug("message processed", "subject", msg.Subject(), "duration", duration, "actionsPublished", len(actions))
	return nil
}

// ... (publishActionWithRetry and other methods remain unchanged) ...
// publishActionWithRetry publishes an action with exponential backoff retry
// Supports both JetStream and Core NATS publish modes based on configuration
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

// publishJetStream publishes to JetStream with ack confirmation
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

// publishCore publishes to core NATS (fire-and-forget, no ack)
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

// Stop gracefully shuts down all subscriptions
func (sm *SubscriptionManager) Stop() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.logger.Info("stopping all subscriptions", "count", len(sm.subscriptions))

	for _, sub := range sm.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

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

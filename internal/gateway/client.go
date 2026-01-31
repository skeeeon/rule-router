// file: internal/gateway/client.go

package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Client limits and timeout constants
const (
	// MaxOutboundResponseSize is the maximum size of an HTTP response body to read for logging (1MB).
	MaxOutboundResponseSize = 1024 * 1024

	// clientOperationTimeout is the maximum time for JetStream operations like getting streams/consumers
	clientOperationTimeout = 30 * time.Second

	// clientMinHeartbeatInterval is the minimum heartbeat duration for consumer health checks
	clientMinHeartbeatInterval = 1 * time.Second

	// clientErrorBackoffDelay is the delay after encountering errors to avoid tight retry loops
	clientErrorBackoffDelay = 100 * time.Millisecond

	// httpRetryInitialDelay is the initial delay for HTTP request retries
	httpRetryInitialDelay = 1 * time.Second

	// httpRetryMaxDelay is the maximum delay for HTTP request retries
	httpRetryMaxDelay = 30 * time.Second

	// httpRetryMaxJitter is the maximum jitter added to HTTP retry delays
	httpRetryMaxJitter = 100 * time.Millisecond
)

// OutboundClient handles NATS messages and makes HTTP requests
// ACK-on-Success: ACKs message only on HTTP 200-299, NAKs on failure
type OutboundClient struct {
	logger        *logger.Logger
	metrics       *metrics.Metrics
	processor     *rule.Processor
	jetstream     jetstream.JetStream
	httpClient    *http.Client
	consumerCfg   *ConsumerConfig
	subscriptions []*OutboundSubscription
	wg            sync.WaitGroup
	mu            sync.RWMutex
}

// OutboundSubscription represents a NATS subscription for outbound HTTP
type OutboundSubscription struct {
	Subject      string
	ConsumerName string
	StreamName   string
	Consumer     jetstream.Consumer
	Workers      int
	iterator     jetstream.MessagesContext // Messages() iterator
	cancel       context.CancelFunc
	logger       *logger.Logger
}

// ConsumerConfig contains JetStream consumer configuration
type ConsumerConfig struct {
	WorkerCount int
	FetchBatchSize  int
	FetchTimeout    time.Duration
	MaxAckPending   int
	AckWaitTimeout  time.Duration
	MaxDeliver      int
}

// NewOutboundClient creates a new HTTP outbound client
func NewOutboundClient(
	logger *logger.Logger,
	metrics *metrics.Metrics,
	processor *rule.Processor,
	js jetstream.JetStream,
	consumerCfg *ConsumerConfig,
	httpClientCfg *config.HTTPClientConfig,
) *OutboundClient {
	return &OutboundClient{
		logger:        logger,
		metrics:       metrics,
		processor:     processor,
		jetstream:     js,
		consumerCfg:   consumerCfg,
		subscriptions: make([]*OutboundSubscription, 0),
		httpClient: &http.Client{
			Timeout: httpClientCfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        httpClientCfg.MaxIdleConns,
				MaxIdleConnsPerHost: httpClientCfg.MaxIdleConnsPerHost,
				IdleConnTimeout:     httpClientCfg.IdleConnTimeout,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: httpClientCfg.TLS.InsecureSkipVerify,
				},
			},
		},
	}
}

// GetSubscriptions returns a copy of all subscriptions
func (c *OutboundClient) GetSubscriptions() []*OutboundSubscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subs := make([]*OutboundSubscription, len(c.subscriptions))
	copy(subs, c.subscriptions)
	return subs
}

// AddSubscription adds a NATS subscription for outbound HTTP.
// It accepts a context for cancellation and timeout control.
func (c *OutboundClient) AddSubscription(ctx context.Context, streamName, consumerName, subject string, workers int) error {
	// Use a timeout context for JetStream operations to prevent indefinite blocking
	opCtx, cancel := context.WithTimeout(ctx, clientOperationTimeout)
	defer cancel()

	// Perform network calls OUTSIDE the lock to avoid blocking other goroutines
	stream, err := c.jetstream.Stream(opCtx, streamName)
	if err != nil {
		return fmt.Errorf("failed to get stream '%s': %w", streamName, err)
	}

	consumer, err := stream.Consumer(opCtx, consumerName)
	if err != nil {
		return fmt.Errorf("failed to get consumer '%s': %w", consumerName, err)
	}

	sub := &OutboundSubscription{
		Subject:      subject,
		ConsumerName: consumerName,
		StreamName:   streamName,
		Consumer:     consumer,
		Workers:      workers,
		logger:       c.logger,
	}

	// Only hold the lock for the actual slice append
	c.mu.Lock()
	c.subscriptions = append(c.subscriptions, sub)
	c.mu.Unlock()

	c.logger.Info("outbound subscription added",
		"stream", streamName,
		"consumer", consumerName,
		"subject", subject,
		"workers", workers)

	return nil
}

// Start begins consuming messages and making HTTP requests
func (c *OutboundClient) Start(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.subscriptions) == 0 {
		c.logger.Info("no outbound subscriptions configured")
		return nil
	}

	c.logger.Info("starting outbound client with Messages() iterator",
		"subscriptions", len(c.subscriptions),
		"fetchBatchSize", c.consumerCfg.FetchBatchSize,
		"fetchTimeout", c.consumerCfg.FetchTimeout)

	for _, sub := range c.subscriptions {
		if err := c.startSubscription(ctx, sub); err != nil {
			return fmt.Errorf("failed to start subscription for '%s': %w", sub.Subject, err)
		}
	}

	c.logger.Info("all outbound subscriptions started successfully")
	return nil
}

// startSubscription initializes a Messages() iterator and worker pool for a single subscription
func (c *OutboundClient) startSubscription(ctx context.Context, sub *OutboundSubscription) error {
	subCtx, cancel := context.WithCancel(ctx)
	sub.cancel = cancel

	// Calculate heartbeat duration: half of fetch timeout, minimum 1 second
	heartbeatDuration := c.consumerCfg.FetchTimeout / 2
	if heartbeatDuration < clientMinHeartbeatInterval {
		heartbeatDuration = clientMinHeartbeatInterval
	}

	c.logger.Debug("creating Messages() iterator for outbound subscription",
		"subject", sub.Subject,
		"pullMaxMessages", c.consumerCfg.FetchBatchSize,
		"pullExpiry", c.consumerCfg.FetchTimeout,
		"heartbeat", heartbeatDuration)

	// Create Messages() iterator - event-driven, no polling
	iter, err := sub.Consumer.Messages(
		jetstream.PullMaxMessages(c.consumerCfg.FetchBatchSize),
		jetstream.PullExpiry(c.consumerCfg.FetchTimeout),
		jetstream.PullHeartbeat(heartbeatDuration),
	)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create Messages() iterator: %w", err)
	}

	// Store iterator for cleanup during shutdown
	sub.iterator = iter

	c.logger.Info("Messages() iterator created successfully for outbound subscription",
		"subject", sub.Subject,
		"stream", sub.StreamName,
		"consumer", sub.ConsumerName,
		"pullMaxMessages", c.consumerCfg.FetchBatchSize,
		"pullExpiry", c.consumerCfg.FetchTimeout,
		"heartbeat", heartbeatDuration)

	// Start worker pool - each worker calls iter.Next() in blocking loop
	for i := 0; i < sub.Workers; i++ {
		c.wg.Add(1)
		go c.messageWorker(subCtx, sub, i)
	}

	c.logger.Info("outbound subscription started with worker pool",
		"subject", sub.Subject,
		"workers", sub.Workers)

	return nil
}

// messageWorker continuously pulls messages from the iterator and processes them
// This replaces the old fetcher + processingWorker pattern with a simpler approach
func (c *OutboundClient) messageWorker(ctx context.Context, sub *OutboundSubscription, workerID int) {
	defer c.wg.Done()

	c.logger.Debug("outbound message worker started",
		"subject", sub.Subject,
		"workerID", workerID)

	for {
		// Check for context cancellation before blocking on Next()
		select {
		case <-ctx.Done():
			c.logger.Debug("outbound message worker context cancelled, shutting down",
				"subject", sub.Subject,
				"workerID", workerID)
			return
		default:
		}

		// Block until message available (NO MORE POLLING!)
		// JetStream handles all buffering, pre-fetching, and optimization internally
		msg, err := sub.iterator.Next()

		if err != nil {
			// Check for normal shutdown conditions first
			if ctx.Err() != nil {
				c.logger.Debug("outbound message worker detected context cancellation",
					"subject", sub.Subject,
					"workerID", workerID)
				return
			}

			// Check for iterator closed/stopped (normal shutdown)
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				c.logger.Info("outbound message iterator closed",
					"subject", sub.Subject,
					"workerID", workerID)
				return
			}

			// Log other errors but continue - JetStream will reconnect automatically
			c.logger.Error("failed to get next message from iterator",
				"subject", sub.Subject,
				"workerID", workerID,
				"error", err,
				"errorType", fmt.Sprintf("%T", err))

			// Brief sleep on persistent errors to avoid tight loop
			time.Sleep(clientErrorBackoffDelay)
			continue
		}

		// Process message with full error handling and panic recovery
		c.processMessageWithRecovery(ctx, msg, sub.Subject, workerID)
	}
}

// processMessageWithRecovery wraps message processing with panic recovery and error handling
// This ensures a single malformed message cannot crash the entire worker
func (c *OutboundClient) processMessageWithRecovery(ctx context.Context, msg jetstream.Msg, subject string, workerID int) {
	// Defer panic recovery to catch any panics during message processing
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("panic recovered in outbound message worker",
				"panic", r,
				"subject", subject,
				"workerID", workerID,
				"stack", string(debug.Stack()))

			// Terminate poison message to prevent redelivery loop
			if termErr := msg.Term(); termErr != nil {
				c.logger.Error("failed to terminate message after panic",
					"subject", subject,
					"error", termErr)
			}

			if c.metrics != nil {
				c.metrics.IncMessagesTotal("error")
			}
		}
	}()

	// Process the message
	if err := c.processMessage(ctx, msg); err != nil {
		c.logger.Error("failed to process outbound message",
			"subject", subject,
			"workerID", workerID,
			"error", err)

		// NAK on failure (will retry up to maxDeliver)
		if nakErr := msg.Nak(); nakErr != nil {
			c.logger.Error("failed to NAK message",
				"subject", subject,
				"error", nakErr)
		}

		if c.metrics != nil {
			c.metrics.IncMessagesTotal("error")
		}
	} else {
		// ACK on success
		if ackErr := msg.Ack(); ackErr != nil {
			c.logger.Error("failed to ACK message",
				"subject", subject,
				"error", ackErr)
		}

		if c.metrics != nil {
			c.metrics.IncMessagesTotal("processed")
		}
	}
}

// processMessage processes a NATS message and makes HTTP requests
func (c *OutboundClient) processMessage(ctx context.Context, msg jetstream.Msg) error {
	start := time.Now()
	if c.metrics != nil {
		c.metrics.IncMessagesTotal("received")
	}

	c.logger.Debug("processing outbound message",
		"subject", msg.Subject(),
		"size", len(msg.Data()))

	// Extract headers
	headers := make(map[string]string)
	if msg.Headers() != nil {
		for key, values := range msg.Headers() {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	// Process through rule engine
	actions, err := c.processor.ProcessNATS(msg.Subject(), msg.Data(), headers)
	if err != nil {
		return fmt.Errorf("rule processing failed: %w", err)
	}

	// Execute all HTTP actions
	for _, action := range actions {
		if action.HTTP != nil {
			if err := c.executeHTTPAction(ctx, action.HTTP); err != nil {
				c.logger.Error("failed to execute HTTP action",
					"subject", msg.Subject(),
					"url", action.HTTP.URL,
					"error", err)
				if c.metrics != nil {
					c.metrics.IncActionsTotal("error")
				}
				// Return error to NAK message
				return fmt.Errorf("HTTP action failed: %w", err)
			}

			if c.metrics != nil {
				c.metrics.IncActionsTotal("success")
				c.metrics.IncRuleMatches()
			}
		} else if action.NATS != nil {
			// NATS actions not typical in outbound, but log if present
			c.logger.Debug("NATS action in outbound rule - consider using rule-router instead",
				"subject", action.NATS.Subject)
		}
	}

	duration := time.Since(start)
	c.logger.Debug("outbound message processed",
		"subject", msg.Subject(),
		"duration", duration,
		"actionsExecuted", len(actions))

	return nil
}

// executeHTTPAction makes an HTTP request with retry logic
func (c *OutboundClient) executeHTTPAction(ctx context.Context, action *rule.HTTPAction) error {
	// Get retry config (use defaults if not specified)
	maxAttempts := 3
	initialDelay := httpRetryInitialDelay
	maxDelay := httpRetryMaxDelay

	if action.Retry != nil {
		if action.Retry.MaxAttempts > 0 {
			maxAttempts = action.Retry.MaxAttempts
		}
		if d, err := time.ParseDuration(action.Retry.InitialDelay); err == nil {
			initialDelay = d
		}
		if d, err := time.ParseDuration(action.Retry.MaxDelay); err == nil {
			maxDelay = d
		}
	}

	// Retry loop with exponential backoff
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := c.makeHTTPRequest(ctx, action)
		if err == nil {
			// Success!
			c.logger.Debug("HTTP request succeeded",
				"url", action.URL,
				"attempt", attempt)
			return nil
		}

		lastErr = err

		// Don't retry on last attempt
		if attempt == maxAttempts {
			break
		}

		// Exponential backoff with jitter
		delay := initialDelay * time.Duration(1<<(attempt-1))
		if delay > maxDelay {
			delay = maxDelay
		}
		jitter := time.Duration(rand.Intn(int(httpRetryMaxJitter.Milliseconds()))) * time.Millisecond
		delay += jitter

		c.logger.Warn("HTTP request failed, retrying",
			"url", action.URL,
			"attempt", attempt,
			"maxAttempts", maxAttempts,
			"nextRetryIn", delay,
			"error", err)

		// Wait before retry
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("HTTP request failed after %d attempts: %w", maxAttempts, lastErr)
}

// makeHTTPRequest makes a single HTTP request
func (c *OutboundClient) makeHTTPRequest(ctx context.Context, action *rule.HTTPAction) error {
	start := time.Now()

	// Prepare payload
	var body io.Reader
	if action.Passthrough {
		body = bytes.NewReader(action.RawPayload)
	} else {
		body = strings.NewReader(action.Payload)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, action.Method, action.URL, body)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Add headers
	if len(action.Headers) > 0 {
		for key, value := range action.Headers {
			req.Header.Set(key, value)
		}
	}

	// Set default Content-Type if not provided
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.IncHTTPOutboundRequestsTotal(action.URL, "error")
		}
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for logging (ignore errors - best effort)
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxOutboundResponseSize))

	// Record metrics
	duration := time.Since(start).Seconds()
	if c.metrics != nil {
		c.metrics.IncHTTPOutboundRequestsTotal(action.URL, fmt.Sprintf("%d", resp.StatusCode))
		c.metrics.ObserveHTTPOutboundDuration(action.URL, duration)
	}

	// Check status code (2xx = success)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.logger.Debug("HTTP request successful",
			"url", action.URL,
			"method", action.Method,
			"statusCode", resp.StatusCode,
			"duration", duration)
		return nil
	}

	// Non-2xx status is an error
	c.logger.Warn("HTTP request returned non-2xx status",
		"url", action.URL,
		"method", action.Method,
		"statusCode", resp.StatusCode,
		"responseBody", string(responseBody),
		"duration", duration)

	return fmt.Errorf("HTTP request returned status %d", resp.StatusCode)
}

// Stop gracefully shuts down all outbound subscriptions
func (c *OutboundClient) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("stopping outbound client", "subscriptions", len(c.subscriptions))

	// Step 1: Stop all iterators gracefully (drains pending messages)
	for _, sub := range c.subscriptions {
		if sub.iterator != nil {
			c.logger.Debug("stopping Messages() iterator", "subject", sub.Subject)
			sub.iterator.Stop()
			c.logger.Debug("Messages() iterator stopped", "subject", sub.Subject)
		}
	}

	// Step 2: Cancel contexts to unblock workers immediately
	for _, sub := range c.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

	// Step 3: Wait for all workers to finish
	c.logger.Debug("waiting for all outbound workers to finish")
	c.wg.Wait()

	c.logger.Info("outbound client stopped successfully")
	return nil
}




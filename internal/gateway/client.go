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
	"rule-router/config" // NEW: Import config package
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
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
	cancel       context.CancelFunc
	logger       *logger.Logger
}

// ConsumerConfig contains JetStream consumer configuration
type ConsumerConfig struct {
	SubscriberCount int
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
	httpClientCfg *config.HTTPClientConfig, // CHANGED: Pass the full client config struct
) *OutboundClient {
	return &OutboundClient{
		logger:        logger,
		metrics:       metrics,
		processor:     processor,
		jetstream:     js,
		consumerCfg:   consumerCfg,
		subscriptions: make([]*OutboundSubscription, 0),
		httpClient: &http.Client{
			Timeout: httpClientCfg.Timeout, // Use timeout from config
			Transport: &http.Transport{
				MaxIdleConns:        httpClientCfg.MaxIdleConns,
				MaxIdleConnsPerHost: httpClientCfg.MaxIdleConnsPerHost,
				IdleConnTimeout:     httpClientCfg.IdleConnTimeout,
				TLSClientConfig: &tls.Config{
					// CHANGED: Use the configurable value
					InsecureSkipVerify: httpClientCfg.TLS.InsecureSkipVerify,
				},
			},
		},
	}
}

func (c *OutboundClient) GetSubscriptions() []*OutboundSubscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subs := make([]*OutboundSubscription, len(c.subscriptions))
	copy(subs, c.subscriptions)
	return subs
}

// AddSubscription adds a NATS subscription for outbound HTTP
func (c *OutboundClient) AddSubscription(streamName, consumerName, subject string, workers int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	stream, err := c.jetstream.Stream(context.Background(), streamName)
	if err != nil {
		return fmt.Errorf("failed to get stream '%s': %w", streamName, err)
	}

	consumer, err := stream.Consumer(context.Background(), consumerName)
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

	c.subscriptions = append(c.subscriptions, sub)

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

	c.logger.Info("starting outbound client",
		"subscriptions", len(c.subscriptions),
		"fetchBatchSize", c.consumerCfg.FetchBatchSize)

	for _, sub := range c.subscriptions {
		subCtx, cancel := context.WithCancel(ctx)
		sub.cancel = cancel

		msgChan := make(chan jetstream.Msg, c.consumerCfg.FetchBatchSize*2)

		// Start fetcher
		c.wg.Add(1)
		go c.fetcher(subCtx, sub, msgChan)

		// Start workers
		for i := 0; i < sub.Workers; i++ {
			c.wg.Add(1)
			go c.processingWorker(subCtx, msgChan, i, sub.Subject)
		}

		c.logger.Info("outbound subscription started",
			"subject", sub.Subject,
			"stream", sub.StreamName,
			"consumer", sub.ConsumerName,
			"workers", sub.Workers)
	}

	return nil
}

// Stop gracefully shuts down all outbound subscriptions
func (c *OutboundClient) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("stopping outbound client", "subscriptions", len(c.subscriptions))

	// Cancel all contexts
	for _, sub := range c.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}

	// Wait for all goroutines
	c.wg.Wait()
	c.logger.Info("outbound client stopped")
	return nil
}

// fetcher pulls messages from JetStream
func (c *OutboundClient) fetcher(ctx context.Context, sub *OutboundSubscription, msgChan chan<- jetstream.Msg) {
	defer c.wg.Done()
	defer close(msgChan)

	sub.logger.Debug("outbound fetcher started", "subject", sub.Subject)

	for {
		select {
		case <-ctx.Done():
			sub.logger.Debug("outbound fetcher shutting down", "subject", sub.Subject)
			return
		default:
		}

		msgs, err := sub.Consumer.FetchNoWait(c.consumerCfg.FetchBatchSize)
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

// processingWorker processes messages and makes HTTP requests
func (c *OutboundClient) processingWorker(ctx context.Context, msgChan <-chan jetstream.Msg, workerID int, subject string) {
	defer c.wg.Done()

	c.logger.Debug("outbound worker started", "subject", subject, "workerID", workerID)

	for msg := range msgChan {
		currentMsg := msg

		func() {
			// Panic recovery
			defer func() {
				if r := recover(); r != nil {
					c.logger.Error("panic recovered in outbound worker",
						"panic", r,
						"subject", currentMsg.Subject(),
						"stack", string(debug.Stack()))

					if termErr := currentMsg.Term(); termErr != nil {
						c.logger.Error("failed to terminate message after panic",
							"subject", currentMsg.Subject(),
							"error", termErr)
					}

					if c.metrics != nil {
						c.metrics.IncMessagesTotal("error")
					}
				}
			}()

			// Process message
			if err := c.processMessage(ctx, currentMsg); err != nil {
				c.logger.Error("failed to process outbound message",
					"subject", currentMsg.Subject(),
					"error", err)

				// NAK on failure (will retry up to maxDeliver)
				if nakErr := currentMsg.Nak(); nakErr != nil {
					c.logger.Error("failed to NAK message",
						"subject", currentMsg.Subject(),
						"error", nakErr)
				}

				if c.metrics != nil {
					c.metrics.IncMessagesTotal("error")
				}
			} else {
				// ACK on success
				if ackErr := currentMsg.Ack(); ackErr != nil {
					c.logger.Error("failed to ACK message",
						"subject", currentMsg.Subject(),
						"error", ackErr)
				}

				if c.metrics != nil {
					c.metrics.IncMessagesTotal("processed")
				}
			}
		}()
	}

	c.logger.Debug("outbound worker finished", "subject", subject, "workerID", workerID)
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
	initialDelay := 1 * time.Second
	maxDelay := 30 * time.Second

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
		jitter := time.Duration(rand.Intn(100)) * time.Millisecond
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

	// Read response body (for logging)
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit

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

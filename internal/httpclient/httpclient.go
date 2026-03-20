// file: internal/httpclient/httpclient.go

package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Constants for HTTP request execution
const (
	// MaxResponseSize is the maximum size of an HTTP response body to read for logging (1MB).
	MaxResponseSize = 1024 * 1024

	// retryInitialDelay is the default initial delay for HTTP request retries
	retryInitialDelay = 1 * time.Second

	// retryMaxDelay is the default maximum delay for HTTP request retries
	retryMaxDelay = 30 * time.Second

	// retryMaxJitter is the maximum jitter added to HTTP retry delays
	retryMaxJitter = 100 * time.Millisecond
)

// HTTPExecutor handles HTTP request execution with retry logic.
// Used by both the http-gateway and rule-scheduler to execute HTTP actions.
type HTTPExecutor struct {
	client  *http.Client
	logger  *logger.Logger
	metrics *metrics.Metrics // may be nil
}

// NewHTTPExecutor creates a new HTTP executor from config
func NewHTTPExecutor(
	cfg *config.HTTPClientConfig,
	log *logger.Logger,
	m *metrics.Metrics,
) *HTTPExecutor {
	return &HTTPExecutor{
		client: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        cfg.MaxIdleConns,
				MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
				IdleConnTimeout:     cfg.IdleConnTimeout,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: cfg.TLS.InsecureSkipVerify,
				},
			},
		},
		logger:  log,
		metrics: m,
	}
}

// ExecuteHTTPAction makes an HTTP request with retry logic
func (e *HTTPExecutor) ExecuteHTTPAction(ctx context.Context, action *rule.HTTPAction) error {
	// Get retry config (use defaults if not specified)
	maxAttempts := 3
	initialDelay := retryInitialDelay
	maxDelay := retryMaxDelay

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
		err := e.makeHTTPRequest(ctx, action)
		if err == nil {
			// Success!
			e.logger.Debug("HTTP request succeeded",
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
		jitter := time.Duration(rand.Intn(int(retryMaxJitter.Milliseconds()))) * time.Millisecond
		delay += jitter

		e.logger.Warn("HTTP request failed, retrying",
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
func (e *HTTPExecutor) makeHTTPRequest(ctx context.Context, action *rule.HTTPAction) error {
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
	resp, err := e.client.Do(req)
	if err != nil {
		if e.metrics != nil {
			e.metrics.IncHTTPOutboundRequestsTotal(action.URL, "error")
		}
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for logging (ignore errors - best effort)
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))

	// Record metrics
	duration := time.Since(start).Seconds()
	if e.metrics != nil {
		e.metrics.IncHTTPOutboundRequestsTotal(action.URL, fmt.Sprintf("%d", resp.StatusCode))
		e.metrics.ObserveHTTPOutboundDuration(action.URL, duration)
	}

	// Check status code (2xx = success)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		e.logger.Debug("HTTP request successful",
			"url", action.URL,
			"method", action.Method,
			"statusCode", resp.StatusCode,
			"duration", duration)
		return nil
	}

	// Non-2xx status is an error
	e.logger.Warn("HTTP request returned non-2xx status",
		"url", action.URL,
		"method", action.Method,
		"statusCode", resp.StatusCode,
		"responseBody", string(responseBody),
		"duration", duration)

	return fmt.Errorf("HTTP request returned status %d", resp.StatusCode)
}

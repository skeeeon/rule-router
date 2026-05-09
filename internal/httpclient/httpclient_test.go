// file: internal/httpclient/httpclient_test.go

package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// newTestExecutor creates an HTTPExecutor with test defaults, nil metrics, and no publisher.
func newTestExecutor() *HTTPExecutor {
	cfg := &config.HTTPClientConfig{
		Timeout:             10 * time.Second,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     30 * time.Second,
	}
	return NewHTTPExecutor(cfg, logger.NewNopLogger(), nil, nil)
}

// newTestExecutorWithPublisher is like newTestExecutor but with a response publisher.
func newTestExecutorWithPublisher(p ResponsePublisher) *HTTPExecutor {
	cfg := &config.HTTPClientConfig{
		Timeout:             10 * time.Second,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     30 * time.Second,
	}
	return NewHTTPExecutor(cfg, logger.NewNopLogger(), nil, p)
}

// fakePublisher captures NATS publish calls for assertions.
type fakePublisher struct {
	calls []*rule.NATSAction
	err   error
}

func (f *fakePublisher) Publish(ctx context.Context, action *rule.NATSAction) error {
	f.calls = append(f.calls, action)
	return f.err
}

func TestExecuteHTTPAction_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Payload: `{"test": true}`,
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestExecuteHTTPAction_NonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Payload: `{"test": true}`,
		Retry: &rule.RetryConfig{
			MaxAttempts:  1,
			InitialDelay: "10ms",
		},
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err == nil {
		t.Fatal("expected error for 500 status, got nil")
	}
}

func TestExecuteHTTPAction_RetryThenSuccess(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Payload: `{"test": true}`,
		Retry: &rule.RetryConfig{
			MaxAttempts:  3,
			InitialDelay: "10ms",
			MaxDelay:     "50ms",
		},
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestExecuteHTTPAction_RetryExhaustion(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		Retry: &rule.RetryConfig{
			MaxAttempts:  3,
			InitialDelay: "10ms",
			MaxDelay:     "50ms",
		},
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err == nil {
		t.Fatal("expected error after retry exhaustion, got nil")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestExecuteHTTPAction_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Retry: &rule.RetryConfig{
			MaxAttempts:  5,
			InitialDelay: "500ms",
			MaxDelay:     "1s",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := executor.ExecuteHTTPAction(ctx, action)
	if err == nil {
		t.Fatal("expected error from context cancellation, got nil")
	}
}

func TestExecuteHTTPAction_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Payload: `{"test": true}`,
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
			"Authorization":   "Bearer test-token",
		},
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("expected X-Custom-Header=custom-value, got %s", receivedHeaders.Get("X-Custom-Header"))
	}
	if receivedHeaders.Get("Authorization") != "Bearer test-token" {
		t.Errorf("expected Authorization=Bearer test-token, got %s", receivedHeaders.Get("Authorization"))
	}
}

func TestExecuteHTTPAction_DefaultContentType(t *testing.T) {
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:     server.URL,
		Method:  "POST",
		Payload: `{"test": true}`,
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if receivedContentType != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", receivedContentType)
	}
}

func TestExecuteHTTPAction_CustomContentType(t *testing.T) {
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "POST",
		Payload: `<xml>test</xml>`,
		Headers: map[string]string{
			"Content-Type": "application/xml",
		},
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if receivedContentType != "application/xml" {
		t.Errorf("expected Content-Type=application/xml, got %s", receivedContentType)
	}
}

func TestExecuteHTTPAction_PassthroughPayload(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := newTestExecutor()
	rawPayload := []byte(`{"raw": "data"}`)
	action := &rule.HTTPAction{
		URL:         server.URL,
		Method:      "POST",
		Passthrough: true,
		RawPayload:  rawPayload,
	}

	err := executor.ExecuteHTTPAction(context.Background(), action)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if receivedBody != string(rawPayload) {
		t.Errorf("expected body %q, got %q", string(rawPayload), receivedBody)
	}
}

func TestExecuteHTTPAction_MethodAndURL(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			var receivedMethod string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedMethod = r.Method
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			executor := newTestExecutor()
			action := &rule.HTTPAction{
				URL:    server.URL,
				Method: method,
			}

			err := executor.ExecuteHTTPAction(context.Background(), action)
			if err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if receivedMethod != method {
				t.Errorf("expected method %s, got %s", method, receivedMethod)
			}
		})
	}
}

func TestExecuteHTTPAction_2xxStatusCodes(t *testing.T) {
	statusCodes := []int{200, 201, 202, 204}
	for _, code := range statusCodes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			executor := newTestExecutor()
			action := &rule.HTTPAction{
				URL:    server.URL,
				Method: "POST",
				Retry:  &rule.RetryConfig{MaxAttempts: 1},
			}

			err := executor.ExecuteHTTPAction(context.Background(), action)
			if err != nil {
				t.Fatalf("expected success for status %d, got error: %v", code, err)
			}
		})
	}
}

func TestExecuteHTTPAction_DefaultRetry(t *testing.T) {
	// When no retry config is provided, defaults to 3 attempts
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	executor := newTestExecutor()
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		// No Retry config — defaults apply
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := executor.ExecuteHTTPAction(ctx, action)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 default attempts, got %d", attempts)
	}
}

func TestExecuteHTTPAction_PublishResponse_OnSuccess(t *testing.T) {
	body := `{"status":"ok","value":42}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	pub := &fakePublisher{}
	executor := newTestExecutorWithPublisher(pub)
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		PublishResponse: &rule.PublishResponseSpec{
			Subject: "poll.devices.123.status",
		},
	}

	if err := executor.ExecuteHTTPAction(context.Background(), action); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.calls))
	}
	got := pub.calls[0]
	if got.Subject != "poll.devices.123.status" {
		t.Errorf("subject mismatch: got %q want %q", got.Subject, "poll.devices.123.status")
	}
	if !got.Passthrough {
		t.Errorf("expected Passthrough=true on response publish")
	}
	if string(got.RawPayload) != body {
		t.Errorf("payload mismatch: got %q want %q", string(got.RawPayload), body)
	}
}

func TestExecuteHTTPAction_PublishResponse_NotCalledOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	pub := &fakePublisher{}
	executor := newTestExecutorWithPublisher(pub)
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		Retry: &rule.RetryConfig{
			MaxAttempts:  1,
			InitialDelay: "1ms",
			MaxDelay:     "1ms",
		},
		PublishResponse: &rule.PublishResponseSpec{
			Subject: "should.not.publish",
		},
	}

	if err := executor.ExecuteHTTPAction(context.Background(), action); err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(pub.calls) != 0 {
		t.Fatalf("expected no publishes on non-2xx, got %d", len(pub.calls))
	}
}

func TestExecuteHTTPAction_PublishResponse_NotSetSkipsPublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	pub := &fakePublisher{}
	executor := newTestExecutorWithPublisher(pub)
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		// PublishResponse not set
	}

	if err := executor.ExecuteHTTPAction(context.Background(), action); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(pub.calls) != 0 {
		t.Fatalf("expected no publish when PublishResponse not set, got %d", len(pub.calls))
	}
}

func TestExecuteHTTPAction_PublishResponse_NoPublisherSkipsPublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	executor := newTestExecutor() // no publisher
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		PublishResponse: &rule.PublishResponseSpec{
			Subject: "no.publisher",
		},
	}

	// HTTP request itself should still succeed; publish is silently skipped.
	if err := executor.ExecuteHTTPAction(context.Background(), action); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

func TestExecuteHTTPAction_PublishResponse_PublishErrorDoesNotFailAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	pub := &fakePublisher{err: fmt.Errorf("nats unavailable")}
	executor := newTestExecutorWithPublisher(pub)
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		PublishResponse: &rule.PublishResponseSpec{
			Subject: "poll.errors",
		},
	}

	// HTTP succeeded; publish failure should be logged but not surfaced.
	if err := executor.ExecuteHTTPAction(context.Background(), action); err != nil {
		t.Fatalf("publish error should not fail HTTP action, got: %v", err)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish attempt, got %d", len(pub.calls))
	}
}

func TestExecuteHTTPAction_PublishResponse_BodyCappedAt1MB(t *testing.T) {
	// Server responds with 2 MB; executor should cap at MaxResponseSize (1 MB).
	bigBody := make([]byte, 2*MaxResponseSize)
	for i := range bigBody {
		bigBody[i] = 'A'
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bigBody)
	}))
	defer server.Close()

	pub := &fakePublisher{}
	executor := newTestExecutorWithPublisher(pub)
	action := &rule.HTTPAction{
		URL:    server.URL,
		Method: "GET",
		PublishResponse: &rule.PublishResponseSpec{
			Subject: "poll.big",
		},
	}

	if err := executor.ExecuteHTTPAction(context.Background(), action); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.calls))
	}
	if got := len(pub.calls[0].RawPayload); got != MaxResponseSize {
		t.Errorf("expected payload capped at %d bytes, got %d", MaxResponseSize, got)
	}
}

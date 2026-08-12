// Package gateway bridges HTTP and NATS in the inbound direction: it serves
// webhooks and turns them into rule evaluations.
//
// InboundServer routes every path through one catch-all handler that asks the
// Processor which rules match, so exact paths and wildcard patterns behave the
// same whether the rules came from disk or from KV. Requests are handled
// fire-and-forget on a bounded worker pool unless a matching rule produces a
// synchronous response (a respond action, or a NATS action with request:true),
// in which case the handler answers inline.
//
// An hmac block on an HTTP trigger is enforced here as a fail-closed gate over
// the raw body, before any rule fires.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"rule-router/internal/deferred"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Server defaults and limits
const (
	// DefaultInboundWorkerCount is the default number of workers processing inbound webhooks.
	DefaultInboundWorkerCount = 10
	// DefaultInboundQueueSize is the default size of the inbound work queue.
	DefaultInboundQueueSize = 100
	// MaxInboundBodySize is the maximum size of an inbound HTTP request body (10MB).
	MaxInboundBodySize = 10 * 1024 * 1024
	// DefaultRequestTimeout bounds an HTTP→NATS bridge request when a rule does
	// not specify its own timeout. Note: the HTTP server's WriteTimeout must
	// exceed this, or a slow responder's reply may be cut off.
	DefaultRequestTimeout = 5 * time.Second
	// deferredActionTimeout bounds publishing of a trailing-throttle batch. The
	// webhook was answered when the window opened, so no client is waiting.
	deferredActionTimeout = 30 * time.Second
)

// Standard JSON responses
const (
	responseAccepted = `{"status":"accepted"}`
	responseHealthy  = `{"status":"healthy"}`
)

// webhookJob represents a unit of work to be processed by a worker.
// It encapsulates all necessary data from an incoming HTTP request.
type webhookJob struct {
	path    string
	method  string
	body    []byte
	headers map[string]string
}

// InboundServer handles HTTP requests and publishes to NATS.
// It uses a fixed-size worker pool for bounded concurrency and backpressure.
// All HTTP paths route through a single catch-all handler that delegates
// path-to-rule matching to the Processor. This handles both exact and
// wildcard paths for file-loaded and KV-loaded rules.
type InboundServer struct {
	logger     *logger.Logger
	metrics    *metrics.Metrics
	processor  *rule.Processor
	publisher  NATSPublisher
	natsConn   *nats.Conn
	httpServer *http.Server
	serverCfg  *Config

	// Worker pool components
	workQueue chan webhookJob // Buffered channel acting as a job queue
	wg        sync.WaitGroup  // Waits for all workers to gracefully shut down

	// coalescer holds trailing-throttle NATS actions and publishes the last one
	// per window, long after the webhook was answered.
	coalescer *deferred.Coalescer
}

// Config contains HTTP server configuration.
// Added worker pool configuration fields.
type Config struct {
	Address             string
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	MaxHeaderBytes      int
	ShutdownGracePeriod time.Duration

	// Number of concurrent workers processing inbound webhooks.
	// This should be a configurable value.
	InboundWorkerCount int

	// Size of the buffered channel for incoming webhooks.
	// This allows the server to absorb bursts of traffic.
	InboundQueueSize int
}

// NATSPublisher publishes an evaluated NATS action. Satisfied by the broker's
// shared ActionPublisher, which owns transport selection (per-action mode over
// the global default), the ack wait, and the retry policy — so this package
// does not reimplement any of it.
type NATSPublisher interface {
	PublishWithRetry(ctx context.Context, action *rule.NATSAction) error
}

// NewInboundServer creates a new HTTP inbound server with a worker pool.
// nc is used only for the HTTP↔NATS request/reply bridge; ordinary publishes go
// through publisher.
func NewInboundServer(
	logger *logger.Logger,
	metrics *metrics.Metrics,
	processor *rule.Processor,
	publisher NATSPublisher,
	nc *nats.Conn,
	serverCfg *Config,
) *InboundServer {
	logger = logger.With("component", "gateway")

	// Grug brain: Use good defaults if not set.
	if serverCfg.InboundWorkerCount <= 0 {
		serverCfg.InboundWorkerCount = DefaultInboundWorkerCount
		logger.Info("InboundWorkerCount not set, using default", "count", serverCfg.InboundWorkerCount)
	}
	if serverCfg.InboundQueueSize <= 0 {
		serverCfg.InboundQueueSize = DefaultInboundQueueSize
		logger.Info("InboundQueueSize not set, using default", "size", serverCfg.InboundQueueSize)
	}

	s := &InboundServer{
		logger:    logger,
		metrics:   metrics,
		processor: processor,
		publisher: publisher,
		natsConn:  nc,
		serverCfg: serverCfg,
		// Initialize the buffered channel for the work queue.
		workQueue: make(chan webhookJob, serverCfg.InboundQueueSize),
	}
	s.coalescer = deferred.New("http-inbound", s.executeDeferred, deferredActionTimeout, logger, metrics)
	return s
}

// executeDeferred publishes a trailing-throttle NATS action once its window has
// closed. Only NATS actions reach here: the inbound server never runs outbound
// HTTP actions, and respond/request actions cannot be deferred.
func (s *InboundServer) executeDeferred(ctx context.Context, action *rule.Action) error {
	if action.NATS == nil {
		s.logger.Warn("deferred non-NATS action on inbound path; skipping")
		return nil
	}
	if err := s.publishToNATS(ctx, action.NATS); err != nil {
		if s.metrics != nil {
			s.metrics.IncActionsTotal("error")
		}
		return fmt.Errorf("failed to publish deferred NATS action to %s: %w", action.NATS.Subject, err)
	}
	if s.metrics != nil {
		s.metrics.IncActionsTotal("success")
	}
	return nil
}

// Start begins the HTTP server and starts the worker pool.
func (s *InboundServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health check endpoints (always registered)
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/healthz", s.healthHandler)

	// Single catch-all handler delegates path matching to the processor.
	// Static, wildcard, file-loaded, and KV-loaded rules all flow through here.
	mux.HandleFunc("/", s.webhookHandler)

	for _, path := range s.processor.HTTPPaths() {
		s.logger.Info("registered HTTP rule path", "path", path)
	}

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:              s.serverCfg.Address,
		Handler:           mux,
		ReadTimeout:       s.serverCfg.ReadTimeout,
		ReadHeaderTimeout: s.serverCfg.ReadTimeout,
		WriteTimeout:      s.serverCfg.WriteTimeout,
		IdleTimeout:       s.serverCfg.IdleTimeout,
		MaxHeaderBytes:    s.serverCfg.MaxHeaderBytes,
	}

	// Start the fixed-size pool of worker goroutines.
	s.startWorkers(ctx)

	// Start server in goroutine
	go func() {
		s.logger.Info("starting HTTP inbound server",
			"address", s.serverCfg.Address,
			"workers", s.serverCfg.InboundWorkerCount,
			"queueSize", s.serverCfg.InboundQueueSize)

		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

// startWorkers launches the fixed pool of goroutines. Workers drain the queue
// until it is closed by Stop() rather than exiting on context cancellation — an
// already-accepted webhook (the server answered 200) must still be processed on
// shutdown. Publishing therefore uses context.Background() (bounded per-publish by
// AckTimeout in publishToNATS), not the lifecycle context, which is cancelled
// before Stop() runs. The ctx parameter is intentionally ignored.
func (s *InboundServer) startWorkers(_ context.Context) {
	s.logger.Info("starting inbound workers", "count", s.serverCfg.InboundWorkerCount)
	for i := 0; i < s.serverCfg.InboundWorkerCount; i++ {
		s.wg.Add(1)
		workerID := i + 1
		go func() {
			defer s.wg.Done()
			for job := range s.workQueue {
				if s.metrics != nil {
					s.metrics.SetMessageProcessingBacklog(float64(len(s.workQueue)))
				}
				s.processWebhookWithRecovery(context.Background(), job.path, job.method, job.body, job.headers, workerID)
			}
		}()
	}
}

// Stop gracefully shuts down the HTTP server and the worker pool.
func (s *InboundServer) Stop(ctx context.Context) error {
	s.logger.Info("stopping HTTP inbound server and workers")

	// 1. Stop the HTTP server first to prevent new requests from arriving.
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.logger.Error("failed to gracefully shutdown HTTP server", "error", err)
			// Continue with shutdown even if this fails.
		}
	}

	// 2. Close the workQueue channel. Workers range over it and exit once it is
	//    drained. The HTTP server is already shut down (step 1), and the handler's
	//    enqueue is a non-blocking select, so no goroutine can send after this.
	s.logger.Info("closing work queue, waiting for workers to drain accepted webhooks")
	close(s.workQueue)

	// 3. Wait for workers to finish draining, bounded by the shutdown grace period
	//    (ctx). Because the queue is closed, workers exit on their own once it is
	//    empty; the bound only prevents a wedged upstream from blocking shutdown.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("HTTP inbound server and all workers stopped successfully")
	case <-ctx.Done():
		s.logger.Warn("shutdown grace period expired with accepted webhooks still draining",
			"remaining", len(s.workQueue))
	}

	// 4. Flush pending trailing-throttle batches. Workers are done, so no new
	//    batches can be submitted. Shares the caller's grace period.
	s.coalescer.Stop(ctx)

	return nil
}

// processWebhookWithRecovery wraps processWebhook with panic recovery.
// This ensures a single malformed request cannot crash the entire worker.
func (s *InboundServer) processWebhookWithRecovery(ctx context.Context, path, method string, body []byte, headers map[string]string, workerID int) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic recovered in inbound HTTP worker",
				"panic", r,
				"path", path,
				"method", method,
				"workerID", workerID,
				"stack", string(debug.Stack()))

			if s.metrics != nil {
				s.metrics.IncMessagesTotal("error")
			}
		}
	}()

	s.processWebhook(ctx, path, method, body, headers)
}

// processWebhook processes the webhook and publishes to NATS
func (s *InboundServer) processWebhook(ctx context.Context, path, method string, body []byte, headers map[string]string) {
	// Process through rule engine
	outcome, err := s.processor.ProcessHTTP(path, method, body, headers)
	if err != nil {
		s.logger.Error("failed to process webhook",
			"path", path,
			"method", method,
			"error", err)
		return
	}

	// Trailing-throttle batches publish when their window closes. The webhook
	// was already answered 202, so nothing waits on them.
	for _, batch := range outcome.Deferred {
		s.coalescer.Submit(batch)
	}

	// Publish all matched actions to NATS
	for _, action := range outcome.Immediate {
		if action.NATS != nil {
			if err := s.publishToNATS(ctx, action.NATS); err != nil {
				s.logger.Error("failed to publish to NATS",
					"path", path,
					"method", method,
					"subject", action.NATS.Subject,
					"error", err)
				if s.metrics != nil {
					s.metrics.IncActionsTotal("error")
				}
			} else {
				s.logger.Debug("published to NATS",
					"path", path,
					"method", method,
					"subject", action.NATS.Subject)
				if s.metrics != nil {
					s.metrics.IncActionsTotal("success")
				}
			}
		} else if action.HTTP != nil {
			// HTTP actions not supported in inbound server
			s.logger.Warn("HTTP action in inbound webhook rule - this should use outbound client",
				"path", path,
				"url", action.HTTP.URL)
		}
	}
}

// publishToNATS publishes a NATS action on the shared publisher, so an inbound
// webhook reaches NATS by exactly the same path — transport, mode resolution,
// ack wait, retry policy — as a router or scheduler action.
func (s *InboundServer) publishToNATS(ctx context.Context, action *rule.NATSAction) error {
	return s.publisher.PublishWithRetry(ctx, action)
}

// routeInfo carries a request's routing identity through the response helpers.
//
// path is the concrete request path — used for rule matching and logging.
// route is the rule-declared pattern that matched it, used as the Prometheus
// `path` label so a wildcard route reports one series instead of one per URL.
// For exact-path rules the two are identical.
type routeInfo struct {
	path   string
	route  string
	method string
}

// webhookHandler is the single HTTP entry point. It delegates path matching
// to the Processor (which handles both exact paths and wildcard patterns),
// enqueues matching requests onto the worker pool, and returns 503 when the
// queue is full. Unknown paths receive 404 with a sentinel metric label to
// bound metric cardinality under path-scan traffic.
func (s *InboundServer) webhookHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	path := r.URL.Path

	s.logger.Debug("webhook received",
		"path", path,
		"method", r.Method,
		"remoteAddr", r.RemoteAddr)

	// Metrics are labelled with the matched rule pattern, never the raw path —
	// see Processor.MatchHTTPPath.
	matchedRoute, matched := s.processor.MatchHTTPPath(path)
	if !matched {
		s.logger.Debug("no rule for path, returning 404", "path", path, "method", r.Method)
		http.Error(w, "Not Found", http.StatusNotFound)
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal("_unknown_", r.Method, "404")
			s.metrics.ObserveHTTPRequestDuration("_unknown_", r.Method, time.Since(start).Seconds())
		}
		return
	}

	rt := routeInfo{path: path, route: matchedRoute, method: r.Method}

	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxInboundBodySize))
	if err != nil {
		s.logger.Error("failed to read request body", "error", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal(rt.route, rt.method, "400")
			s.metrics.ObserveHTTPRequestDuration(rt.route, rt.method, time.Since(start).Seconds())
		}
		return
	}

	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[textproto.CanonicalMIMEHeaderKey(key)] = values[0]
		}
	}

	// Fail-closed HMAC gate: if any rule matching this path/method declares an
	// `hmac` block, the request must carry a valid HMAC over the raw body, or it
	// is rejected here — before any rule fires (sync or fire-and-forget).
	// CheckHTTPHMAC records the per-result hmac metric.
	if required, ok := s.processor.CheckHTTPHMAC(path, r.Method, body, headers); required && !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal(rt.route, rt.method, "401")
			s.metrics.ObserveHTTPRequestDuration(rt.route, rt.method, time.Since(start).Seconds())
		}
		return
	}

	// Synchronous routes (respond actions / HTTP↔NATS bridge) are handled inline
	// in this request goroutine — they must write a response, so they bypass the
	// fire-and-forget worker queue. Plain webhook routes keep the bounded-queue
	// fire-and-forget behavior below.
	if s.processor.HasSyncHTTPPath(path, r.Method) {
		s.handleSync(w, r, rt, body, headers, start)
		return
	}

	job := webhookJob{
		path:    path,
		method:  r.Method,
		body:    body,
		headers: headers,
	}

	select {
	case s.workQueue <- job:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(responseAccepted))
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal(rt.route, rt.method, "200")
			s.metrics.SetMessageProcessingBacklog(float64(len(s.workQueue)))
		}
	default:
		s.logger.Warn("inbound work queue full, rejecting request",
			"path", path,
			"queueCapacity", s.serverCfg.InboundQueueSize,
			"hint", "increase http.server.inboundQueueSize in config or add more workers")
		http.Error(w, fmt.Sprintf("Service Unavailable: webhook queue full (capacity %d). Increase http.server.inboundQueueSize in config.",
			s.serverCfg.InboundQueueSize), http.StatusServiceUnavailable)
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal(rt.route, rt.method, "503")
		}
	}

	if s.metrics != nil {
		s.metrics.ObserveHTTPRequestDuration(rt.route, rt.method, time.Since(start).Seconds())
	}
}

// handleSync processes a synchronous HTTP rule inline in the request goroutine
// (no work queue) and writes the evaluated response. It serves two shapes:
//   - respond action (B1): the evaluated payload is written as the HTTP response
//   - NATS request action (B2): the gateway does nc.Request and returns the reply
//
// The first respond/request action produced wins the response; any plain NATS
// publish actions fire as side-effects. A context deadline bounds bridge calls.
func (s *InboundServer) handleSync(w http.ResponseWriter, r *http.Request, rt routeInfo, body []byte, headers map[string]string, start time.Time) {
	outcome, err := s.processor.ProcessHTTP(rt.path, rt.method, body, headers)
	if err != nil {
		s.logger.Error("failed to process synchronous webhook", "path", rt.path, "method", rt.method, "error", err)
		s.writeSyncError(w, rt, http.StatusInternalServerError, "Internal Server Error", start)
		return
	}

	// Side-effect publishes on a sync path can still be trailing-throttled; the
	// response itself cannot (the loader rejects trailing with request: true and
	// throttle on respond actions).
	for _, batch := range outcome.Deferred {
		s.coalescer.Submit(batch)
	}

	responded := false
	for _, action := range outcome.Immediate {
		switch {
		case action.Respond != nil:
			if responded {
				continue
			}
			s.writeRespond(w, rt, action.Respond, start)
			responded = true
		case action.NATS != nil && action.NATS.Request:
			if responded {
				continue
			}
			s.bridgeRequest(w, r, rt, action.NATS, start)
			responded = true
		case action.NATS != nil:
			// Plain publish fires as a side-effect alongside the response.
			if err := s.publishToNATS(r.Context(), action.NATS); err != nil {
				s.logger.Error("failed to publish side-effect NATS action",
					"path", rt.path, "subject", action.NATS.Subject, "error", err)
			}
		case action.HTTP != nil:
			s.logger.Warn("HTTP action in inbound webhook rule - this should use outbound client",
				"path", rt.path, "url", action.HTTP.URL)
		}
	}

	if !responded {
		// The path has a sync rule (HasSyncHTTPPath) but no respond/request action
		// fired this time — conditions didn't match. Nothing to return.
		s.writeSyncError(w, rt, http.StatusNotFound, "Not Found", start)
	}
}

// writeRespond writes a respond action's evaluated payload as the HTTP response.
func (s *InboundServer) writeRespond(w http.ResponseWriter, rt routeInfo, action *rule.RespondAction, start time.Time) {
	for k, v := range action.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}

	status := action.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if action.Passthrough {
		w.Write(action.RawPayload)
	} else {
		w.Write([]byte(action.Payload))
	}
	s.recordSync(rt, status, start)
}

// bridgeRequest performs the HTTP↔NATS bridge: it sends a NATS request and
// returns the reply as the HTTP response. No responder → 503; timeout → 504.
func (s *InboundServer) bridgeRequest(w http.ResponseWriter, r *http.Request, rt routeInfo, action *rule.NATSAction, start time.Time) {
	timeout := DefaultRequestTimeout
	if action.Timeout != "" {
		if d, err := time.ParseDuration(action.Timeout); err == nil {
			timeout = d
		}
	}

	var payloadBytes []byte
	if action.Passthrough {
		payloadBytes = action.RawPayload
	} else {
		payloadBytes = []byte(action.Payload)
	}

	msg := nats.NewMsg(action.Subject)
	msg.Data = payloadBytes
	if len(action.Headers) > 0 {
		msg.Header = make(nats.Header)
		for k, v := range action.Headers {
			msg.Header.Set(k, v)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	reply, err := s.natsConn.RequestMsgWithContext(ctx, msg)
	if err != nil {
		switch {
		case errors.Is(err, nats.ErrNoResponders):
			s.logger.Warn("NATS bridge request: no responders", "path", rt.path, "subject", action.Subject)
			s.writeSyncError(w, rt, http.StatusServiceUnavailable, "Service Unavailable: no NATS responder", start)
		case r.Context().Err() != nil:
			// The client disconnected before the reply arrived — the parent request
			// context (not our timeout) was cancelled. Nothing to send; don't pretend
			// it was a timeout. 499 (nginx "Client Closed Request") for the metric only.
			s.logger.Debug("NATS bridge request abandoned: client disconnected", "path", rt.path, "subject", action.Subject)
			s.recordSync(rt, 499, start)
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
			s.logger.Warn("NATS bridge request timed out", "path", rt.path, "subject", action.Subject, "timeout", timeout)
			s.writeSyncError(w, rt, http.StatusGatewayTimeout, "Gateway Timeout: NATS request timed out", start)
		default:
			s.logger.Warn("NATS bridge request failed", "path", rt.path, "subject", action.Subject, "error", err)
			s.writeSyncError(w, rt, http.StatusBadGateway, "Bad Gateway: NATS request failed", start)
		}
		return
	}

	ct := reply.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	w.Write(reply.Data)
	s.recordSync(rt, http.StatusOK, start)
}

// writeSyncError writes an error status for a synchronous route and records metrics.
func (s *InboundServer) writeSyncError(w http.ResponseWriter, rt routeInfo, status int, msg string, start time.Time) {
	http.Error(w, msg, status)
	s.recordSync(rt, status, start)
}

// recordSync records the request-total and duration metrics for a sync response,
// labelled by matched route rather than concrete request path.
func (s *InboundServer) recordSync(rt routeInfo, status int, start time.Time) {
	if s.metrics != nil {
		s.metrics.IncHTTPInboundRequestsTotal(rt.route, rt.method, strconv.Itoa(status))
		s.metrics.ObserveHTTPRequestDuration(rt.route, rt.method, time.Since(start).Seconds())
	}
}

func (s *InboundServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(responseHealthy))
}

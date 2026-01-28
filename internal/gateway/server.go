// file: internal/gateway/server.go

package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
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
type InboundServer struct {
	logger     *logger.Logger
	metrics    *metrics.Metrics
	processor  *rule.Processor
	jetstream  jetstream.JetStream
	natsConn   *nats.Conn
	httpServer *http.Server
	publishCfg *PublishConfig
	serverCfg  *ServerConfig

	// Worker pool components
	workQueue chan webhookJob   // Buffered channel acting as a job queue
	wg        sync.WaitGroup    // Waits for all workers to gracefully shut down
}

// ServerConfig contains HTTP server configuration.
// Added worker pool configuration fields.
type ServerConfig struct {
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

// PublishConfig contains NATS publish configuration
type PublishConfig struct {
	Mode           string        // "jetstream" or "core"
	AckTimeout     time.Duration
	MaxRetries     int
	RetryBaseDelay time.Duration
}

// NewInboundServer creates a new HTTP inbound server with a worker pool.
func NewInboundServer(
	logger *logger.Logger,
	metrics *metrics.Metrics,
	processor *rule.Processor,
	js jetstream.JetStream,
	nc *nats.Conn,
	serverCfg *ServerConfig,
	publishCfg *PublishConfig,
) *InboundServer {
	// Grug brain: Use good defaults if not set.
	if serverCfg.InboundWorkerCount <= 0 {
		serverCfg.InboundWorkerCount = 10 // Default to 10 workers
		logger.Info("InboundWorkerCount not set, using default", "count", serverCfg.InboundWorkerCount)
	}
	if serverCfg.InboundQueueSize <= 0 {
		serverCfg.InboundQueueSize = 100 // Default to a queue size of 100
		logger.Info("InboundQueueSize not set, using default", "size", serverCfg.InboundQueueSize)
	}

	return &InboundServer{
		logger:     logger,
		metrics:    metrics,
		processor:  processor,
		jetstream:  js,
		natsConn:   nc,
		serverCfg:  serverCfg,
		publishCfg: publishCfg,
		// Initialize the buffered channel for the work queue.
		workQueue: make(chan webhookJob, serverCfg.InboundQueueSize),
	}
}

// Start begins the HTTP server and starts the worker pool.
func (s *InboundServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Register handlers for all HTTP paths from rules
	paths := s.processor.GetHTTPPaths()
	for _, path := range paths {
		// Capture path in closure
		handlerPath := path
		mux.HandleFunc(handlerPath, s.webhookHandler(handlerPath))
		s.logger.Info("registered HTTP handler", "path", handlerPath)
	}

	// Health check endpoint
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/healthz", s.healthHandler)

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:           s.serverCfg.Address,
		Handler:        mux,
		ReadTimeout:    s.serverCfg.ReadTimeout,
		WriteTimeout:   s.serverCfg.WriteTimeout,
		IdleTimeout:    s.serverCfg.IdleTimeout,
		MaxHeaderBytes: s.serverCfg.MaxHeaderBytes,
	}

	// Start the fixed-size pool of worker goroutines.
	s.startWorkers(ctx)

	// Start server in goroutine
	go func() {
		s.logger.Info("starting HTTP inbound server",
			"address", s.serverCfg.Address,
			"paths", len(paths),
			"workers", s.serverCfg.InboundWorkerCount,
			"queueSize", s.serverCfg.InboundQueueSize)

		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

// startWorkers launches the fixed pool of goroutines.
func (s *InboundServer) startWorkers(ctx context.Context) {
	for i := 0; i < s.serverCfg.InboundWorkerCount; i++ {
		s.wg.Add(1)
		workerID := i + 1
		go func() {
			defer s.wg.Done()
			s.logger.Info("starting inbound worker", "workerID", workerID)
			// Loop checks both context cancellation and channel closure
			for {
				select {
				case <-ctx.Done():
					// Context cancelled - exit immediately
					s.logger.Debug("inbound worker context cancelled", "workerID", workerID)
					return
				case job, ok := <-s.workQueue:
					if !ok {
						// Channel closed - no more jobs will arrive
						s.logger.Info("inbound worker stopped", "workerID", workerID)
						return
					}
					s.processWebhook(job.path, job.method, job.body, job.headers)
				}
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

	// 2. Close the workQueue channel. This signals to the workers (ranging over the channel)
	//    that no more jobs will be sent, and they should exit their loop once empty.
	s.logger.Info("closing work queue, waiting for workers to finish in-flight jobs")
	close(s.workQueue)

	// 3. Wait for all worker goroutines to finish their current jobs and exit.
	s.wg.Wait()

	s.logger.Info("HTTP inbound server and all workers stopped successfully")
	return nil
}

// webhookHandler now ENQUEUES jobs instead of processing them directly.
// It provides backpressure by returning 503 if the queue is full.
func (s *InboundServer) webhookHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Log request
		s.logger.Debug("webhook received",
			"path", r.URL.Path,
			"method", r.Method,
			"remoteAddr", r.RemoteAddr)

		// Ensure body is closed regardless of read success/failure
		defer r.Body.Close()

		// Read request body (with size limit)
		body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
		if err != nil {
			s.logger.Error("failed to read request body", "error", err)
			http.Error(w, "Bad Request", http.StatusBadRequest)
			if s.metrics != nil {
				s.metrics.IncHTTPInboundRequestsTotal(path, r.Method, "400")
			}
			return
		}

		// Extract headers
		headers := make(map[string]string)
		for key, values := range r.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}

		// Create a job with the request data.
		job := webhookJob{
			path:    path,
			method:  r.Method,
			body:    body,
			headers: headers,
		}

		// Non-blocking send to the work queue.
		select {
		case s.workQueue <- job:
			// Job successfully enqueued. Return 200 OK.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"accepted"}`))
			if s.metrics != nil {
				s.metrics.IncHTTPInboundRequestsTotal(path, r.Method, "200")
			}
		default:
			// The queue is full. This is our backpressure mechanism.
			s.logger.Warn("inbound work queue is full, rejecting request",
				"path", path,
				"queueSize", s.serverCfg.InboundQueueSize)
			http.Error(w, "Service Unavailable: server is busy, please try again later.", http.StatusServiceUnavailable)
			if s.metrics != nil {
				s.metrics.IncHTTPInboundRequestsTotal(path, r.Method, "503")
			}
		}

		// Observe duration of the HTTP handler itself (which should be very fast).
		if s.metrics != nil {
			s.metrics.ObserveHTTPRequestDuration(path, r.Method, time.Since(start).Seconds())
		}
	}
}

// processWebhook processes the webhook and publishes to NATS
func (s *InboundServer) processWebhook(path, method string, body []byte, headers map[string]string) {
	// Process through rule engine
	actions, err := s.processor.ProcessHTTP(path, method, body, headers)
	if err != nil {
		s.logger.Error("failed to process webhook",
			"path", path,
			"method", method,
			"error", err)
		return
	}

	// Publish all matched actions to NATS
	for _, action := range actions {
		if action.NATS != nil {
			if err := s.publishToNATS(action.NATS); err != nil {
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

// publishToNATS publishes a NATS action with retry logic
func (s *InboundServer) publishToNATS(action *rule.NATSAction) error {
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

	// Publish based on mode
	if s.publishCfg.Mode == "core" {
		return s.natsConn.PublishMsg(msg)
	}

	// JetStream async publish
	ctx, cancel := context.WithTimeout(context.Background(), s.publishCfg.AckTimeout)
	defer cancel()

	ackF, err := s.jetstream.PublishMsgAsync(msg)
	if err != nil {
		return fmt.Errorf("jetstream async publish failed: %w", err)
	}

	// Wait for ACK
	select {
	case <-ackF.Ok():
		return nil
	case err := <-ackF.Err():
		return fmt.Errorf("jetstream publish failed: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("publish timeout: %w", ctx.Err())
	}
}

// healthHandler responds to health check requests
func (s *InboundServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}


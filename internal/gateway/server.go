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

// InboundServer handles HTTP requests and publishes to NATS
// Fire-and-forget: Returns 200 immediately, publishes to NATS asynchronously
type InboundServer struct {
	logger     *logger.Logger
	metrics    *metrics.Metrics
	processor  *rule.Processor
	jetstream  jetstream.JetStream
	natsConn   *nats.Conn
	httpServer *http.Server
	publishCfg *PublishConfig
	serverCfg  *ServerConfig
	mu         sync.RWMutex
}

// ServerConfig contains HTTP server configuration
type ServerConfig struct {
	Address              string
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	MaxHeaderBytes       int
	ShutdownGracePeriod  time.Duration
}

// PublishConfig contains NATS publish configuration
type PublishConfig struct {
	Mode           string        // "jetstream" or "core"
	AckTimeout     time.Duration
	MaxRetries     int
	RetryBaseDelay time.Duration
}

// NewInboundServer creates a new HTTP inbound server
func NewInboundServer(
	logger *logger.Logger,
	metrics *metrics.Metrics,
	processor *rule.Processor,
	js jetstream.JetStream,
	nc *nats.Conn,
	serverCfg *ServerConfig,
	publishCfg *PublishConfig,
) *InboundServer {
	return &InboundServer{
		logger:     logger,
		metrics:    metrics,
		processor:  processor,
		jetstream:  js,
		natsConn:   nc,
		serverCfg:  serverCfg,
		publishCfg: publishCfg,
	}
}

// Start begins the HTTP server
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
	
	// Start server in goroutine
	go func() {
		s.logger.Info("starting HTTP inbound server",
			"address", s.serverCfg.Address,
			"paths", paths)
		
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTP server error", "error", err)
		}
	}()
	
	return nil
}

// Stop gracefully shuts down the HTTP server
func (s *InboundServer) Stop(ctx context.Context) error {
	s.logger.Info("stopping HTTP inbound server")
	
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown HTTP server: %w", err)
		}
	}
	
	s.logger.Info("HTTP inbound server stopped")
	return nil
}

// webhookHandler handles incoming webhook requests
// Fire-and-forget: Returns 200 immediately, processes asynchronously
func (s *InboundServer) webhookHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Log request
		s.logger.Debug("webhook received",
			"path", r.URL.Path,
			"method", r.Method,
			"remoteAddr", r.RemoteAddr)
		
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
		defer r.Body.Close()
		
		// Extract headers
		headers := make(map[string]string)
		for key, values := range r.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
		
		// FIRE-AND-FORGET: Return 200 immediately
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"accepted"}`))
		
		if s.metrics != nil {
			s.metrics.IncHTTPInboundRequestsTotal(path, r.Method, "200")
			s.metrics.ObserveHTTPRequestDuration(path, r.Method, time.Since(start).Seconds())
		}
		
		// Process asynchronously (don't block response)
		go s.processWebhook(path, r.Method, body, headers)
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
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

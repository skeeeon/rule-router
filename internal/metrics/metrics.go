// file: internal/metrics/metrics.go

package metrics

import (
	"runtime"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics provides centralized metrics collection for both rule-router and http-gateway
type Metrics struct {
	registry *prometheus.Registry // Now exposed for HTTP metrics

	// Message processing metrics (shared)
	messagesTotal             *prometheus.CounterVec
	messageProcessingBacklog  prometheus.Gauge
	ruleMatches               prometheus.Counter
	rulesActive               prometheus.Gauge
	actionsTotal              *prometheus.CounterVec
	actionsByType             *prometheus.CounterVec
	actionPublishFailures     prometheus.Counter
	templateOpsTotal          *prometheus.CounterVec

	// NATS connection metrics (shared)
	natsConnectionStatus prometheus.Gauge
	natsReconnects       prometheus.Counter

	// Signature verification metrics (shared)
	signatureVerificationsTotal   *prometheus.CounterVec
	signatureVerificationDuration prometheus.Histogram

	// KV metrics (shared)
	kvCacheHits   prometheus.Counter
	kvCacheMisses prometheus.Counter
	kvCacheSize   prometheus.Gauge

	// System metrics (shared)
	goroutines   prometheus.Gauge
	memoryBytes  prometheus.Gauge

	// HTTP Inbound metrics (http-gateway only)
	httpInboundRequestsTotal *prometheus.CounterVec
	httpRequestDuration      *prometheus.HistogramVec

	// HTTP Outbound metrics (http-gateway only)
	httpOutboundRequestsTotal *prometheus.CounterVec
	httpOutboundDuration      *prometheus.HistogramVec

	// Internal counters for atomic operations
	stats struct {
		messagesReceived uint64
		messagesError    uint64
	}
}

// NewMetrics creates a new metrics instance with all collectors registered
func NewMetrics(registry *prometheus.Registry) (*Metrics, error) {
	m := &Metrics{
		registry: registry,

		// Message processing
		messagesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "messages_total",
				Help: "Total number of messages by status",
			},
			[]string{"status"},
		),
		messageProcessingBacklog: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "message_processing_backlog",
				Help: "Number of messages waiting to be processed",
			},
		),
		ruleMatches: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rule_matches_total",
				Help: "Total number of rule matches",
			},
		),
		rulesActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rules_active",
				Help: "Number of active rules",
			},
		),
		actionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "actions_total",
				Help: "Total number of actions by status",
			},
			[]string{"status"},
		),
		actionsByType: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "actions_by_type_total",
				Help: "Total number of actions by type",
			},
			[]string{"type"},
		),
		actionPublishFailures: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "action_publish_failures_total",
				Help: "Total number of action publish failures",
			},
		),
		templateOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "template_operations_total",
				Help: "Total number of template operations by status",
			},
			[]string{"status"},
		),

		// NATS connection
		natsConnectionStatus: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "nats_connection_status",
				Help: "NATS connection status (1 = connected, 0 = disconnected)",
			},
		),
		natsReconnects: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "nats_reconnects_total",
				Help: "Total number of NATS reconnections",
			},
		),

		// Signature verification
		signatureVerificationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "signature_verifications_total",
				Help: "Total number of signature verifications by result",
			},
			[]string{"result"},
		),
		signatureVerificationDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "signature_verification_duration_seconds",
				Help:    "Duration of signature verification operations",
				Buckets: prometheus.DefBuckets,
			},
		),

		// KV metrics
		kvCacheHits: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "kv_cache_hits_total",
				Help: "Total number of KV cache hits",
			},
		),
		kvCacheMisses: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "kv_cache_misses_total",
				Help: "Total number of KV cache misses",
			},
		),
		kvCacheSize: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "kv_cache_size",
				Help: "Current number of entries in KV cache",
			},
		),

		// System metrics
		goroutines: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "process_goroutines",
				Help: "Number of goroutines",
			},
		),
		memoryBytes: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "process_memory_bytes",
				Help: "Process memory usage in bytes",
			},
		),

		// HTTP Inbound (http-gateway)
		httpInboundRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_inbound_requests_total",
				Help: "Total number of inbound HTTP requests",
			},
			[]string{"path", "method", "status"},
		),
		httpRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "Duration of inbound HTTP requests",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"path", "method"},
		),

		// HTTP Outbound (http-gateway)
		httpOutboundRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_outbound_requests_total",
				Help: "Total number of outbound HTTP requests",
			},
			[]string{"url", "status_code"},
		),
		httpOutboundDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_outbound_duration_seconds",
				Help:    "Duration of outbound HTTP requests",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"url"},
		),
	}

	// Register all collectors
	collectors := []prometheus.Collector{
		m.messagesTotal,
		m.messageProcessingBacklog,
		m.ruleMatches,
		m.rulesActive,
		m.actionsTotal,
		m.actionsByType,
		m.actionPublishFailures,
		m.templateOpsTotal,
		m.natsConnectionStatus,
		m.natsReconnects,
		m.signatureVerificationsTotal,
		m.signatureVerificationDuration,
		m.kvCacheHits,
		m.kvCacheMisses,
		m.kvCacheSize,
		m.goroutines,
		m.memoryBytes,
		m.httpInboundRequestsTotal,
		m.httpRequestDuration,
		m.httpOutboundRequestsTotal,
		m.httpOutboundDuration,
	}

	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}

	return m, nil
}

// GetRegistry returns the Prometheus registry (needed for HTTP handler)
func (m *Metrics) GetRegistry() *prometheus.Registry {
	return m.registry
}

// Message processing metrics
func (m *Metrics) IncMessagesTotal(status string) {
	m.messagesTotal.WithLabelValues(status).Inc()
	if status == "received" {
		atomic.AddUint64(&m.stats.messagesReceived, 1)
	} else if status == "error" {
		atomic.AddUint64(&m.stats.messagesError, 1)
	}
}

func (m *Metrics) SetMessageProcessingBacklog(count float64) {
	m.messageProcessingBacklog.Set(count)
}

func (m *Metrics) IncRuleMatches() {
	m.ruleMatches.Inc()
}

func (m *Metrics) SetRulesActive(count float64) {
	m.rulesActive.Set(count)
}

func (m *Metrics) IncActionsTotal(status string) {
	m.actionsTotal.WithLabelValues(status).Inc()
}

func (m *Metrics) IncActionsByType(actionType string) {
	m.actionsByType.WithLabelValues(actionType).Inc()
}

func (m *Metrics) IncActionPublishFailures() {
	m.actionPublishFailures.Inc()
}

func (m *Metrics) IncTemplateOpsTotal(status string) {
	m.templateOpsTotal.WithLabelValues(status).Inc()
}

// NATS connection metrics
func (m *Metrics) SetNATSConnectionStatus(connected bool) {
	if connected {
		m.natsConnectionStatus.Set(1)
	} else {
		m.natsConnectionStatus.Set(0)
	}
}

func (m *Metrics) IncNATSReconnects() {
	m.natsReconnects.Inc()
}

// Signature verification metrics
func (m *Metrics) IncSignatureVerifications(result string) {
	m.signatureVerificationsTotal.WithLabelValues(result).Inc()
}

func (m *Metrics) ObserveSignatureVerificationDuration(seconds float64) {
	m.signatureVerificationDuration.Observe(seconds)
}

// KV metrics
func (m *Metrics) IncKVCacheHits() {
	m.kvCacheHits.Inc()
}

func (m *Metrics) IncKVCacheMisses() {
	m.kvCacheMisses.Inc()
}

func (m *Metrics) SetKVCacheSize(size float64) {
	m.kvCacheSize.Set(size)
}

// System metrics
func (m *Metrics) UpdateSystemMetrics() {
	m.goroutines.Set(float64(runtime.NumGoroutine()))

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	m.memoryBytes.Set(float64(memStats.Alloc))
}

// HTTP Inbound metrics (http-gateway only)
func (m *Metrics) IncHTTPInboundRequestsTotal(path, method, status string) {
	m.httpInboundRequestsTotal.WithLabelValues(path, method, status).Inc()
}

func (m *Metrics) ObserveHTTPRequestDuration(path, method string, seconds float64) {
	m.httpRequestDuration.WithLabelValues(path, method).Observe(seconds)
}

// HTTP Outbound metrics (http-gateway only)
func (m *Metrics) IncHTTPOutboundRequestsTotal(url, statusCode string) {
	m.httpOutboundRequestsTotal.WithLabelValues(url, statusCode).Inc()
}

func (m *Metrics) ObserveHTTPOutboundDuration(url string, seconds float64) {
	m.httpOutboundDuration.WithLabelValues(url).Observe(seconds)
}

// GetStats returns current statistics
func (m *Metrics) GetStats() (received, errors uint64) {
	return atomic.LoadUint64(&m.stats.messagesReceived),
		atomic.LoadUint64(&m.stats.messagesError)
}

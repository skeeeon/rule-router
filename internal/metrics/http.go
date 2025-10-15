// file: internal/metrics/http.go

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// HTTP-specific metrics for http-gateway

var (
	// HTTP Inbound metrics
	httpInboundRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_inbound_requests_total",
			Help: "Total number of HTTP inbound requests",
		},
		[]string{"path", "method", "status"},
	)
	
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
	
	// HTTP Outbound metrics
	httpOutboundRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_outbound_requests_total",
			Help: "Total number of HTTP outbound requests",
		},
		[]string{"url", "status_code"},
	)
	
	httpOutboundDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_outbound_duration_seconds",
			Help:    "HTTP outbound request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"url"},
	)
)

// RegisterHTTPMetrics registers HTTP-specific metrics
func (m *Metrics) RegisterHTTPMetrics() error {
	collectors := []prometheus.Collector{
		httpInboundRequestsTotal,
		httpRequestDuration,
		httpOutboundRequestsTotal,
		httpOutboundDuration,
	}
	
	for _, collector := range collectors {
		if err := m.registry.Register(collector); err != nil {
			return err
		}
	}
	
	return nil
}

// IncHTTPInboundRequestsTotal increments HTTP inbound request counter
func (m *Metrics) IncHTTPInboundRequestsTotal(path, method, status string) {
	httpInboundRequestsTotal.WithLabelValues(path, method, status).Inc()
}

// ObserveHTTPRequestDuration observes HTTP request duration
func (m *Metrics) ObserveHTTPRequestDuration(path, method string, duration float64) {
	httpRequestDuration.WithLabelValues(path, method).Observe(duration)
}

// IncHTTPOutboundRequestsTotal increments HTTP outbound request counter
func (m *Metrics) IncHTTPOutboundRequestsTotal(url, statusCode string) {
	httpOutboundRequestsTotal.WithLabelValues(url, statusCode).Inc()
}

// ObserveHTTPOutboundDuration observes HTTP outbound duration
func (m *Metrics) ObserveHTTPOutboundDuration(url string, duration float64) {
	httpOutboundDuration.WithLabelValues(url).Observe(duration)
}

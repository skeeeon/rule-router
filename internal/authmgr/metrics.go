// file: internal/authmgr/metrics.go

package authmgr

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics provides centralized metrics collection for the nats-auth-manager.
type Metrics struct {
	AuthSuccessTotal   *prometheus.CounterVec
	AuthFailuresTotal  *prometheus.CounterVec
	AuthDuration       *prometheus.HistogramVec
	KVStoreFailuresTotal *prometheus.CounterVec
}

// NewMetrics creates a new metrics instance and registers the collectors.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		AuthSuccessTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "authmgr_auth_success_total",
				Help: "Total number of successful authentications by provider.",
			},
			[]string{"provider"},
		),
		AuthFailuresTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "authmgr_auth_failures_total",
				Help: "Total number of failed authentications by provider.",
			},
			[]string{"provider"},
		),
		AuthDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "authmgr_auth_duration_seconds",
				Help:    "Duration of authentication requests by provider.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"provider"},
		),
		KVStoreFailuresTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "authmgr_kv_store_failures_total",
				Help: "Total number of failures to store a token in the KV bucket by provider.",
			},
			[]string{"provider"},
		),
	}

	if err := reg.Register(m.AuthSuccessTotal); err != nil {
		return nil, err
	}
	if err := reg.Register(m.AuthFailuresTotal); err != nil {
		return nil, err
	}
	if err := reg.Register(m.AuthDuration); err != nil {
		return nil, err
	}
	if err := reg.Register(m.KVStoreFailuresTotal); err != nil {
		return nil, err
	}

	return m, nil
}

// IncAuthSuccess increments the counter for successful authentications.
func (m *Metrics) IncAuthSuccess(providerID string) {
	m.AuthSuccessTotal.WithLabelValues(providerID).Inc()
}

// IncAuthFailure increments the counter for failed authentications.
func (m *Metrics) IncAuthFailure(providerID string) {
	m.AuthFailuresTotal.WithLabelValues(providerID).Inc()
}

// ObserveAuthDuration records the duration of an authentication attempt.
func (m *Metrics) ObserveAuthDuration(providerID string, seconds float64) {
	m.AuthDuration.WithLabelValues(providerID).Observe(seconds)
}

// IncKVStoreFailure increments the counter for KV store failures.
func (m *Metrics) IncKVStoreFailure(providerID string) {
	m.KVStoreFailuresTotal.WithLabelValues(providerID).Inc()
}

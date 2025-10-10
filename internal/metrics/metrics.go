//file: internal/metrics/metrics.go

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all prometheus metrics for the application
type Metrics struct {
	// Message metrics
	messagesTotal     *prometheus.CounterVec
	messageQueueDepth prometheus.Gauge
	processingBacklog prometheus.Gauge

	// Rule metrics
	ruleMatchesTotal prometheus.Counter
	rulesActive      prometheus.Gauge

	// NATS metrics
	natsConnectionStatus prometheus.Gauge
	natsReconnectsTotal  prometheus.Counter

	// Action metrics
	actionsTotal          *prometheus.CounterVec
	actionPublishFailures prometheus.Counter
	actionsByType         *prometheus.CounterVec // NEW

	// Template metrics
	templateOpsTotal *prometheus.CounterVec

	// System metrics
	processGoroutines  prometheus.Gauge
	processMemoryBytes prometheus.Gauge
}

// NewMetrics creates and registers all prometheus metrics
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		messagesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "messages_total",
				Help: "Total number of messages processed by status",
			},
			[]string{"status"},
		),
		messageQueueDepth: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "message_queue_depth",
				Help: "Current number of messages in the processing queue",
			},
		),
		processingBacklog: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "message_processing_backlog",
				Help: "Difference between received and processed messages",
			},
		),
		ruleMatchesTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rule_matches_total",
				Help: "Total number of rule matches",
			},
		),
		rulesActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rules_active",
				Help: "Current number of active rules",
			},
		),
		natsConnectionStatus: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "nats_connection_status",
				Help: "Current NATS connection status (0/1)",
			},
		),
		natsReconnectsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "nats_reconnects_total",
				Help: "Total number of NATS reconnection attempts",
			},
		),
		actionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "actions_total",
				Help: "Total number of rule actions executed by status",
			},
			[]string{"status"},
		),
		actionPublishFailures: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "action_publish_failures_total",
				Help: "Total number of action publish failures (before retry)",
			},
		),
		// NEW: Counter for action types
		actionsByType: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "actions_by_type_total",
				Help: "Total actions by type (templated vs passthrough)",
			},
			[]string{"type"},
		),
		templateOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "template_operations_total",
				Help: "Total number of template operations by status",
			},
			[]string{"status"},
		),
		processGoroutines: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "process_goroutines",
				Help: "Current number of goroutines",
			},
		),
		processMemoryBytes: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "process_memory_bytes",
				Help: "Current memory usage in bytes",
			},
		),
	}

	// Register all metrics
	metrics := []prometheus.Collector{
		m.messagesTotal,
		m.messageQueueDepth,
		m.processingBacklog,
		m.ruleMatchesTotal,
		m.rulesActive,
		m.natsConnectionStatus,
		m.natsReconnectsTotal,
		m.actionsTotal,
		m.actionPublishFailures,
		m.actionsByType, // NEW
		m.templateOpsTotal,
		m.processGoroutines,
		m.processMemoryBytes,
	}

	for _, metric := range metrics {
		if err := reg.Register(metric); err != nil {
			return nil, err
		}
	}

	return m, nil
}

// IncMessagesTotal increments the messages counter for a given status
func (m *Metrics) IncMessagesTotal(status string) {
	m.messagesTotal.WithLabelValues(status).Inc()
}

// SetMessageQueueDepth sets the current message queue depth
func (m *Metrics) SetMessageQueueDepth(depth float64) {
	m.messageQueueDepth.Set(depth)
}

// SetProcessingBacklog sets the current processing backlog
func (m *Metrics) SetProcessingBacklog(backlog float64) {
	m.processingBacklog.Set(backlog)
}

// IncRuleMatches increments the rule matches counter
func (m *Metrics) IncRuleMatches() {
	m.ruleMatchesTotal.Inc()
}

// SetRulesActive sets the number of active rules
func (m *Metrics) SetRulesActive(count float64) {
	m.rulesActive.Set(count)
}

// SetNATSConnectionStatus sets the NATS connection status
func (m *Metrics) SetNATSConnectionStatus(connected bool) {
	if connected {
		m.natsConnectionStatus.Set(1)
	} else {
		m.natsConnectionStatus.Set(0)
	}
}

// IncNATSReconnects increments the NATS reconnects counter
func (m *Metrics) IncNATSReconnects() {
	m.natsReconnectsTotal.Inc()
}

// IncActionsTotal increments the actions counter for a given status
func (m *Metrics) IncActionsTotal(status string) {
	m.actionsTotal.WithLabelValues(status).Inc()
}

// IncActionPublishFailures increments the action publish failures counter
func (m *Metrics) IncActionPublishFailures() {
	m.actionPublishFailures.Inc()
}

// NEW: IncActionsByType increments the action type counter
func (m *Metrics) IncActionsByType(actionType string) {
    m.actionsByType.WithLabelValues(actionType).Inc()
}

// IncTemplateOpsTotal increments the template operations counter for a given status
func (m *Metrics) IncTemplateOpsTotal(status string) {
	m.templateOpsTotal.WithLabelValues(status).Inc()
}

// SetProcessMetrics sets the current process metrics
func (m *Metrics) SetProcessMetrics(goroutines, memoryBytes float64) {
	m.processGoroutines.Set(goroutines)
	m.processMemoryBytes.Set(memoryBytes)
}

// Legacy compatibility methods for MQTT metrics (now mapping to NATS)

// SetMQTTConnectionStatus sets the NATS connection status (legacy method)
func (m *Metrics) SetMQTTConnectionStatus(connected bool) {
	m.SetNATSConnectionStatus(connected)
}

// IncMQTTReconnects increments the NATS reconnects counter (legacy method)
func (m *Metrics) IncMQTTReconnects() {
	m.IncNATSReconnects()
}

// SetWorkerPoolActive is now a no-op since we removed worker pools
func (m *Metrics) SetWorkerPoolActive(count float64) {
	// No-op - worker pool metrics removed
}

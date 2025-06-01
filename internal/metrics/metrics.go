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

	// MQTT metrics
	mqttConnectionStatus prometheus.Gauge
	mqttReconnectsTotal prometheus.Counter

	// Action metrics
	actionsTotal *prometheus.CounterVec

	// Template metrics
	templateOpsTotal *prometheus.CounterVec

	// System metrics
	processGoroutines  prometheus.Gauge
	processMemoryBytes prometheus.Gauge
	workerPoolActive   prometheus.Gauge
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
		mqttConnectionStatus: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "mqtt_connection_status",
				Help: "Current MQTT connection status (0/1)",
			},
		),
		mqttReconnectsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "mqtt_reconnects_total",
				Help: "Total number of MQTT reconnection attempts",
			},
		),
		actionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "actions_total",
				Help: "Total number of rule actions executed by status",
			},
			[]string{"status"},
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
		workerPoolActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "worker_pool_active",
				Help: "Current number of active workers",
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
		m.mqttConnectionStatus,
		m.mqttReconnectsTotal,
		m.actionsTotal,
		m.templateOpsTotal,
		m.processGoroutines,
		m.processMemoryBytes,
		m.workerPoolActive,
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

// SetMQTTConnectionStatus sets the MQTT connection status
func (m *Metrics) SetMQTTConnectionStatus(connected bool) {
	if connected {
		m.mqttConnectionStatus.Set(1)
	} else {
		m.mqttConnectionStatus.Set(0)
	}
}

// IncMQTTReconnects increments the MQTT reconnects counter
func (m *Metrics) IncMQTTReconnects() {
	m.mqttReconnectsTotal.Inc()
}

// IncActionsTotal increments the actions counter for a given status
func (m *Metrics) IncActionsTotal(status string) {
	m.actionsTotal.WithLabelValues(status).Inc()
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

// SetWorkerPoolActive sets the number of active workers
func (m *Metrics) SetWorkerPoolActive(count float64) {
	m.workerPoolActive.Set(count)
}

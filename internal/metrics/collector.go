// file: internal/metrics/collector.go

package metrics

import (
	"sync"
	"time"
)

// MetricsCollector handles periodic collection of system metrics
type MetricsCollector struct {
	metrics        *Metrics
	updateInterval time.Duration
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(metrics *Metrics, updateInterval time.Duration) *MetricsCollector {
	return &MetricsCollector{
		metrics:        metrics,
		updateInterval: updateInterval,
		stopChan:       make(chan struct{}),
	}
}

// Start begins periodic collection of system metrics
func (mc *MetricsCollector) Start() {
	mc.wg.Add(1)
	go mc.collect()
}

// Stop gracefully shuts down the metrics collector
func (mc *MetricsCollector) Stop() {
	close(mc.stopChan)
	mc.wg.Wait()
}

// collect periodically updates system metrics
func (mc *MetricsCollector) collect() {
	defer mc.wg.Done()

	ticker := time.NewTicker(mc.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-mc.stopChan:
			return
		case <-ticker.C:
			// Update system metrics (goroutines, memory)
			mc.metrics.UpdateSystemMetrics()
		}
	}
}

//go:build !js

package metrics

import (
	"sync"
	"time"
)

// Collector handles periodic collection of system metrics
type Collector struct {
	metrics        *Metrics
	updateInterval time.Duration
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

// NewCollector creates a new metrics collector
func NewCollector(metrics *Metrics, updateInterval time.Duration) *Collector {
	return &Collector{
		metrics:        metrics,
		updateInterval: updateInterval,
		stopChan:       make(chan struct{}),
	}
}

// Start begins periodic collection of system metrics
func (mc *Collector) Start() {
	mc.wg.Add(1)
	go mc.collect()
}

// Stop gracefully shuts down the metrics collector
func (mc *Collector) Stop() {
	close(mc.stopChan)
	mc.wg.Wait()
}

// collect periodically updates system metrics
func (mc *Collector) collect() {
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

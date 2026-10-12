package broker

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"rule-router/internal/metrics"
)

// gaugeValue reads a gauge's value from a Prometheus registry. Returns 0 if the
// family is absent.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			for _, m := range f.GetMetric() {
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

// TestConnectionStatusSetOnInitialConnect pins that nats_connection_status
// reads 1 once the first connect succeeds. It used to be set only by the
// disconnect, reconnect and closed handlers, so a healthy instance reported 0
// until its first reconnect.
func TestConnectionStatusSetOnInitialConnect(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := metrics.NewMetrics(reg)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	b := newTestBroker()
	b.metrics = m

	natsOpts, err := b.buildNATSOptions()
	if err != nil {
		t.Fatalf("buildNATSOptions: %v", err)
	}
	var opts nats.Options
	for _, opt := range natsOpts {
		if err := opt(&opts); err != nil {
			t.Fatalf("applying NATS option: %v", err)
		}
	}

	if opts.ConnectedCB == nil {
		t.Fatal("no connect handler: nothing sets nats_connection_status on the initial connect")
	}
	opts.ConnectedCB(nil)
	if got := gaugeValue(t, reg, "nats_connection_status"); got != 1 {
		t.Fatalf("nats_connection_status after connect = %v, want 1", got)
	}

	opts.DisconnectedErrCB(nil, nil)
	if got := gaugeValue(t, reg, "nats_connection_status"); got != 0 {
		t.Fatalf("nats_connection_status after disconnect = %v, want 0", got)
	}
}

package broker

import (
	"strings"

	"rule-router/internal/natsservice"
)

// serviceName is what every rule-router process registers as on NATS micro
// discovery. A constant, because a shared name is what lets one
// $SRV.PING.rule-router find every instance.
const serviceName = "rule-router"

// RegisterService announces this process on the NATS micro discovery protocol
// ($SRV.PING, $SRV.INFO, $SRV.STATS): name, version, host, enabled features and
// start time. It adds no endpoints — identity only — so micro answers all three
// verbs by itself.
//
// Never fatal: discovery must not be a dependency of routing, so a failure is
// logged and the process carries on. A credential refused $SRV.> fails
// asynchronously instead, through handleAsyncError. Close stops the service.
func (b *NATSBroker) RegisterService(version string, features []string) {
	svc, err := natsservice.Add(b.natsConn, serviceName, version,
		"Rule-based message routing for NATS",
		map[string]string{"features": strings.Join(features, ",")})
	if err != nil {
		b.logger.Warn("NATS service discovery registration failed, continuing without it", "error", err)
		return
	}
	b.service = svc
}

package broker

import (
	"os"
	"regexp"
	"strings"

	"github.com/nats-io/nats.go/micro"
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
	metadata := map[string]string{"features": strings.Join(features, ",")}
	if host, err := os.Hostname(); err == nil {
		metadata["host"] = host
	}

	svc, err := micro.AddService(b.natsConn, micro.Config{
		Name:        serviceName,
		Version:     serviceVersion(version),
		Description: "Rule-based message routing for NATS",
		Metadata:    metadata,
	})
	if err != nil {
		b.logger.Warn("NATS service discovery registration failed, continuing without it", "error", err)
		return
	}
	b.service = svc
}

// semverPattern is the regular expression semver.org publishes, which is also
// the one micro validates a service version against.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// serviceVersion is the build version in the form micro accepts: a leading
// "v" is dropped, and anything still not semver reports as 0.0.0-dev. Builds
// without -ldflags stamp "dev", which AddService would otherwise refuse.
func serviceVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if semverPattern.MatchString(v) {
		return v
	}
	return "0.0.0-dev"
}

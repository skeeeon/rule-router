// Package natsservice registers a process on the NATS micro discovery protocol
// ($SRV.PING, $SRV.INFO, $SRV.STATS). It is shared by rule-router and
// nats-auth-manager so both report identity the same way: name, semver
// version, host and start time, with no endpoints.
package natsservice

import (
	"os"
	"regexp"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// Add registers an identity-only micro service on nc. metadata may be nil;
// "host" is added to it when the hostname is known. Callers treat an error as
// a warning: discovery must never be a dependency of the process's real work.
// A credential refused $SRV.> does not fail here but asynchronously, through
// the connection's error handler, with a nil subscription.
func Add(nc *nats.Conn, name, version, description string, metadata map[string]string) (micro.Service, error) {
	md := make(map[string]string, len(metadata)+1)
	for k, v := range metadata {
		md[k] = v
	}
	if host, err := os.Hostname(); err == nil {
		md["host"] = host
	}

	return micro.AddService(nc, micro.Config{
		Name:        name,
		Version:     Version(version),
		Description: description,
		Metadata:    md,
	})
}

// semverPattern is the regular expression semver.org publishes, which is also
// the one micro validates a service version against.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// Version is the build version in the form micro accepts: a leading "v" is
// dropped, and anything still not semver reports as 0.0.0-dev. Builds without
// -ldflags stamp "dev", which AddService would otherwise refuse.
func Version(v string) string {
	v = strings.TrimPrefix(v, "v")
	if semverPattern.MatchString(v) {
		return v
	}
	return "0.0.0-dev"
}

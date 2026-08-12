// Package metrics defines the Prometheus instrumentation for rule-router:
// message, rule-match, action, forEach, throttle, and HMAC counters plus
// latency histograms, exposed on the configurable metrics listener.
//
// Under GOOS=js every method is a no-op stub, so instrumented code needs no
// build tags of its own. A nil *Metrics is not safe to call; callers that may
// run without metrics guard on nil, which is the convention throughout the
// codebase.
package metrics

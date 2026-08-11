// file: internal/broker/action_streams.go

package broker

import (
	"strings"

	"rule-router/internal/rule"
)

// maxWarnedSubjects caps how many offending subjects are named in the
// unstreamed-action warning; the rest are reported as a count.
const maxWarnedSubjects = 20

// WarnUnstreamedActionSubjects logs one warning naming the NATS action subjects
// in these rules that will be published over JetStream to a subject no
// discovered stream captures.
//
// Such a publish is never acked: it burns the full ackTimeout and fails. In the
// router that failure NAKs the trigger message, so the rule redelivers and
// retries until MaxDeliver dead-letters it; in the gateway the webhook was
// already answered 200, so the payload is silently lost; on a cron it simply
// fails every fire, forever. Almost always the action meant `mode: core`.
//
// Trigger subjects are validated strictly elsewhere (a consumer genuinely cannot
// be created without a stream). Action subjects only warn: a stream created
// after startup is legitimate, and RefreshStreams picks it up.
//
// source labels where the rules came from — a rules directory, or a KV key.
func (b *NATSBroker) WarnUnstreamedActionSubjects(rules []*rule.Rule, source string) {
	if b == nil || b.streamResolver == nil {
		return
	}

	unstreamed := b.streamResolver.UnstreamedSubjects(
		jetStreamActionSubjects(rules, b.config.NATS.Publish.Mode),
	)
	if len(unstreamed) == 0 {
		return
	}

	shown := unstreamed
	if len(shown) > maxWarnedSubjects {
		shown = shown[:maxWarnedSubjects]
	}
	b.logger.Warn("rules publish to JetStream subjects with no matching stream; "+
		"these publishes will time out waiting for an ack and then fail — "+
		"add a stream covering them or set 'mode: core' on the action",
		"source", source,
		"subjects", len(unstreamed),
		"examples", shown)
}

// jetStreamActionSubjects returns the deduped, statically known subjects these
// rules publish over JetStream, given the global publish mode.
//
// Skipped, because none of them is a stream publish that can be checked here:
// actions whose effective mode is core; `request: true` actions (core
// request/reply, no stream involved); and templated subjects, which are known
// only per evaluation.
func jetStreamActionSubjects(rules []*rule.Rule, globalMode string) []string {
	isCore := func(actionMode string) bool {
		if actionMode == "" {
			return globalMode == rule.ModeCore
		}
		return actionMode == rule.ModeCore
	}

	seen := make(map[string]struct{})
	subjects := make([]string, 0)

	add := func(subject string) {
		if subject == "" || strings.Contains(subject, "{") {
			return
		}
		if _, ok := seen[subject]; ok {
			return
		}
		seen[subject] = struct{}{}
		subjects = append(subjects, subject)
	}

	for _, r := range rules {
		if r == nil {
			continue
		}
		if action := r.Action.NATS; action != nil && !action.Request && !isCore(action.Mode) {
			add(action.Subject)
		}
		// An HTTP action's publishResponse always uses the global publish mode —
		// it carries no per-action override.
		if action := r.Action.HTTP; action != nil && action.PublishResponse != nil && !isCore("") {
			add(action.PublishResponse.Subject)
		}
	}

	return subjects
}

// UnstreamedSubjects returns the subset of subjects that no discovered stream
// captures, preserving input order.
//
// It reports nothing when no streams are known — either discovery has not run or
// the connection's credentials cannot see any. Warning about every subject in
// that case would be noise about a permissions or topology problem, not about
// the rules.
func (sr *StreamResolver) UnstreamedSubjects(subjects []string) []string {
	sr.mu.RLock()
	discovered := sr.discovered
	streamCount := len(sr.streams)
	sr.mu.RUnlock()

	if !discovered || streamCount == 0 {
		return nil
	}

	var unstreamed []string
	for _, subject := range subjects {
		if _, err := sr.FindStreamForSubject(subject); err != nil {
			unstreamed = append(unstreamed, subject)
		}
	}
	return unstreamed
}

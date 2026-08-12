// Package rule implements the rule engine: it loads YAML rule definitions,
// indexes them by trigger, and evaluates incoming messages against them.
//
// The entry point is Processor. Each Process method returns an Outcome holding
// the actions to run now and the actions a trailing throttle is holding; the
// package never performs a side effect itself, so executing an Outcome is
// always the caller's job. Loader parses and validates rules, Index maps NATS
// subjects and HTTP paths to them, and Evaluator resolves conditions.
//
// Templates resolve message fields ({field}, {data.device.id}), subject tokens
// ({@subject.0}), KV lookups ({@kv.bucket.key}), and system functions
// ({@timestamp()}, {@uuid7()}). Values keep their JSON types so numeric
// comparisons stay numeric.
//
// Processor is safe for concurrent use. KV-loaded rules are swapped atomically
// by ReplaceKVRuleSet, so a reload is invisible to in-flight evaluation.
package rule

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

const (
	// DefaultMaxForEachIterations is the default limit for forEach array processing.
	// Prevents runaway iteration on large arrays. Can be overridden via configuration.
	DefaultMaxForEachIterations = 100
)

type Processor struct {
	index            *Index
	allRules         []*Rule
	scheduleRules    []*Rule
	httpExactPaths   map[string][]*Rule // file-loaded HTTP rules keyed by exact path
	httpPatternRules []*HTTPPatternRule // file-loaded HTTP rules with wildcard paths
	timeProvider     TimeProvider
	kvContext        *KVContext
	logger           *logger.Logger
	metrics          *metrics.Metrics
	stats            ProcessorStats
	evaluator        *Evaluator
	templater        *TemplateEngine
	sigVerification  *SignatureVerification
	maxForEachIters  int // Configurable forEach iteration limit
	throttle         *ThrottleManager

	// KV-based rule storage: a single atomic.Value holds the whole rule set so
	// reloads swap NATS, HTTP, and Schedule rules in one operation. Three
	// separate Stores (the old layout) left a window where readers could see
	// new NATS rules but old HTTP rules (or vice versa) during reload.
	kvRuleSet atomic.Value // holds *KVRuleSet
}

// KVRuleSet is the unit of atomic rule reload: NATS, HTTP, and Schedule rules
// bundled together so a single Store updates all three coherently.
type KVRuleSet struct {
	NATS     map[string][]*Rule // NATS subject → rules
	HTTP     *HTTPKVRuleSet     // exact paths + pattern rules (non-nil)
	Schedule []*Rule            // schedule-triggered rules
}

// HTTPPatternRule pairs an HTTP-triggered rule with its compiled path matcher.
type HTTPPatternRule struct {
	Rule    *Rule
	Matcher *PatternMatcher
}

// HTTPKVRuleSet holds KV-loaded HTTP rules bucketed by exact path and pattern.
// Carried inside *KVRuleSet; Exact is always non-nil after ReplaceKVRuleSet.
type HTTPKVRuleSet struct {
	Exact    map[string][]*Rule
	Patterns []*HTTPPatternRule
}

type ProcessorStats struct {
	Processed uint64
	Matched   uint64
	Errors    uint64
}

// ForEachResult tracks detailed forEach processing statistics
type ForEachResult struct {
	TotalElements     int
	ProcessedElements int
	FilteredElements  int
	FailedElements    int
	Errors            []ElementError
}

// ElementError tracks individual element processing failures
type ElementError struct {
	Index     int
	ErrorType string
	Err       error
}

// forEachArrayData holds the extracted array and metadata for forEach processing
type forEachArrayData struct {
	items     []any
	fieldPath string
}

// forEachActionConfig holds the common forEach fields from either action type.
type forEachActionConfig struct {
	ForEach     string
	forEachPath []string
	Filter      *Conditions
	Passthrough bool
	Merge       bool
	Payload     string
	Headers     map[string]string
	MetricsType string // "nats" or "http"
}

// resolvedPayload is the result of resolving an action's payload config
// (passthrough, merge, or templated) into concrete bytes or text.
type resolvedPayload struct {
	Text        string // set in template mode
	Raw         []byte // set in passthrough or merge mode
	Passthrough bool   // true means use Raw
}

// forEachElementError lets callbacks report typed errors for metrics.
type forEachElementError struct {
	ErrorType string
	Err       error
}

func (e *forEachElementError) Error() string { return e.Err.Error() }
func (e *forEachElementError) Unwrap() error { return e.Err }

// extractForEachArray extracts and validates the array for forEach processing.
// This is shared logic between NATS and HTTP forEach handlers.
// Supports both message field paths (e.g., "{data.readings}") and system field
// references (e.g., "{@kv.config.door_list}") for KV-sourced fan-out patterns.
func (p *Processor) extractForEachArray(forEachTemplate string, precomputedPath []string, context *EvaluationContext) (*forEachArrayData, error) {
	// Extract the field path from template braces using shared utility
	// forEach: "{data.readings}" → "data.readings"
	// forEach: "{@kv.config.doors}" → "@kv.config.doors"
	fieldPath := ExtractVariable(forEachTemplate)
	if fieldPath == "" {
		return nil, fmt.Errorf("invalid forEach template syntax (must be {field}): %s", forEachTemplate)
	}

	p.logger.Debug("extracted forEach field path",
		"template", forEachTemplate,
		"extractedPath", fieldPath)

	// Resolve the array value — system fields (@kv, etc.) go through ResolveValue,
	// regular message fields use direct path traversal.
	var arrayValue any
	if strings.HasPrefix(fieldPath, "@") {
		resolved, found := context.ResolveValue(fieldPath)
		if !found {
			return nil, fmt.Errorf("forEach system field not found: %s", forEachTemplate)
		}
		arrayValue = resolved
	} else {
		// Use pre-computed path when available, fall back to runtime split
		arrayPath := precomputedPath
		if arrayPath == nil {
			var err error
			arrayPath, err = SplitPathRespectingBraces(fieldPath)
			if err != nil {
				return nil, fmt.Errorf("invalid forEach path %q: %w", fieldPath, err)
			}
		}
		var traverseErr error
		arrayValue, traverseErr = context.traverser.TraversePath(context.Msg, arrayPath)
		if traverseErr != nil {
			return nil, fmt.Errorf("forEach field not found: %s: %w", forEachTemplate, traverseErr)
		}
	}

	arrayItems, ok := arrayValue.([]any)
	if !ok {
		return nil, fmt.Errorf("forEach field is not an array: %s (type: %T)", forEachTemplate, arrayValue)
	}

	// Enforce iteration limit
	if p.maxForEachIters > 0 && len(arrayItems) > p.maxForEachIters {
		return nil, fmt.Errorf("forEach array exceeds limit: %d elements > %d max iterations (configure maxForEachIterations to increase)",
			len(arrayItems), p.maxForEachIters)
	}

	p.logger.Debug("forEach array extracted",
		"field", forEachTemplate,
		"arrayLength", len(arrayItems),
		"maxIterations", p.maxForEachIters)

	return &forEachArrayData{
		items:     arrayItems,
		fieldPath: fieldPath,
	}, nil
}

// An Option configures a Processor at construction.
//
// These are options rather than setters because a Processor becomes shared
// across worker goroutines the moment it starts serving, so everything it needs
// has to be in place before that. Most callers need none of them: a bare
// NewProcessor(log) evaluates rules with no KV, metrics, or signature checking.
type Option func(*Processor)

// WithMetrics attaches a metrics sink. A nil sink is accepted and disables
// instrumentation, which is what the CLI and the browser tester use.
func WithMetrics(m *metrics.Metrics) Option {
	return func(p *Processor) { p.metrics = m }
}

// WithKVContext gives rules access to NATS KV lookups ({@kv.bucket.key}).
// Without it those templates resolve to nothing.
func WithKVContext(kv *KVContext) Option {
	return func(p *Processor) { p.kvContext = kv }
}

// WithSignatureVerification enables nkey payload signature checking on inbound
// messages.
func WithSignatureVerification(sv *SignatureVerification) Option {
	return func(p *Processor) { p.sigVerification = sv }
}

// WithMaxForEachIterations bounds how many elements a forEach action may expand,
// guarding against runaway fan-out. Zero means unlimited; the default is
// DefaultMaxForEachIterations.
func WithMaxForEachIterations(n int) Option {
	return func(p *Processor) { p.maxForEachIters = n }
}

// WithTimeProvider replaces the system clock backing {@time.*} and {@date.*}.
// Intended for tests that need a fixed instant.
func WithTimeProvider(tp TimeProvider) Option {
	return func(p *Processor) { p.timeProvider = tp }
}

// NewProcessor creates a Processor. See Option for KV, metrics, signature
// verification, and forEach limits.
func NewProcessor(log *logger.Logger, opts ...Option) *Processor {
	p := &Processor{
		index:            NewIndex(log),
		allRules:         make([]*Rule, 0),
		httpExactPaths:   make(map[string][]*Rule),
		httpPatternRules: make([]*HTTPPatternRule, 0),
		timeProvider:     NewSystemTimeProvider(),
		logger:           log.With("component", "processor"),
		evaluator:        NewEvaluator(log),
		templater:        NewTemplateEngine(log),
		maxForEachIters:  DefaultMaxForEachIterations,
		throttle:         NewThrottleManager(),
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.kvContext != nil {
		p.logger.Info("initializing processor with KV support", "buckets", p.kvContext.Buckets())
	} else {
		p.logger.Info("initializing processor without KV support")
	}

	if p.sigVerification != nil && p.sigVerification.Enabled {
		p.logger.Info("initializing processor with signature verification enabled",
			"pubKeyHeader", p.sigVerification.PublicKeyHeader,
			"sigHeader", p.sigVerification.SignatureHeader)
	}

	p.logger.Debug("forEach iteration limit configured", "maxIterations", p.maxForEachIters)

	return p
}

// LoadRules loads rules and indexes them by trigger type.
//
// It fails if any rule declares an HTTP path pattern that will not compile.
// Rules that came through Loader were already checked by ValidatePathPattern,
// so in practice this only catches rules built programmatically — the CLI, the
// browser tester, tests — where silently dropping one would hide the mistake.
//
// On failure the Processor is left partially indexed and must be discarded.
// That costs nothing today: every caller loads into a Processor it just built,
// and rule reloads go through a fresh app (SIGHUP) or ReplaceKVRuleSet (KV).
func (p *Processor) LoadRules(rules []Rule) error {
	p.logger.Info("loading rules into processor", "ruleCount", len(rules))

	p.index.Clear()
	p.allRules = make([]*Rule, 0, len(rules))
	p.scheduleRules = make([]*Rule, 0)
	p.httpExactPaths = make(map[string][]*Rule)
	p.httpPatternRules = make([]*HTTPPatternRule, 0)

	natsCount := 0
	httpCount := 0
	httpPatternCount := 0
	scheduleCount := 0

	for i := range rules {
		rule := &rules[i]
		rule.index = i
		p.allRules = append(p.allRules, rule)

		// Pre-compute condition paths for hot-path optimization
		if rule.Conditions != nil {
			PrepareConditions(rule.Conditions)
		}
		if rule.Action.NATS != nil && rule.Action.NATS.Filter != nil {
			PrepareConditions(rule.Action.NATS.Filter)
		}
		if rule.Action.HTTP != nil && rule.Action.HTTP.Filter != nil {
			PrepareConditions(rule.Action.HTTP.Filter)
		}

		// Pre-compute forEach paths
		if rule.Action.NATS != nil && rule.Action.NATS.ForEach != "" {
			rule.Action.NATS.forEachPath = prepareForEachPath(rule.Action.NATS.ForEach)
		}
		if rule.Action.HTTP != nil && rule.Action.HTTP.ForEach != "" {
			rule.Action.HTTP.forEachPath = prepareForEachPath(rule.Action.HTTP.ForEach)
		}

		if rule.Trigger.NATS != nil {
			p.index.Add(rule)
			natsCount++
		}

		if rule.Trigger.HTTP != nil {
			path := rule.Trigger.HTTP.Path
			httpCount++

			if PathContainsWildcards(path) {
				matcher, err := NewPathMatcher(path)
				if err != nil {
					return fmt.Errorf("rule %d: invalid HTTP path pattern %q: %w", i, path, err)
				}
				p.httpPatternRules = append(p.httpPatternRules, &HTTPPatternRule{
					Rule:    rule,
					Matcher: matcher,
				})
				httpPatternCount++
				p.logger.Debug("indexed HTTP wildcard rule",
					"pattern", path,
					"method", rule.Trigger.HTTP.Method)
			} else {
				p.httpExactPaths[path] = append(p.httpExactPaths[path], rule)
				p.logger.Debug("indexed HTTP rule",
					"path", path,
					"method", rule.Trigger.HTTP.Method)
			}
		}

		if rule.Trigger.Schedule != nil {
			p.scheduleRules = append(p.scheduleRules, rule)
			scheduleCount++

			p.logger.Debug("indexed schedule rule",
				"cron", rule.Trigger.Schedule.Cron,
				"timezone", rule.Trigger.Schedule.Timezone)
		}
	}

	if p.metrics != nil {
		p.metrics.SetRulesActive(float64(natsCount + httpCount + scheduleCount))
	}

	p.logger.Info("rules loaded",
		"total", len(rules),
		"natsRules", natsCount,
		"httpRules", httpCount,
		"httpPatternRules", httpPatternCount,
		"scheduleRules", scheduleCount,
		"httpExactPaths", len(p.httpExactPaths))

	return nil
}

// Subjects returns all NATS subjects for subscription setup
func (p *Processor) Subjects() []string {
	return p.index.SubscriptionSubjects()
}

// HTTPPaths returns all configured HTTP paths and patterns for diagnostic
// logging. Pattern strings (e.g. "/webhooks/*/events") are returned as-is,
// not expanded.
func (p *Processor) HTTPPaths() []string {
	pathSet := make(map[string]bool)
	for path := range p.httpExactPaths {
		pathSet[path] = true
	}
	for _, pr := range p.httpPatternRules {
		pathSet[pr.Rule.Trigger.HTTP.Path] = true
	}
	if set := p.loadKVRuleSet(); set != nil {
		for path := range set.HTTP.Exact {
			pathSet[path] = true
		}
		for _, pr := range set.HTTP.Patterns {
			pathSet[pr.Rule.Trigger.HTTP.Path] = true
		}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	return paths
}

// MatchHTTPPath returns the rule-declared route that matches the given request
// path, and whether anything matched at all. Exact-path rules report the path
// itself; wildcard rules report the pattern string ("/webhooks/*/events") rather
// than the concrete request path.
//
// The inbound gateway uses the returned route as the Prometheus `path` label:
// labelling with the raw request path would mint one label value per distinct
// URL under a wildcard route (e.g. every /internal-api/> request), which is
// unbounded series growth. The pattern is bounded by rule count.
//
// Checks exact-path maps first (O(1)), then walks wildcard patterns, so the
// most specific route wins. When several wildcard rules match, the first in
// load order names the label — all matching rules still fire, and per-rule
// attribution lives in rule_matches_total.
func (p *Processor) MatchHTTPPath(path string) (string, bool) {
	if len(p.httpExactPaths[path]) > 0 {
		return path, true
	}
	for _, pr := range p.httpPatternRules {
		if MatchPath(pr.Matcher, path) {
			return pr.Rule.Trigger.HTTP.Path, true
		}
	}
	if set := p.loadKVRuleSet(); set != nil {
		if len(set.HTTP.Exact[path]) > 0 {
			return path, true
		}
		for _, pr := range set.HTTP.Patterns {
			if MatchPath(pr.Matcher, path) {
				return pr.Rule.Trigger.HTTP.Path, true
			}
		}
	}
	return "", false
}

// HasHTTPPath returns true if any loaded rule's HTTP trigger matches the given
// request path. Used by the inbound gateway to early-reject unknown paths.
func (p *Processor) HasHTTPPath(path string) bool {
	_, ok := p.MatchHTTPPath(path)
	return ok
}

// HasSyncHTTPPath returns true if any rule matching the path+method produces a
// synchronous HTTP response — i.e. it has a respond action or a NATS action with
// request:true (the HTTP↔NATS bridge). The inbound gateway uses this to route a
// request to the inline/synchronous handler instead of the fire-and-forget queue.
func (p *Processor) HasSyncHTTPPath(path, method string) bool {
	for _, r := range p.findHTTPRules(path, method) {
		if r.Action.Respond != nil {
			return true
		}
		if r.Action.NATS != nil && r.Action.NATS.Request {
			return true
		}
	}
	return false
}

// loadKVRuleSet returns the current KV-loaded rule set, or nil if no KV rules
// have been loaded yet. Lock-free; safe to call from any goroutine.
func (p *Processor) loadKVRuleSet() *KVRuleSet {
	set, _ := p.kvRuleSet.Load().(*KVRuleSet)
	return set
}

// AllRules returns all loaded rules (NATS, HTTP, and schedule)
func (p *Processor) AllRules() []*Rule {
	return p.allRules
}

// filterNATSRules returns the rules whose NATS trigger passes the filter.
// A nil filter passes everything; the all-pass case returns the input slice
// unchanged to avoid allocating on the hot path.
func filterNATSRules(rules []*Rule, filter NATSTriggerFilter) []*Rule {
	if filter == nil {
		return rules
	}
	pass := 0
	for _, r := range rules {
		if r.Trigger.NATS != nil && filter(r.Trigger.NATS) {
			pass++
		}
	}
	if pass == len(rules) {
		return rules
	}
	filtered := make([]*Rule, 0, pass)
	for _, r := range rules {
		if r.Trigger.NATS != nil && filter(r.Trigger.NATS) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// ScheduleRules returns all schedule-triggered rules.
// Returns KV-loaded schedule rules when set (even if empty); falls back to file-loaded rules.
func (p *Processor) ScheduleRules() []*Rule {
	if set := p.loadKVRuleSet(); set != nil {
		return set.Schedule
	}
	return p.scheduleRules
}

// ReplaceKVRuleSet atomically swaps the entire KV-loaded rule set (NATS, HTTP,
// and Schedule) in one operation. Called by RuleKVManager on every KV change.
// Caller must pass a non-nil *KVRuleSet with a non-nil HTTP field.
func (p *Processor) ReplaceKVRuleSet(set *KVRuleSet) {
	// Assign each rule a unique index so throttle/debounce keys
	// ({phase}:{index}:{key}) can't collide across KV-loaded rules. The
	// file-load path does this in LoadRules; KV rules need it here. Each *Rule
	// lives in exactly one bucket (one trigger type), so a single counter suffices.
	idx := 0
	for _, rs := range set.NATS {
		for _, r := range rs {
			r.index = idx
			idx++
		}
	}
	for _, rs := range set.HTTP.Exact {
		for _, r := range rs {
			r.index = idx
			idx++
		}
	}
	for _, pr := range set.HTTP.Patterns {
		pr.Rule.index = idx
		idx++
	}
	for _, r := range set.Schedule {
		r.index = idx
		idx++
	}

	p.kvRuleSet.Store(set)

	natsTotal := 0
	for _, rs := range set.NATS {
		natsTotal += len(rs)
	}
	httpTotal := len(set.HTTP.Patterns)
	for _, rs := range set.HTTP.Exact {
		httpTotal += len(rs)
	}
	p.logger.Debug("KV rule set replaced",
		"natsSubjects", len(set.NATS),
		"natsRules", natsTotal,
		"httpExactPaths", len(set.HTTP.Exact),
		"httpPatternRules", len(set.HTTP.Patterns),
		"scheduleRules", len(set.Schedule))

	if p.metrics != nil {
		p.metrics.SetRulesActive(float64(natsTotal + httpTotal + len(set.Schedule)))
	}
}

// ProcessForSubscription processes a message using O(1) lookup by trigger subject.
// triggerSubject is the trigger pattern (e.g., "sensors.tank.>") used for rule lookup.
// messageSubject is the actual message subject (e.g., "sensors.tank.001") used for template variables.
// Falls back to index pattern matching if no KV rules are loaded.
//
// filter selects which rules this transport evaluates (JetStreamRuleFilter for
// JetStream consumers, CoreRuleFilter for core subscriptions); nil means all.
// A subject can be served by both transports — via overlapping wildcards or
// mixed-mode rules on one subject — and each delivery must only fire its own
// rules or every such message double-fires.
func (p *Processor) ProcessForSubscription(triggerSubject, messageSubject string, payload []byte, headers map[string]string, filter NATSTriggerFilter) (Outcome, error) {
	set := p.loadKVRuleSet()
	if set == nil {
		return p.processNATSFiltered(messageSubject, payload, headers, filter)
	}

	rules := set.NATS[triggerSubject]
	if len(rules) == 0 {
		return p.processNATSFiltered(messageSubject, payload, headers, filter)
	}

	// Rules exist for this subject but none belong to this transport: done.
	// (No fallback — pattern matching would re-match the other transport's rules.)
	rules = filterNATSRules(rules, filter)
	if len(rules) == 0 {
		return Outcome{}, nil
	}

	p.logger.Debug("processing via KV rules",
		"triggerSubject", triggerSubject,
		"messageSubject", messageSubject,
		"ruleCount", len(rules))

	context, err := NewEvaluationContext(
		payload,
		headers,
		NewSubjectContext(messageSubject),
		nil,
		p.timeProvider.CurrentContext(),
		p.kvContext,
		p.sigVerification,
		p.logger,
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		return Outcome{}, err
	}

	return p.evaluateRules(rules, context, "nats")
}

// ProcessSchedule processes a schedule-triggered rule.
// Creates an evaluation context with no inbound message data — only time and KV contexts
// are available for condition evaluation and template rendering.
func (p *Processor) ProcessSchedule(rule *Rule) (Outcome, error) {
	p.logger.Debug("processing schedule rule", "cron", rule.Trigger.Schedule.Cron)

	context, err := NewEvaluationContext(
		nil, // no payload
		nil, // no headers
		nil, // no subject context
		nil, // no HTTP context
		p.timeProvider.CurrentContext(),
		p.kvContext,
		nil, // no signature verification
		p.logger,
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		p.logger.Error("failed to create evaluation context for schedule rule", "error", err)
		return Outcome{}, err
	}

	return p.evaluateRules([]*Rule{rule}, context, "schedule")
}

// ProcessNATS processes a NATS message through the rule engine
func (p *Processor) ProcessNATS(subject string, payload []byte, headers map[string]string) (Outcome, error) {
	return p.processNATSFiltered(subject, payload, headers, nil)
}

// processNATSFiltered is ProcessNATS with an optional transport filter applied
// to the pattern-matched rules (see ProcessForSubscription).
func (p *Processor) processNATSFiltered(subject string, payload []byte, headers map[string]string, filter NATSTriggerFilter) (Outcome, error) {
	p.logger.Debug("processing NATS message", "subject", subject, "payloadSize", len(payload))

	// Create SubjectContext first so we can reuse its tokens for pattern matching
	subjectCtx := NewSubjectContext(subject)

	rules := filterNATSRules(p.index.FindAllMatchingTokenized(subject, subjectCtx.Tokens), filter)
	if len(rules) == 0 {
		return Outcome{}, nil
	}

	context, err := NewEvaluationContext(
		payload,
		headers,
		subjectCtx,
		nil,
		p.timeProvider.CurrentContext(),
		p.kvContext,
		p.sigVerification,
		p.logger,
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		p.logger.Error("failed to create evaluation context", "error", err, "subject", subject)
		return Outcome{}, err
	}

	return p.evaluateRules(rules, context, "nats")
}

// ProcessHTTP processes an HTTP request through the rule engine
func (p *Processor) ProcessHTTP(path, method string, payload []byte, headers map[string]string) (Outcome, error) {
	p.logger.Debug("processing HTTP request", "path", path, "method", method, "payloadSize", len(payload))

	rules := p.findHTTPRules(path, method)
	if len(rules) == 0 {
		return Outcome{}, nil
	}

	context, err := NewEvaluationContext(
		payload,
		headers,
		nil,
		NewHTTPRequestContext(path, method),
		p.timeProvider.CurrentContext(),
		p.kvContext,
		p.sigVerification,
		p.logger,
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		p.logger.Error("failed to create evaluation context", "error", err, "path", path)
		return Outcome{}, err
	}

	return p.evaluateRules(rules, context, "http")
}

// findHTTPRules finds all HTTP rules matching the path and method.
// Collects from file-loaded exact + pattern rules, then KV-loaded exact +
// pattern rules. Exact matches and wildcard matches both fire when both apply.
func (p *Processor) findHTTPRules(path, method string) []*Rule {
	var rulesForPath []*Rule

	if fileExact := p.httpExactPaths[path]; len(fileExact) > 0 {
		rulesForPath = append(rulesForPath, fileExact...)
	}
	for _, pr := range p.httpPatternRules {
		if MatchPath(pr.Matcher, path) {
			rulesForPath = append(rulesForPath, pr.Rule)
		}
	}

	if set := p.loadKVRuleSet(); set != nil {
		if kvExact := set.HTTP.Exact[path]; len(kvExact) > 0 {
			rulesForPath = append(rulesForPath, kvExact...)
		}
		for _, pr := range set.HTTP.Patterns {
			if MatchPath(pr.Matcher, path) {
				rulesForPath = append(rulesForPath, pr.Rule)
			}
		}
	}

	if len(rulesForPath) == 0 {
		p.logger.Debug("no HTTP rules for path", "path", path)
		return nil
	}

	if method == "" {
		p.logger.Debug("HTTP rule lookup complete (all methods)",
			"path", path,
			"matchedRules", len(rulesForPath))
		return rulesForPath
	}

	var matching []*Rule
	for _, rule := range rulesForPath {
		if rule.Trigger.HTTP.Method == "" || rule.Trigger.HTTP.Method == method {
			matching = append(matching, rule)
			p.logger.Debug("HTTP rule matched",
				"path", path,
				"method", method,
				"ruleMethod", rule.Trigger.HTTP.Method)
		}
	}

	p.logger.Debug("HTTP rule lookup complete",
		"path", path,
		"method", method,
		"totalRulesForPath", len(rulesForPath),
		"matchedRules", len(matching))

	return matching
}

// CheckHTTPHMAC enforces fail-closed HMAC verification for an inbound webhook.
// It returns required=true if any rule matching (path, method) declares an
// `hmac` block, and ok=true only if the request satisfies every such rule. The
// gateway returns 401 when required && !ok, before any rule fires.
//
// When multiple matched rules declare hmac (uncommon — typically one rule per
// webhook path), the request must validate against each rule's secret; a
// mismatch fails closed, which is the safe outcome.
func (p *Processor) CheckHTTPHMAC(path, method string, body []byte, headers map[string]string) (required, ok bool) {
	for _, rule := range p.findHTTPRules(path, method) {
		cfg := rule.Trigger.HTTP.HMAC
		if cfg == nil {
			continue
		}
		required = true

		result := hmacError
		if secret, secretOK := p.resolveHMACSecret(cfg.Secret); secretOK {
			result = verifyHMAC(cfg, secret, body, headers)
		}

		if p.metrics != nil {
			p.metrics.IncWebhookHMACVerifications(result)
		}

		if result != hmacValid {
			p.logger.Warn("inbound HMAC verification failed",
				"path", path, "method", method, "header", cfg.Header, "result", result)
			return true, false
		}
	}
	return required, true
}

// resolveHMACSecret returns the secret bytes for an HMAC config value. Literal
// and ${ENV} values arrive already expanded at load time; a {@kv.bucket.key}
// reference is resolved here against the KV store. Returns ok=false when a KV
// reference cannot be resolved or the resulting value is empty (→ fail closed).
func (p *Processor) resolveHMACSecret(secret string) ([]byte, bool) {
	if strings.HasPrefix(secret, "{@kv.") && strings.HasSuffix(secret, "}") {
		if p.kvContext == nil {
			return nil, false
		}
		field := secret[1 : len(secret)-1] // strip surrounding { }
		val, found := p.kvContext.FieldWithContext(field, map[string]any{}, nil, nil)
		if !found {
			return nil, false
		}
		s := secretToString(val)
		if s == "" {
			return nil, false
		}
		return []byte(s), true
	}
	if secret == "" {
		return nil, false
	}
	return []byte(secret), true
}

// secretToString coerces a KV-resolved value to its string form for use as a secret.
func secretToString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

// triggerLabel returns a stable, low-cardinality identifier for a rule's trigger,
// used as the `trigger` label on rule_matches_total. There is no rule-name field,
// so the trigger descriptor is the closest per-rule identity available. Cardinality
// is bounded by the number of distinct triggers (operator-authored).
func triggerLabel(r *Rule) string {
	switch {
	case r.Trigger.NATS != nil:
		return r.Trigger.NATS.Subject
	case r.Trigger.HTTP != nil:
		return r.Trigger.HTTP.Path
	case r.Trigger.Schedule != nil:
		return "schedule:" + r.Trigger.Schedule.Cron
	default:
		return "unknown"
	}
}

// evaluateRules evaluates a set of rules against a context, returning the
// actions to run now and those a trailing throttle is holding.
//
// The split happens here rather than in a pass over the result because this is
// the only place that knows a rule is trailing — one rule's actions are exactly
// one deferred batch, so no regrouping is needed and no caller can skip it.
func (p *Processor) evaluateRules(rules []*Rule, context *EvaluationContext, triggerType string) (Outcome, error) {
	// Propagate the metrics sink so lazy signature verification can record
	// outcomes/latency. Harmless when nil (tests/CLI) or in WASM (no-op stub).
	context.Metrics = p.metrics

	var out Outcome
	matched := false

	for _, rule := range rules {
		p.logger.Debug("evaluating rule", "ruleIndex", rule.index, "triggerType", triggerType)

		// Trigger throttle: skip rule evaluation entirely if suppressed.
		// Leading mode only (enforced by the loader) — the whole point is to
		// avoid paying for evaluation, which holding a message cannot do.
		if cfg := getTriggerThrottle(rule); cfg != nil {
			key := ThrottleKey("t", rule.index, p.resolveThrottleKey(cfg, context, triggerType))
			window, _ := time.ParseDuration(cfg.Window) // pre-validated by loader
			if !p.throttle.Allow(key, window) {
				p.logger.Debug("trigger throttle suppressed rule", "ruleIndex", rule.index, "key", key)
				if p.metrics != nil {
					p.metrics.IncThrottleSuppressed("trigger")
				}
				continue
			}
		}

		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
			// Action throttle. Leading mode gates execution here and drops the
			// action outright. Trailing mode evaluates the action normally and
			// routes the result into Outcome.Deferred, leaving the coalescing
			// and the delayed emit to the executing layer — the Processor
			// itself never sleeps, times, or publishes.
			var deferSpec *DeferSpec
			if cfg := getActionThrottle(rule); cfg != nil {
				key := ThrottleKey("a", rule.index, p.resolveThrottleKey(cfg, context, triggerType))
				window, _ := time.ParseDuration(cfg.Window) // pre-validated by loader
				if cfg.IsTrailing() {
					deferSpec = &DeferSpec{Key: key, Window: window}
				} else if !p.throttle.Allow(key, window) {
					p.logger.Debug("action throttle suppressed rule", "ruleIndex", rule.index, "key", key)
					if p.metrics != nil {
						p.metrics.IncThrottleSuppressed("action")
					}
					continue
				}
			}

			actionResults, err := p.processAction(&rule.Action, context)
			if err != nil {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("error")
				}
				p.logger.Error("failed to process action", "error", err, "triggerType", triggerType)
				continue
			}

			for _, action := range actionResults {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("success")
					p.metrics.IncRuleMatches(triggerLabel(rule))

					if action.NATS != nil {
						if action.NATS.Merge {
							p.metrics.IncActionsByType("merge")
						} else if action.NATS.Passthrough {
							p.metrics.IncActionsByType("passthrough")
						} else {
							p.metrics.IncActionsByType("templated")
						}
					} else if action.HTTP != nil {
						if action.HTTP.Merge {
							p.metrics.IncActionsByType("merge")
						} else if action.HTTP.Passthrough {
							p.metrics.IncActionsByType("passthrough")
						} else {
							p.metrics.IncActionsByType("templated")
						}
					}
				}
			}

			if len(actionResults) == 0 {
				continue
			}
			matched = true

			if deferSpec == nil {
				out.Immediate = append(out.Immediate, actionResults...)
				continue
			}

			// One rule's fan-out is one batch, held under one key, so a
			// trailing window replaces it as a unit rather than collapsing N
			// forEach actions into the last element. Defer is also stamped on
			// each action so inspection surfaces (rule-cli, web tester) can
			// show that it is held rather than fired.
			for _, a := range actionResults {
				a.Defer = deferSpec
			}
			out.Deferred = append(out.Deferred, DeferredBatch{
				Key:     deferSpec.Key,
				Window:  deferSpec.Window,
				Actions: actionResults,
			})
		}
	}

	atomic.AddUint64(&p.stats.Processed, 1)
	if matched {
		atomic.AddUint64(&p.stats.Matched, 1)
	}
	return out, nil
}

// getTriggerThrottle returns the throttle config from a rule's trigger, or nil.
func getTriggerThrottle(rule *Rule) *ThrottleConfig {
	if rule.Trigger.NATS != nil {
		return rule.Trigger.NATS.Throttle
	}
	if rule.Trigger.HTTP != nil {
		return rule.Trigger.HTTP.Throttle
	}
	return nil
}

// getActionThrottle returns the throttle config from a rule's action, or nil.
// Respond actions have none: a single terminal response cannot be rate-limited.
func getActionThrottle(rule *Rule) *ThrottleConfig {
	if rule.Action.NATS != nil {
		return rule.Action.NATS.Throttle
	}
	if rule.Action.HTTP != nil {
		return rule.Action.HTTP.Throttle
	}
	return nil
}

// resolveThrottleKey resolves the throttle key template against the evaluation
// context. Defaults to the full NATS subject or HTTP path when no key is set.
//
// Note this always runs against the *trigger* context, before any forEach
// expansion — so a key naming an array-element field resolves empty. Action
// throttle deliberately gates a rule's action as a unit; see the throttle
// section of docs/01-core-concepts.md.
func (p *Processor) resolveThrottleKey(cfg *ThrottleConfig, ctx *EvaluationContext, triggerType string) string {
	if cfg.Key == "" {
		if triggerType == "nats" && ctx.Subject != nil {
			return ctx.Subject.Full
		}
		if triggerType == "http" && ctx.HTTP != nil {
			return ctx.HTTP.Path
		}
		return ""
	}

	resolved, err := p.templater.Execute(cfg.Key, ctx)
	if err != nil {
		p.logger.Warn("failed to resolve throttle key template, using literal",
			"template", cfg.Key, "error", err)
		return cfg.Key
	}
	return resolved
}

// processAction processes an action (NATS or HTTP)
// Returns a slice of actions to support forEach
func (p *Processor) processAction(action *Action, context *EvaluationContext) ([]*Action, error) {
	var results []*Action

	if action.NATS != nil {
		natsActions, err := p.processNATSAction(action.NATS, context)
		if err != nil {
			return nil, err
		}
		results = append(results, natsActions...)
	} else if action.HTTP != nil {
		httpActions, err := p.processHTTPAction(action.HTTP, context)
		if err != nil {
			return nil, err
		}
		results = append(results, httpActions...)
	} else if action.Respond != nil {
		respondActions, err := p.processRespondAction(action.Respond, context)
		if err != nil {
			return nil, err
		}
		results = append(results, respondActions...)
	} else {
		return nil, errors.New("action has no NATS, HTTP, or respond configuration")
	}

	return results, nil
}

// processRespondAction templates a respond action's payload and headers. The
// evaluated result is sent back to the caller (HTTP response or NATS reply) by
// the gateway/responder rather than published onward. Reuses resolvePayload and
// templateHeaders so passthrough/merge/template semantics match NATS/HTTP actions.
func (p *Processor) processRespondAction(action *RespondAction, context *EvaluationContext) ([]*Action, error) {
	result := &RespondAction{
		StatusCode:  action.StatusCode,
		Passthrough: action.Passthrough,
		Merge:       action.Merge,
	}

	pl, err := p.resolvePayload(action.Passthrough, action.Merge, action.Payload, context)
	if err != nil {
		return nil, err
	}
	if pl.Passthrough {
		result.RawPayload = pl.Raw
		result.Passthrough = true
	} else {
		result.Payload = pl.Text
	}

	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	return []*Action{{Respond: result}}, nil
}

// resolvePayload resolves an action's payload config into concrete bytes or text.
// Passthrough reuses the inbound raw payload; merge templates the overlay and
// deep-merges it onto the inbound message; otherwise the payload template is
// executed. Shared by processNATSAction and processHTTPAction.
func (p *Processor) resolvePayload(passthrough, merge bool, payloadTemplate string, context *EvaluationContext) (resolvedPayload, error) {
	if passthrough {
		return resolvedPayload{Raw: context.RawPayload, Passthrough: true}, nil
	}
	if merge {
		mergedBytes, err := p.applyMerge(payloadTemplate, context.Msg, context)
		if err != nil {
			return resolvedPayload{}, fmt.Errorf("failed to merge payload: %w", err)
		}
		return resolvedPayload{Raw: mergedBytes, Passthrough: true}, nil
	}
	payload, err := p.templater.Execute(payloadTemplate, context)
	if err != nil {
		return resolvedPayload{}, fmt.Errorf("failed to template payload: %w", err)
	}
	return resolvedPayload{Text: payload}, nil
}

// processNATSAction processes a NATS action with template substitution
// Returns multiple actions when forEach is used
func (p *Processor) processNATSAction(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	if action.ForEach != "" {
		return p.processNATSActionWithForEach(action, context)
	}

	// Mode must be carried onto the evaluated action: it is what the publishing
	// layer reads to pick core vs JetStream. Dropping it silently downgrades
	// every `mode: core` action to a JetStream publish.
	result := &NATSAction{
		Mode:        action.Mode,
		Passthrough: action.Passthrough,
		Merge:       action.Merge,
		Request:     action.Request,
		Timeout:     action.Timeout,
	}

	subject, err := p.templater.Execute(action.Subject, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template subject: %w", err)
	}
	result.Subject = subject

	pl, err := p.resolvePayload(action.Passthrough, action.Merge, action.Payload, context)
	if err != nil {
		return nil, err
	}
	if pl.Passthrough {
		result.RawPayload = pl.Raw
		result.Passthrough = true
	} else {
		result.Payload = pl.Text
	}

	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	return []*Action{{NATS: result}}, nil
}

// ensureObject wraps primitive array elements to ensure they can be processed.
// Objects are passed through unchanged.
//
// This enables forEach to work with:
//   - Object arrays: [{"id": 1}, {"id": 2}] → unchanged
//   - String arrays: ["dev-1", "dev-2"] → [{"@value": "dev-1"}, {"@value": "dev-2"}]
//   - Number arrays: [100, 200, 300] → [{"@value": 100}, {"@value": 200}, {"@value": 300}]
//   - Mixed arrays: [{"id": 1}, "text", 42] → handles each appropriately
func ensureObject(item any) map[string]any {
	if itemMap, ok := item.(map[string]any); ok {
		// Already an object - return as-is
		return itemMap
	}

	// Primitive - wrap it in @value
	return map[string]any{"@value": item}
}

// processNATSActionWithForEach processes a NATS action with forEach iteration
func (p *Processor) processNATSActionWithForEach(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	cfg := forEachActionConfig{
		ForEach: action.ForEach, forEachPath: action.forEachPath,
		Filter: action.Filter, Passthrough: action.Passthrough,
		Merge: action.Merge, Payload: action.Payload,
		Headers: action.Headers, MetricsType: "nats",
	}
	return p.processActionForEach(cfg, context, func(elemCtx *EvaluationContext, pl resolvedPayload, headers map[string]string, _ int) (*Action, error) {
		subject, err := p.templater.Execute(action.Subject, elemCtx)
		if err != nil {
			return nil, &forEachElementError{ErrorType: "template_subject_failed", Err: err}
		}
		return &Action{NATS: &NATSAction{
			Subject: subject, Mode: action.Mode,
			Passthrough: pl.Passthrough,
			Merge:       action.Merge, Payload: pl.Text,
			RawPayload: pl.Raw, Headers: headers,
			Request: action.Request, Timeout: action.Timeout,
		}}, nil
	})
}

// processHTTPAction processes an HTTP action with template substitution
// Returns multiple actions when forEach is used
func (p *Processor) processHTTPAction(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	if action.ForEach != "" {
		return p.processHTTPActionWithForEach(action, context)
	}

	result := &HTTPAction{
		Passthrough: action.Passthrough,
		Merge:       action.Merge,
		Retry:       action.Retry,
	}

	url, err := p.templater.Execute(action.URL, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template URL: %w", err)
	}
	result.URL = url

	method, err := p.templater.Execute(action.Method, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template method: %w", err)
	}
	result.Method = method

	pl, err := p.resolvePayload(action.Passthrough, action.Merge, action.Payload, context)
	if err != nil {
		return nil, err
	}
	if pl.Passthrough {
		result.RawPayload = pl.Raw
		result.Passthrough = true
	} else {
		result.Payload = pl.Text
	}

	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	if action.PublishResponse != nil {
		subj, err := p.templater.Execute(action.PublishResponse.Subject, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template publishResponse.subject: %w", err)
		}
		result.PublishResponse = &PublishResponseSpec{Subject: subj}
	}

	return []*Action{{HTTP: result}}, nil
}

// processHTTPActionWithForEach processes an HTTP action with forEach iteration
func (p *Processor) processHTTPActionWithForEach(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	cfg := forEachActionConfig{
		ForEach: action.ForEach, forEachPath: action.forEachPath,
		Filter: action.Filter, Passthrough: action.Passthrough,
		Merge: action.Merge, Payload: action.Payload,
		Headers: action.Headers, MetricsType: "http",
	}
	return p.processActionForEach(cfg, context, func(elemCtx *EvaluationContext, pl resolvedPayload, headers map[string]string, _ int) (*Action, error) {
		url, err := p.templater.Execute(action.URL, elemCtx)
		if err != nil {
			return nil, &forEachElementError{ErrorType: "template_url_failed", Err: err}
		}
		method, err := p.templater.Execute(action.Method, elemCtx)
		if err != nil {
			return nil, &forEachElementError{ErrorType: "template_method_failed", Err: err}
		}
		var publishResponse *PublishResponseSpec
		if action.PublishResponse != nil {
			subj, err := p.templater.Execute(action.PublishResponse.Subject, elemCtx)
			if err != nil {
				return nil, &forEachElementError{ErrorType: "template_publish_response_subject_failed", Err: err}
			}
			publishResponse = &PublishResponseSpec{Subject: subj}
		}
		return &Action{HTTP: &HTTPAction{
			URL: url, Method: method, Passthrough: pl.Passthrough,
			Merge: action.Merge, Payload: pl.Text,
			RawPayload: pl.Raw, Headers: headers,
			Retry:           action.Retry,
			PublishResponse: publishResponse,
		}}, nil
	})
}

// processActionForEach is the shared forEach loop for both NATS and HTTP actions.
// It handles array extraction, filtering, payload processing, error tracking,
// metrics, and logging. The buildAction callback handles action-type-specific
// templating (e.g., subject for NATS, url+method for HTTP).
func (p *Processor) processActionForEach(
	cfg forEachActionConfig,
	context *EvaluationContext,
	buildAction func(elemCtx *EvaluationContext, pl resolvedPayload, headers map[string]string, index int) (*Action, error),
) ([]*Action, error) {
	start := time.Now()

	p.logger.Debug("processing action with forEach",
		"forEachField", cfg.ForEach, "type", cfg.MetricsType)

	arrayData, err := p.extractForEachArray(cfg.ForEach, cfg.forEachPath, context)
	if err != nil {
		return nil, err
	}

	result := &ForEachResult{
		TotalElements: len(arrayData.items),
		Errors:        make([]ElementError, 0),
	}

	var actions []*Action

	for i, item := range arrayData.items {
		itemMap := ensureObject(item)
		elementContext := context.WithElement(itemMap)

		// Apply filter if present
		if cfg.Filter != nil {
			if !p.evaluator.Evaluate(cfg.Filter, elementContext) {
				result.FilteredElements++
				continue
			}
		}

		// Process payload (shared across action types)
		var pl resolvedPayload
		if cfg.Passthrough {
			rawPayload, err := safeMarshal(itemMap)
			if err != nil {
				p.logElementError(result, i, "marshal_failed", cfg.ForEach, err)
				continue
			}
			pl = resolvedPayload{Raw: rawPayload, Passthrough: true}
		} else if cfg.Merge {
			mergedBytes, err := p.applyMerge(cfg.Payload, itemMap, elementContext)
			if err != nil {
				p.logElementError(result, i, "merge_payload_failed", cfg.ForEach, err)
				continue
			}
			pl = resolvedPayload{Raw: mergedBytes, Passthrough: true}
		} else {
			payload, err := p.templater.Execute(cfg.Payload, elementContext)
			if err != nil {
				p.logElementError(result, i, "template_payload_failed", cfg.ForEach, err)
				continue
			}
			pl = resolvedPayload{Text: payload}
		}

		// Template headers
		headers, err := p.templateHeaders(cfg.Headers, elementContext)
		if err != nil {
			p.logElementError(result, i, "template_headers_failed", cfg.ForEach, err)
			continue
		}

		// Build the action-type-specific result
		action, err := buildAction(elementContext, pl, headers, i)
		if err != nil {
			var elemErr *forEachElementError
			if errors.As(err, &elemErr) {
				p.logElementError(result, i, elemErr.ErrorType, cfg.ForEach, elemErr.Err)
			} else {
				p.logElementError(result, i, "action_build_failed", cfg.ForEach, err)
			}
			continue
		}

		actions = append(actions, action)
		result.ProcessedElements++
	}

	duration := time.Since(start).Seconds()

	if p.metrics != nil {
		p.metrics.IncForEachIterations(cfg.MetricsType, result.TotalElements)
		p.metrics.IncForEachFiltered(cfg.MetricsType, result.FilteredElements)
		p.metrics.IncForEachActionsGenerated(cfg.MetricsType, result.ProcessedElements)
		p.metrics.ObserveForEachDuration(cfg.MetricsType, duration)
		for _, elemErr := range result.Errors {
			p.metrics.IncForEachElementErrors(cfg.MetricsType, elemErr.ErrorType)
		}
	}

	logFields := []any{
		"field", cfg.ForEach,
		"totalElements", result.TotalElements,
		"processedElements", result.ProcessedElements,
		"filteredElements", result.FilteredElements,
		"failedElements", result.FailedElements,
		"actionsGenerated", len(actions),
		"duration", fmt.Sprintf("%.3fs", duration),
	}
	if result.FailedElements > 0 {
		p.logger.Warn("forEach processing complete with failures", logFields...)
	} else {
		p.logger.Debug("forEach processing complete", logFields...)
	}

	return actions, nil
}

// logElementError records a per-element failure in the ForEachResult and logs it.
func (p *Processor) logElementError(result *ForEachResult, index int, errorType string, field string, err error) {
	p.logger.Error("forEach element processing failed",
		"field", field, "index", index, "errorType", errorType, "error", err)
	result.FailedElements++
	result.Errors = append(result.Errors, ElementError{
		Index: index, ErrorType: errorType, Err: err,
	})
}

// templateHeaders templates all header values
func (p *Processor) templateHeaders(headers map[string]string, context *EvaluationContext) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}

	result := make(map[string]string, len(headers))
	for key, valueTemplate := range headers {
		processedValue, err := p.templater.Execute(valueTemplate, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template header %q: %w", key, err)
		}
		result[key] = processedValue
	}

	return result, nil
}

// safeMarshal marshals a value to JSON with error handling (no panic)
// Replaced mustMarshal to handle errors gracefully
func safeMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}
	return b, nil
}

// deepMerge merges overlay into base, returning a new map.
// Overlay values overwrite base values. Nested objects are merged recursively.
// Arrays in the overlay replace arrays in the base wholesale.
// The base map is never mutated.
func deepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if overlayMap, ok := v.(map[string]any); ok {
			if baseMap, ok := result[k].(map[string]any); ok {
				result[k] = deepMerge(baseMap, overlayMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}

// applyMerge templates the overlay payload, parses it as a JSON object, deep-merges
// it onto base, and returns the resulting JSON bytes.
func (p *Processor) applyMerge(payloadTemplate string, base map[string]any, context *EvaluationContext) ([]byte, error) {
	overlayStr, err := p.templater.Execute(payloadTemplate, context)
	if err != nil {
		return nil, fmt.Errorf("merge: failed to template overlay: %w", err)
	}
	var overlayData map[string]any
	if err := json.Unmarshal([]byte(overlayStr), &overlayData); err != nil {
		return nil, fmt.Errorf("merge: overlay is not a valid JSON object: %w", err)
	}
	merged := deepMerge(base, overlayData)
	return safeMarshal(merged)
}

// ProcessWithSubject is kept for backward compatibility.
// Used by internal/broker/subscription.go.
func (p *Processor) ProcessWithSubject(subject string, payload []byte, headers map[string]string) (Outcome, error) {
	return p.ProcessNATS(subject, payload, headers)
}

// Stats returns processor statistics
func (p *Processor) Stats() ProcessorStats {
	return ProcessorStats{
		Processed: atomic.LoadUint64(&p.stats.Processed),
		Matched:   atomic.LoadUint64(&p.stats.Matched),
		Errors:    atomic.LoadUint64(&p.stats.Errors),
	}
}

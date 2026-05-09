// file: internal/rule/processor.go

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
	index            *RuleIndex
	allRules         []*Rule
	scheduleRules    []*Rule
	httpPathIndex    map[string][]*Rule
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

	// KV-based rule storage: atomically swapped maps for lock-free reads.
	// These are populated by RuleKVManager when KV rules are enabled.
	kvRules          atomic.Value // holds map[string][]*Rule — NATS subject → rules
	httpKVRules      atomic.Value // holds map[string][]*Rule — HTTP path → rules
	kvScheduleRules  atomic.Value // holds []*Rule — schedule-triggered rules from KV
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
	Error     error
}

// forEachArrayData holds the extracted array and metadata for forEach processing
type forEachArrayData struct {
	items     []interface{}
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
	var arrayValue interface{}
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
				return nil, fmt.Errorf("invalid forEach path '%s': %w", fieldPath, err)
			}
		}
		var traverseErr error
		arrayValue, traverseErr = context.traverser.TraversePath(context.Msg, arrayPath)
		if traverseErr != nil {
			return nil, fmt.Errorf("forEach field not found: %s: %w", forEachTemplate, traverseErr)
		}
	}

	arrayItems, ok := arrayValue.([]interface{})
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

// NewProcessor creates a new processor with optional signature verification
func NewProcessor(log *logger.Logger, metrics *metrics.Metrics, kvCtx *KVContext, sigVerification *SignatureVerification) *Processor {
	p := &Processor{
		index:           NewRuleIndex(log),
		allRules:        make([]*Rule, 0),
		httpPathIndex:   make(map[string][]*Rule),
		timeProvider:    NewSystemTimeProvider(),
		kvContext:       kvCtx,
		logger:          log.With("component", "processor"),
		metrics:         metrics,
		evaluator:       NewEvaluator(log),
		templater:       NewTemplateEngine(log),
		sigVerification: sigVerification,
		maxForEachIters: DefaultMaxForEachIterations,
		throttle:        NewThrottleManager(),
	}

	if kvCtx != nil {
		p.logger.Info("initializing processor with KV support", "buckets", kvCtx.GetAllBuckets())
	} else {
		p.logger.Info("initializing processor without KV support")
	}

	if sigVerification != nil && sigVerification.Enabled {
		p.logger.Info("initializing processor with signature verification enabled",
			"pubKeyHeader", sigVerification.PublicKeyHeader,
			"sigHeader", sigVerification.SignatureHeader)
	}

	return p
}

// SetMaxForEachIterations sets the maximum allowed forEach iterations
func (p *Processor) SetMaxForEachIterations(max int) {
	p.maxForEachIters = max
	p.logger.Info("forEach iteration limit configured", "maxIterations", max)
}

// LoadRules loads rules and indexes them by trigger type
func (p *Processor) LoadRules(rules []Rule) error {
	p.logger.Info("loading rules into processor", "ruleCount", len(rules))

	p.index.Clear()
	p.allRules = make([]*Rule, 0, len(rules))
	p.scheduleRules = make([]*Rule, 0)
	p.httpPathIndex = make(map[string][]*Rule)

	natsCount := 0
	httpCount := 0
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
			p.httpPathIndex[path] = append(p.httpPathIndex[path], rule)
			httpCount++

			p.logger.Debug("indexed HTTP rule",
				"path", path,
				"method", rule.Trigger.HTTP.Method)
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
		"scheduleRules", scheduleCount,
		"httpPaths", len(p.httpPathIndex))

	return nil
}

// GetSubjects returns all NATS subjects for subscription setup
func (p *Processor) GetSubjects() []string {
	return p.index.GetSubscriptionSubjects()
}

// GetHTTPPaths returns all unique HTTP paths for route setup
func (p *Processor) GetHTTPPaths() []string {
	pathSet := make(map[string]bool)
	for path := range p.httpPathIndex {
		pathSet[path] = true
	}
	if kvMap, ok := p.httpKVRules.Load().(map[string][]*Rule); ok {
		for path := range kvMap {
			pathSet[path] = true
		}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	return paths
}

// GetAllRules returns all loaded rules (NATS, HTTP, and schedule)
func (p *Processor) GetAllRules() []*Rule {
	return p.allRules
}

// GetScheduleRules returns all schedule-triggered rules.
// Returns KV-loaded schedule rules when set (even if empty); falls back to file-loaded rules.
func (p *Processor) GetScheduleRules() []*Rule {
	if kv, ok := p.kvScheduleRules.Load().([]*Rule); ok {
		return kv
	}
	return p.scheduleRules
}

// ReplaceScheduleRules atomically swaps the KV-loaded schedule rule set.
// Called by RuleKVManager when KV Watch detects changes to schedule-triggered rules.
func (p *Processor) ReplaceScheduleRules(rules []*Rule) {
	p.kvScheduleRules.Store(rules)
	p.logger.Debug("KV schedule rules replaced", "count", len(rules))
}

// ReplaceRules atomically swaps the KV-loaded NATS rule set.
// Called by RuleKVManager when KV Watch detects changes.
// The map is keyed by trigger subject (e.g., "sensors.tank.>").
func (p *Processor) ReplaceRules(rules map[string][]*Rule) {
	p.kvRules.Store(rules)

	total := 0
	for _, rs := range rules {
		total += len(rs)
	}
	p.logger.Debug("KV rules replaced", "subjects", len(rules), "totalRules", total)

	if p.metrics != nil {
		p.metrics.SetRulesActive(float64(total))
	}
}

// ReplaceHTTPRules atomically swaps the KV-loaded HTTP rule set.
// Called by RuleKVManager when KV Watch detects changes.
// The map is keyed by HTTP path (e.g., "/webhook/github").
func (p *Processor) ReplaceHTTPRules(rules map[string][]*Rule) {
	p.httpKVRules.Store(rules)

	total := 0
	for _, rs := range rules {
		total += len(rs)
	}
	p.logger.Debug("KV HTTP rules replaced", "paths", len(rules), "totalRules", total)
}

// ProcessForSubscription processes a message using O(1) lookup by trigger subject.
// triggerSubject is the trigger pattern (e.g., "sensors.tank.>") used for rule lookup.
// messageSubject is the actual message subject (e.g., "sensors.tank.001") used for template variables.
// Falls back to ProcessNATS (pattern matching) if no KV rules are loaded.
func (p *Processor) ProcessForSubscription(triggerSubject, messageSubject string, payload []byte, headers map[string]string) ([]*Action, error) {
	ruleMap, _ := p.kvRules.Load().(map[string][]*Rule)
	if ruleMap == nil {
		return p.ProcessNATS(messageSubject, payload, headers)
	}

	rules := ruleMap[triggerSubject]
	if len(rules) == 0 {
		return p.ProcessNATS(messageSubject, payload, headers)
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
		p.timeProvider.GetCurrentContext(),
		p.kvContext,
		p.sigVerification,
		p.logger,
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		return nil, err
	}

	return p.evaluateRules(rules, context, "nats")
}

// ProcessSchedule processes a schedule-triggered rule.
// Creates an evaluation context with no inbound message data — only time and KV contexts
// are available for condition evaluation and template rendering.
func (p *Processor) ProcessSchedule(rule *Rule) ([]*Action, error) {
	p.logger.Debug("processing schedule rule", "cron", rule.Trigger.Schedule.Cron)

	context, err := NewEvaluationContext(
		nil, // no payload
		nil, // no headers
		nil, // no subject context
		nil, // no HTTP context
		p.timeProvider.GetCurrentContext(),
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
		return nil, err
	}

	return p.evaluateRules([]*Rule{rule}, context, "schedule")
}

// ProcessNATS processes a NATS message through the rule engine
func (p *Processor) ProcessNATS(subject string, payload []byte, headers map[string]string) ([]*Action, error) {
	p.logger.Debug("processing NATS message", "subject", subject, "payloadSize", len(payload))

	// Create SubjectContext first so we can reuse its tokens for pattern matching
	subjectCtx := NewSubjectContext(subject)

	rules := p.index.FindAllMatchingTokenized(subject, subjectCtx.Tokens)
	if len(rules) == 0 {
		return nil, nil
	}

	context, err := NewEvaluationContext(
		payload,
		headers,
		subjectCtx,
		nil,
		p.timeProvider.GetCurrentContext(),
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
		return nil, err
	}

	return p.evaluateRules(rules, context, "nats")
}

// ProcessHTTP processes an HTTP request through the rule engine
func (p *Processor) ProcessHTTP(path, method string, payload []byte, headers map[string]string) ([]*Action, error) {
	p.logger.Debug("processing HTTP request", "path", path, "method", method, "payloadSize", len(payload))

	rules := p.findHTTPRules(path, method)
	if len(rules) == 0 {
		return nil, nil
	}

	context, err := NewEvaluationContext(
		payload,
		headers,
		nil,
		NewHTTPRequestContext(path, method),
		p.timeProvider.GetCurrentContext(),
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
		return nil, err
	}

	return p.evaluateRules(rules, context, "http")
}

// findHTTPRules finds all HTTP rules matching the path and method.
// Checks both file-based httpPathIndex and KV-based httpKVRules.
func (p *Processor) findHTTPRules(path, method string) []*Rule {
	// Collect rules from both sources
	var rulesForPath []*Rule

	if fileRules := p.httpPathIndex[path]; len(fileRules) > 0 {
		rulesForPath = append(rulesForPath, fileRules...)
	}

	if kvMap, ok := p.httpKVRules.Load().(map[string][]*Rule); ok {
		if kvRules := kvMap[path]; len(kvRules) > 0 {
			rulesForPath = append(rulesForPath, kvRules...)
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

// evaluateRules evaluates a set of rules against a context
func (p *Processor) evaluateRules(rules []*Rule, context *EvaluationContext, triggerType string) ([]*Action, error) {
	var actions []*Action

	for _, rule := range rules {
		p.logger.Debug("evaluating rule", "ruleIndex", rule.index, "triggerType", triggerType)

		// Trigger debounce: skip rule evaluation entirely if suppressed
		if cfg := getTriggerDebounce(rule); cfg != nil {
			key := ThrottleKey("t", rule.index, p.resolveThrottleKey(cfg, context, triggerType))
			window, _ := time.ParseDuration(cfg.Window) // pre-validated by loader
			if !p.throttle.Allow(key, window) {
				p.logger.Debug("trigger debounce suppressed rule", "ruleIndex", rule.index, "key", key)
				if p.metrics != nil {
					p.metrics.IncThrottleSuppressed("trigger")
				}
				continue
			}
		}

		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
			// Action debounce: skip action execution if suppressed
			if cfg := getActionDebounce(rule); cfg != nil {
				key := ThrottleKey("a", rule.index, p.resolveThrottleKey(cfg, context, triggerType))
				window, _ := time.ParseDuration(cfg.Window) // pre-validated by loader
				if !p.throttle.Allow(key, window) {
					p.logger.Debug("action debounce suppressed rule", "ruleIndex", rule.index, "key", key)
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
					p.metrics.IncRuleMatches()

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

			actions = append(actions, actionResults...)
		}
	}

	atomic.AddUint64(&p.stats.Processed, 1)
	if len(actions) > 0 {
		atomic.AddUint64(&p.stats.Matched, 1)
	}
	return actions, nil
}

// getTriggerDebounce returns the debounce config from a rule's trigger, or nil.
func getTriggerDebounce(rule *Rule) *DebounceConfig {
	if rule.Trigger.NATS != nil {
		return rule.Trigger.NATS.Debounce
	}
	if rule.Trigger.HTTP != nil {
		return rule.Trigger.HTTP.Debounce
	}
	return nil
}

// getActionDebounce returns the debounce config from a rule's action, or nil.
func getActionDebounce(rule *Rule) *DebounceConfig {
	if rule.Action.NATS != nil {
		return rule.Action.NATS.Debounce
	}
	if rule.Action.HTTP != nil {
		return rule.Action.HTTP.Debounce
	}
	return nil
}

// resolveThrottleKey resolves the debounce key template against the evaluation context.
// Defaults to the full NATS subject or HTTP path when no key is configured.
func (p *Processor) resolveThrottleKey(cfg *DebounceConfig, ctx *EvaluationContext, triggerType string) string {
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
	} else {
		return nil, fmt.Errorf("action has no NATS or HTTP configuration")
	}

	return results, nil
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

	result := &NATSAction{
		Passthrough: action.Passthrough,
		Merge:       action.Merge,
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
func ensureObject(item interface{}) map[string]interface{} {
	if itemMap, ok := item.(map[string]interface{}); ok {
		// Already an object - return as-is
		return itemMap
	}
	
	// Primitive - wrap it in @value
	return map[string]interface{}{"@value": item}
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
			Subject: subject, Passthrough: pl.Passthrough,
			Merge: action.Merge, Payload: pl.Text,
			RawPayload: pl.Raw, Headers: headers,
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
		Index: index, ErrorType: errorType, Error: err,
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
			return nil, fmt.Errorf("failed to template header '%s': %w", key, err)
		}
		result[key] = processedValue
	}

	return result, nil
}

// safeMarshal marshals a value to JSON with error handling (no panic)
// Replaced mustMarshal to handle errors gracefully
func safeMarshal(v interface{}) ([]byte, error) {
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
func deepMerge(base, overlay map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(base)+len(overlay))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if overlayMap, ok := v.(map[string]interface{}); ok {
			if baseMap, ok := result[k].(map[string]interface{}); ok {
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
func (p *Processor) applyMerge(payloadTemplate string, base map[string]interface{}, context *EvaluationContext) ([]byte, error) {
	overlayStr, err := p.templater.Execute(payloadTemplate, context)
	if err != nil {
		return nil, fmt.Errorf("merge: failed to template overlay: %w", err)
	}
	var overlayData map[string]interface{}
	if err := json.Unmarshal([]byte(overlayStr), &overlayData); err != nil {
		return nil, fmt.Errorf("merge: overlay is not a valid JSON object: %w", err)
	}
	merged := deepMerge(base, overlayData)
	return safeMarshal(merged)
}

// ProcessWithSubject is kept for backward compatibility.
// Used by internal/broker/subscription.go.
func (p *Processor) ProcessWithSubject(subject string, payload []byte, headers map[string]string) ([]*Action, error) {
	return p.ProcessNATS(subject, payload, headers)
}

// SetTimeProvider allows injecting a mock time provider for testing
func (p *Processor) SetTimeProvider(provider TimeProvider) {
	p.timeProvider = provider
}

// GetStats returns processor statistics
func (p *Processor) GetStats() ProcessorStats {
	return ProcessorStats{
		Processed: atomic.LoadUint64(&p.stats.Processed),
		Matched:   atomic.LoadUint64(&p.stats.Matched),
		Errors:    atomic.LoadUint64(&p.stats.Errors),
	}
}


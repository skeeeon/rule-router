// file: internal/rule/processor.go

package rule

import (
	json "github.com/goccy/go-json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

type Processor struct {
	index            *RuleIndex
	allRules         []*Rule
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

// extractForEachField unwraps the forEach field from template syntax to get the actual path.
// This is critical for the new brace-everywhere syntax.
//
// Examples:
//   "{notifications}"        → "notifications"
//   "{data.readings}"        → "data.readings"
//   "{nested.path.array}"    → "nested.path.array"
//   "{@items}"               → "@items"
//   "notifications"          → "" (invalid - missing braces)
//   "{}"                     → "" (invalid - empty)
//
// Returns empty string if the template is invalid (missing braces or empty content).
func extractForEachField(forEachTemplate string) string {
	template := strings.TrimSpace(forEachTemplate)
	
	// Must have both opening and closing braces
	if !strings.HasPrefix(template, "{") || !strings.HasSuffix(template, "}") {
		return ""
	}
	
	// Extract content between braces
	fieldPath := template[1 : len(template)-1]
	
	// Content cannot be empty
	if strings.TrimSpace(fieldPath) == "" {
		return ""
	}
	
	return fieldPath
}

// NewProcessor creates a new processor with optional signature verification
func NewProcessor(log *logger.Logger, metrics *metrics.Metrics, kvCtx *KVContext, sigVerification *SignatureVerification) *Processor {
	p := &Processor{
		index:           NewRuleIndex(log),
		allRules:        make([]*Rule, 0),
		httpPathIndex:   make(map[string][]*Rule),
		timeProvider:    NewSystemTimeProvider(),
		kvContext:       kvCtx,
		logger:          log,
		metrics:         metrics,
		evaluator:       NewEvaluator(log),
		templater:       NewTemplateEngine(log),
		sigVerification: sigVerification,
		maxForEachIters: 100, // Default safe limit
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
	p.httpPathIndex = make(map[string][]*Rule)
	
	natsCount := 0
	httpCount := 0
	
	for i := range rules {
		rule := &rules[i]
		p.allRules = append(p.allRules, rule)
		
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
	}
	
	if p.metrics != nil {
		p.metrics.SetRulesActive(float64(natsCount + httpCount))
	}
	
	p.logger.Info("rules loaded",
		"total", len(rules),
		"natsRules", natsCount,
		"httpRules", httpCount,
		"httpPaths", len(p.httpPathIndex))
	
	return nil
}

// GetSubjects returns all NATS subjects for subscription setup
func (p *Processor) GetSubjects() []string {
	return p.index.GetSubscriptionSubjects()
}

// GetHTTPPaths returns all unique HTTP paths for route setup
func (p *Processor) GetHTTPPaths() []string {
	paths := make([]string, 0, len(p.httpPathIndex))
	for path := range p.httpPathIndex {
		paths = append(paths, path)
	}
	return paths
}

// GetAllRules returns all loaded rules (both NATS and HTTP)
func (p *Processor) GetAllRules() []*Rule {
	return p.allRules
}

// ProcessNATS processes a NATS message through the rule engine
func (p *Processor) ProcessNATS(subject string, payload []byte, headers map[string]string) ([]*Action, error) {
	p.logger.Debug("processing NATS message", "subject", subject, "payloadSize", len(payload))

	rules := p.index.FindAllMatching(subject)
	if len(rules) == 0 {
		return nil, nil
	}

	context, err := NewEvaluationContext(
		payload,
		headers,
		NewSubjectContext(subject),
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

// findHTTPRules finds all HTTP rules matching the path and method
func (p *Processor) findHTTPRules(path, method string) []*Rule {
	rulesForPath, exists := p.httpPathIndex[path]
	if !exists || len(rulesForPath) == 0 {
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
		p.logger.Debug("evaluating rule", "triggerType", triggerType)

		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
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
						if action.NATS.Passthrough {
							p.metrics.IncActionsByType("passthrough")
						} else {
							p.metrics.IncActionsByType("templated")
						}
					} else if action.HTTP != nil {
						if action.HTTP.Passthrough {
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

// processNATSAction processes a NATS action with template substitution
// Returns multiple actions when forEach is used
func (p *Processor) processNATSAction(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	if action.ForEach != "" {
		return p.processNATSActionWithForEach(action, context)
	}

	result := &NATSAction{
		Passthrough: action.Passthrough,
	}

	subject, err := p.templater.Execute(action.Subject, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template subject: %w", err)
	}
	result.Subject = subject

	if action.Passthrough {
		result.RawPayload = context.RawPayload
	} else {
		payload, err := p.templater.Execute(action.Payload, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template payload: %w", err)
		}
		result.Payload = payload
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
// Reuses element context for both filter and templating
func (p *Processor) processNATSActionWithForEach(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	start := time.Now()
	
	p.logger.Debug("processing NATS action with forEach",
		"forEachField", action.ForEach)

	// Extract the field path from template braces
	// forEach: "{data.readings}" → "data.readings"
	fieldPath := extractForEachField(action.ForEach)
	if fieldPath == "" {
		return nil, fmt.Errorf("invalid forEach template syntax (must be {field}): %s", action.ForEach)
	}
	
	p.logger.Debug("extracted forEach field path",
		"template", action.ForEach,
		"extractedPath", fieldPath)

	// Use brace-aware path splitting on the EXTRACTED path
	arrayPath, err := SplitPathRespectingBraces(fieldPath)
	if err != nil {
		return nil, fmt.Errorf("invalid forEach path '%s': %w", fieldPath, err)
	}

	arrayValue, err := context.traverser.TraversePath(context.Msg, arrayPath)
	if err != nil {
		return nil, fmt.Errorf("forEach field not found: %s: %w", action.ForEach, err)
	}

	arrayItems, ok := arrayValue.([]interface{})
	if !ok {
		return nil, fmt.Errorf("forEach field is not an array: %s (type: %T)", action.ForEach, arrayValue)
	}

	// Enforce iteration limit
	if p.maxForEachIters > 0 && len(arrayItems) > p.maxForEachIters {
		return nil, fmt.Errorf("forEach array exceeds limit: %d elements > %d max iterations (configure maxForEachIterations to increase)",
			len(arrayItems), p.maxForEachIters)
	}

	p.logger.Debug("forEach array extracted",
		"field", action.ForEach,
		"arrayLength", len(arrayItems),
		"maxIterations", p.maxForEachIters)

	// Track detailed results
	result := &ForEachResult{
		TotalElements: len(arrayItems),
		Errors:        make([]ElementError, 0),
	}
	
	var actions []*Action

	for i, item := range arrayItems {
		// Wrap primitives, pass objects through
		itemMap := ensureObject(item)
		
		// Log if we wrapped a primitive (helpful for debugging)
		if _, ok := item.(map[string]interface{}); !ok {
			p.logger.Debug("forEach element wrapped as primitive",
				"field", action.ForEach,
				"index", i,
				"originalType", fmt.Sprintf("%T", item),
				"accessVia", "@value")
		}

		// Create context once and reuse for both filter and templating
		elementContext, err := p.createElementContextOptimized(itemMap, context)
		if err != nil {
			p.logger.Error("failed to create element context",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "context_creation_failed",
				Error:     err,
			})
			continue
		}

		// Apply filter if present (using the same context)
		if action.Filter != nil {
			if !p.evaluator.Evaluate(action.Filter, elementContext) {
				p.logger.Debug("forEach element filtered out",
					"field", action.ForEach,
					"index", i)
				result.FilteredElements++
				continue
			}
		}

		// Template the action (reusing the same context)
		actionResult := &NATSAction{
			Passthrough: action.Passthrough,
		}

		subject, err := p.templater.Execute(action.Subject, elementContext)
		if err != nil {
			p.logger.Error("failed to template subject for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "template_subject_failed",
				Error:     err,
			})
			continue
		}
		actionResult.Subject = subject

		if action.Passthrough {
			// Handle marshal errors gracefully
			rawPayload, err := safeMarshal(itemMap)
			if err != nil {
				p.logger.Error("failed to marshal element for passthrough",
					"field", action.ForEach,
					"index", i,
					"error", err)
				result.FailedElements++
				result.Errors = append(result.Errors, ElementError{
					Index:     i,
					ErrorType: "marshal_failed",
					Error:     err,
				})
				continue
			}
			actionResult.RawPayload = rawPayload
		} else {
			payload, err := p.templater.Execute(action.Payload, elementContext)
			if err != nil {
				p.logger.Error("failed to template payload for forEach element",
					"field", action.ForEach,
					"index", i,
					"error", err)
				result.FailedElements++
				result.Errors = append(result.Errors, ElementError{
					Index:     i,
					ErrorType: "template_payload_failed",
					Error:     err,
				})
				continue
			}
			actionResult.Payload = payload
		}

		actionResult.Headers, err = p.templateHeaders(action.Headers, elementContext)
		if err != nil {
			p.logger.Error("failed to template headers for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "template_headers_failed",
				Error:     err,
			})
			continue
		}

		actions = append(actions, &Action{NATS: actionResult})
		result.ProcessedElements++
		
		p.logger.Debug("forEach element processed successfully",
			"field", action.ForEach,
			"index", i,
			"subject", actionResult.Subject)
	}

	duration := time.Since(start).Seconds()

	// Record comprehensive metrics
	if p.metrics != nil {
		p.metrics.IncForEachIterations("nats", result.TotalElements)
		p.metrics.IncForEachFiltered("nats", result.FilteredElements)
		p.metrics.IncForEachActionsGenerated("nats", result.ProcessedElements)
		p.metrics.ObserveForEachDuration("nats", duration)
		
		// Track error types
		for _, elemErr := range result.Errors {
			p.metrics.IncForEachElementErrors("nats", elemErr.ErrorType)
		}
	}

	p.logger.Info("forEach processing complete",
		"field", action.ForEach,
		"totalElements", result.TotalElements,
		"processedElements", result.ProcessedElements,
		"filteredElements", result.FilteredElements,
		"failedElements", result.FailedElements,
		"actionsGenerated", len(actions),
		"duration", fmt.Sprintf("%.3fs", duration))

	return actions, nil
}

// processHTTPAction processes an HTTP action with template substitution
// Returns multiple actions when forEach is used
func (p *Processor) processHTTPAction(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	if action.ForEach != "" {
		return p.processHTTPActionWithForEach(action, context)
	}

	result := &HTTPAction{
		Passthrough: action.Passthrough,
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

	if action.Passthrough {
		result.RawPayload = context.RawPayload
	} else {
		payload, err := p.templater.Execute(action.Payload, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template payload: %w", err)
		}
		result.Payload = payload
	}

	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	return []*Action{{HTTP: result}}, nil
}

// processHTTPActionWithForEach processes an HTTP action with forEach iteration
// Reuses element context for both filter and templating
func (p *Processor) processHTTPActionWithForEach(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	start := time.Now()
	
	p.logger.Debug("processing HTTP action with forEach",
		"forEachField", action.ForEach)

	// Extract the field path from template braces
	// forEach: "{data.readings}" → "data.readings"
	fieldPath := extractForEachField(action.ForEach)
	if fieldPath == "" {
		return nil, fmt.Errorf("invalid forEach template syntax (must be {field}): %s", action.ForEach)
	}
	
	p.logger.Debug("extracted forEach field path",
		"template", action.ForEach,
		"extractedPath", fieldPath)

	// Use brace-aware path splitting on the EXTRACTED path
	arrayPath, err := SplitPathRespectingBraces(fieldPath)
	if err != nil {
		return nil, fmt.Errorf("invalid forEach path '%s': %w", fieldPath, err)
	}

	arrayValue, err := context.traverser.TraversePath(context.Msg, arrayPath)
	if err != nil {
		return nil, fmt.Errorf("forEach field not found: %s: %w", action.ForEach, err)
	}

	arrayItems, ok := arrayValue.([]interface{})
	if !ok {
		return nil, fmt.Errorf("forEach field is not an array: %s (type: %T)", action.ForEach, arrayValue)
	}

	// Enforce iteration limit
	if p.maxForEachIters > 0 && len(arrayItems) > p.maxForEachIters {
		return nil, fmt.Errorf("forEach array exceeds limit: %d elements > %d max iterations (configure maxForEachIterations to increase)",
			len(arrayItems), p.maxForEachIters)
	}

	p.logger.Debug("forEach array extracted",
		"field", action.ForEach,
		"arrayLength", len(arrayItems),
		"maxIterations", p.maxForEachIters)

	// Track detailed results
	result := &ForEachResult{
		TotalElements: len(arrayItems),
		Errors:        make([]ElementError, 0),
	}
	
	var actions []*Action

	for i, item := range arrayItems {
		// Wrap primitives, pass objects through
		itemMap := ensureObject(item)
		
		// Log if we wrapped a primitive (helpful for debugging)
		if _, ok := item.(map[string]interface{}); !ok {
			p.logger.Debug("forEach element wrapped as primitive",
				"field", action.ForEach,
				"index", i,
				"originalType", fmt.Sprintf("%T", item),
				"accessVia", "@value")
		}

		// Create context once and reuse for both filter and templating
		elementContext, err := p.createElementContextOptimized(itemMap, context)
		if err != nil {
			p.logger.Error("failed to create element context",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "context_creation_failed",
				Error:     err,
			})
			continue
		}

		// Apply filter if present (using the same context)
		if action.Filter != nil {
			if !p.evaluator.Evaluate(action.Filter, elementContext) {
				p.logger.Debug("forEach element filtered out",
					"field", action.ForEach,
					"index", i)
				result.FilteredElements++
				continue
			}
		}

		// Template the action (reusing the same context)
		actionResult := &HTTPAction{
			Passthrough: action.Passthrough,
			Retry:       action.Retry,
		}

		url, err := p.templater.Execute(action.URL, elementContext)
		if err != nil {
			p.logger.Error("failed to template URL for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "template_url_failed",
				Error:     err,
			})
			continue
		}
		actionResult.URL = url

		method, err := p.templater.Execute(action.Method, elementContext)
		if err != nil {
			p.logger.Error("failed to template method for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "template_method_failed",
				Error:     err,
			})
			continue
		}
		actionResult.Method = method

		if action.Passthrough {
			// Handle marshal errors gracefully
			rawPayload, err := safeMarshal(itemMap)
			if err != nil {
				p.logger.Error("failed to marshal element for passthrough",
					"field", action.ForEach,
					"index", i,
					"error", err)
				result.FailedElements++
				result.Errors = append(result.Errors, ElementError{
					Index:     i,
					ErrorType: "marshal_failed",
					Error:     err,
				})
				continue
			}
			actionResult.RawPayload = rawPayload
		} else {
			payload, err := p.templater.Execute(action.Payload, elementContext)
			if err != nil {
				p.logger.Error("failed to template payload for forEach element",
					"field", action.ForEach,
					"index", i,
					"error", err)
				result.FailedElements++
				result.Errors = append(result.Errors, ElementError{
					Index:     i,
					ErrorType: "template_payload_failed",
					Error:     err,
				})
				continue
			}
			actionResult.Payload = payload
		}

		actionResult.Headers, err = p.templateHeaders(action.Headers, elementContext)
		if err != nil {
			p.logger.Error("failed to template headers for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			result.FailedElements++
			result.Errors = append(result.Errors, ElementError{
				Index:     i,
				ErrorType: "template_headers_failed",
				Error:     err,
			})
			continue
		}

		actions = append(actions, &Action{HTTP: actionResult})
		result.ProcessedElements++
		
		p.logger.Debug("forEach element processed successfully",
			"field", action.ForEach,
			"index", i,
			"url", actionResult.URL)
	}

	duration := time.Since(start).Seconds()

	// Record comprehensive metrics
	if p.metrics != nil {
		p.metrics.IncForEachIterations("http", result.TotalElements)
		p.metrics.IncForEachFiltered("http", result.FilteredElements)
		p.metrics.IncForEachActionsGenerated("http", result.ProcessedElements)
		p.metrics.ObserveForEachDuration("http", duration)
		
		// Track error types
		for _, elemErr := range result.Errors {
			p.metrics.IncForEachElementErrors("http", elemErr.ErrorType)
		}
	}

	p.logger.Info("forEach processing complete",
		"field", action.ForEach,
		"totalElements", result.TotalElements,
		"processedElements", result.ProcessedElements,
		"filteredElements", result.FilteredElements,
		"failedElements", result.FailedElements,
		"actionsGenerated", len(actions),
		"duration", fmt.Sprintf("%.3fs", duration))

	return actions, nil
}

// createElementContextOptimized creates a context for array element WITHOUT marshal/unmarshal
// Avoids JSON serialization roundtrip (5-10x faster)
func (p *Processor) createElementContextOptimized(element map[string]interface{}, originalContext *EvaluationContext) (*EvaluationContext, error) {
	// Direct assignment without marshal/unmarshal cycle
	elementContext := &EvaluationContext{
		Msg:             element,                      // Current element
		OriginalMsg:     originalContext.OriginalMsg,  // CRITICAL: Preserve root for @msg
		RawPayload:      originalContext.RawPayload,   // Not used in forEach, but keep for consistency
		Headers:         originalContext.Headers,
		Subject:         originalContext.Subject,
		HTTP:            originalContext.HTTP,
		Time:            originalContext.Time,
		KV:              originalContext.KV,
		traverser:       originalContext.traverser,
		sigVerification: originalContext.sigVerification,
		sigChecked:      originalContext.sigChecked,
		sigValid:        originalContext.sigValid,
		signerPublicKey: originalContext.signerPublicKey,
		logger:          p.logger,
	}

	return elementContext, nil
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

// ProcessWithSubject is kept for backward compatibility
func (p *Processor) ProcessWithSubject(subject string, payload []byte, headers map[string]string) ([]*Action, error) {
	return p.ProcessNATS(subject, payload, headers)
}

// Process is kept for backward compatibility
func (p *Processor) Process(subject string, payload []byte) ([]*Action, error) {
	return p.ProcessNATS(subject, payload, nil)
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

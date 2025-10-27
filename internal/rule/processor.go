// file: internal/rule/processor.go

package rule

import (
	json "github.com/goccy/go-json"
	"fmt"
	"strings"
	"sync/atomic"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

type Processor struct {
	index           *RuleIndex
	allRules        []*Rule                // Store all rules for http-gateway
	httpPathIndex   map[string][]*Rule     // O(1) lookup by HTTP path
	timeProvider    TimeProvider
	kvContext       *KVContext
	logger          *logger.Logger
	metrics         *metrics.Metrics
	stats           ProcessorStats
	evaluator       *Evaluator
	templater       *TemplateEngine
	sigVerification *SignatureVerification
}

type ProcessorStats struct {
	Processed uint64
	Matched   uint64
	Errors    uint64
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

// LoadRules loads rules and indexes them by trigger type
func (p *Processor) LoadRules(rules []Rule) error {
	p.logger.Info("loading rules into processor", "ruleCount", len(rules))
	
	// Clear existing indexes
	p.index.Clear()
	p.allRules = make([]*Rule, 0, len(rules))
	p.httpPathIndex = make(map[string][]*Rule)
	
	natsCount := 0
	httpCount := 0
	
	for i := range rules {
		rule := &rules[i]
		
		// Store ALL rules for GetAllRules()
		p.allRules = append(p.allRules, rule)
		
		// Index NATS-triggered rules for fast lookup
		if rule.Trigger.NATS != nil {
			p.index.Add(rule)
			natsCount++
		}
		
		// Index HTTP-triggered rules by path for O(1) lookup
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

	// Create evaluation context with NATS subject context
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

	// Create evaluation context with HTTP request context
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
			// NEW: processAction now returns a slice of actions (for forEach support)
			actionResults, err := p.processAction(&rule.Action, context)
			if err != nil {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("error")
				}
				p.logger.Error("failed to process action", "error", err, "triggerType", triggerType)
				continue
			}
			
			// Track metrics for each action generated
			for _, action := range actionResults {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("success")
					p.metrics.IncRuleMatches()
					
					// Track action type
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
			
			// NEW: Flatten multiple actions from forEach into results
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
// NEW: Returns a slice of actions to support forEach
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
// NEW: Returns multiple actions when forEach is used
func (p *Processor) processNATSAction(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	// NEW: Check if action has forEach
	if action.ForEach != "" {
		return p.processNATSActionWithForEach(action, context)
	}

	// Original single-action logic
	result := &NATSAction{
		Passthrough: action.Passthrough,
	}

	// Template subject
	subject, err := p.templater.Execute(action.Subject, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template subject: %w", err)
	}
	result.Subject = subject

	// Handle payload
	if action.Passthrough {
		result.RawPayload = context.RawPayload
	} else {
		payload, err := p.templater.Execute(action.Payload, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template payload: %w", err)
		}
		result.Payload = payload
	}

	// Template headers
	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	return []*Action{{NATS: result}}, nil
}

// processNATSActionWithForEach processes a NATS action with forEach iteration
// NEW: Generates multiple actions by iterating over an array
func (p *Processor) processNATSActionWithForEach(action *NATSAction, context *EvaluationContext) ([]*Action, error) {
	p.logger.Debug("processing NATS action with forEach",
		"forEachField", action.ForEach)

	// 1. Extract array from message using JSONPath traversal
	arrayPath := strings.Split(action.ForEach, ".")
	arrayValue, err := context.traverser.TraversePath(context.Msg, arrayPath)
	if err != nil {
		return nil, fmt.Errorf("forEach field not found: %s: %w", action.ForEach, err)
	}

	// 2. Ensure the value is an array
	arrayItems, ok := arrayValue.([]interface{})
	if !ok {
		return nil, fmt.Errorf("forEach field is not an array: %s (type: %T)", action.ForEach, arrayValue)
	}

	p.logger.Debug("forEach array extracted",
		"field", action.ForEach,
		"arrayLength", len(arrayItems))

	var actions []*Action

	// 3. Iterate over array elements
	for i, item := range arrayItems {
		// Convert element to map
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			p.logger.Warn("forEach array element is not an object, skipping",
				"field", action.ForEach,
				"index", i,
				"elementType", fmt.Sprintf("%T", item))
			continue
		}

		// 4. Apply filter if present
		if action.Filter != nil {
			elementContext, err := p.createElementContext(itemMap, context)
			if err != nil {
				p.logger.Error("failed to create element context for filter",
					"field", action.ForEach,
					"index", i,
					"error", err)
				continue
			}

			// Evaluate filter conditions
			if !p.evaluator.Evaluate(action.Filter, elementContext) {
				p.logger.Debug("forEach element filtered out",
					"field", action.ForEach,
					"index", i)
				continue
			}
		}

		// 5. Create element context for templating
		elementContext, err := p.createElementContext(itemMap, context)
		if err != nil {
			p.logger.Error("failed to create element context for templating",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}

		// 6. Template the action with element context
		result := &NATSAction{
			Passthrough: action.Passthrough,
		}

		// Template subject
		subject, err := p.templater.Execute(action.Subject, elementContext)
		if err != nil {
			p.logger.Error("failed to template subject for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}
		result.Subject = subject

		// Handle payload
		if action.Passthrough {
			// Passthrough mode: use element as payload
			result.RawPayload = mustMarshal(itemMap)
		} else {
			// Template mode: render template with element context
			payload, err := p.templater.Execute(action.Payload, elementContext)
			if err != nil {
				p.logger.Error("failed to template payload for forEach element",
					"field", action.ForEach,
					"index", i,
					"error", err)
				continue
			}
			result.Payload = payload
		}

		// Template headers
		result.Headers, err = p.templateHeaders(action.Headers, elementContext)
		if err != nil {
			p.logger.Error("failed to template headers for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}

		actions = append(actions, &Action{NATS: result})
		
		p.logger.Debug("forEach element processed successfully",
			"field", action.ForEach,
			"index", i,
			"subject", result.Subject)
	}

	p.logger.Info("forEach processing complete",
		"field", action.ForEach,
		"totalElements", len(arrayItems),
		"actionsGenerated", len(actions))

	return actions, nil
}

// processHTTPAction processes an HTTP action with template substitution
// NEW: Returns multiple actions when forEach is used
func (p *Processor) processHTTPAction(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	// NEW: Check if action has forEach
	if action.ForEach != "" {
		return p.processHTTPActionWithForEach(action, context)
	}

	// Original single-action logic
	result := &HTTPAction{
		Passthrough: action.Passthrough,
		Retry:       action.Retry,
	}

	// Template URL
	url, err := p.templater.Execute(action.URL, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template URL: %w", err)
	}
	result.URL = url

	// Template method
	method, err := p.templater.Execute(action.Method, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template method: %w", err)
	}
	result.Method = method

	// Handle payload
	if action.Passthrough {
		result.RawPayload = context.RawPayload
	} else {
		payload, err := p.templater.Execute(action.Payload, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template payload: %w", err)
		}
		result.Payload = payload
	}

	// Template headers
	result.Headers, err = p.templateHeaders(action.Headers, context)
	if err != nil {
		return nil, fmt.Errorf("failed to template headers: %w", err)
	}

	return []*Action{{HTTP: result}}, nil
}

// processHTTPActionWithForEach processes an HTTP action with forEach iteration
// NEW: Generates multiple actions by iterating over an array
func (p *Processor) processHTTPActionWithForEach(action *HTTPAction, context *EvaluationContext) ([]*Action, error) {
	p.logger.Debug("processing HTTP action with forEach",
		"forEachField", action.ForEach)

	// 1. Extract array from message
	arrayPath := strings.Split(action.ForEach, ".")
	arrayValue, err := context.traverser.TraversePath(context.Msg, arrayPath)
	if err != nil {
		return nil, fmt.Errorf("forEach field not found: %s: %w", action.ForEach, err)
	}

	// 2. Ensure the value is an array
	arrayItems, ok := arrayValue.([]interface{})
	if !ok {
		return nil, fmt.Errorf("forEach field is not an array: %s (type: %T)", action.ForEach, arrayValue)
	}

	p.logger.Debug("forEach array extracted",
		"field", action.ForEach,
		"arrayLength", len(arrayItems))

	var actions []*Action

	// 3. Iterate over array elements
	for i, item := range arrayItems {
		// Convert element to map
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			p.logger.Warn("forEach array element is not an object, skipping",
				"field", action.ForEach,
				"index", i,
				"elementType", fmt.Sprintf("%T", item))
			continue
		}

		// 4. Apply filter if present
		if action.Filter != nil {
			elementContext, err := p.createElementContext(itemMap, context)
			if err != nil {
				p.logger.Error("failed to create element context for filter",
					"field", action.ForEach,
					"index", i,
					"error", err)
				continue
			}

			// Evaluate filter conditions
			if !p.evaluator.Evaluate(action.Filter, elementContext) {
				p.logger.Debug("forEach element filtered out",
					"field", action.ForEach,
					"index", i)
				continue
			}
		}

		// 5. Create element context for templating
		elementContext, err := p.createElementContext(itemMap, context)
		if err != nil {
			p.logger.Error("failed to create element context for templating",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}

		// 6. Template the action with element context
		result := &HTTPAction{
			Passthrough: action.Passthrough,
			Retry:       action.Retry,
		}

		// Template URL
		url, err := p.templater.Execute(action.URL, elementContext)
		if err != nil {
			p.logger.Error("failed to template URL for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}
		result.URL = url

		// Template method
		method, err := p.templater.Execute(action.Method, elementContext)
		if err != nil {
			p.logger.Error("failed to template method for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}
		result.Method = method

		// Handle payload
		if action.Passthrough {
			// Passthrough mode: use element as payload
			result.RawPayload = mustMarshal(itemMap)
		} else {
			// Template mode: render template with element context
			payload, err := p.templater.Execute(action.Payload, elementContext)
			if err != nil {
				p.logger.Error("failed to template payload for forEach element",
					"field", action.ForEach,
					"index", i,
					"error", err)
				continue
			}
			result.Payload = payload
		}

		// Template headers
		result.Headers, err = p.templateHeaders(action.Headers, context)
		if err != nil {
			p.logger.Error("failed to template headers for forEach element",
				"field", action.ForEach,
				"index", i,
				"error", err)
			continue
		}

		actions = append(actions, &Action{HTTP: result})
		
		p.logger.Debug("forEach element processed successfully",
			"field", action.ForEach,
			"index", i,
			"url", result.URL)
	}

	p.logger.Info("forEach processing complete",
		"field", action.ForEach,
		"totalElements", len(arrayItems),
		"actionsGenerated", len(actions))

	return actions, nil
}

// createElementContext creates a new evaluation context for an array element during forEach
// CRITICAL: Preserves reference to OriginalMsg for @msg prefix support
func (p *Processor) createElementContext(element map[string]interface{}, originalContext *EvaluationContext) (*EvaluationContext, error) {
	// Marshal element to bytes for context creation
	elementBytes, err := json.Marshal(element)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal array element: %w", err)
	}

	// Create new context with element as the current message
	elementContext, err := NewEvaluationContext(
		elementBytes,
		originalContext.Headers,
		originalContext.Subject,
		originalContext.HTTP,
		originalContext.Time,
		originalContext.KV,
		originalContext.sigVerification,
		p.logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create element context: %w", err)
	}

	// CRITICAL: Preserve reference to original message for @msg prefix
	// This allows templates to access root message fields via @msg.field
	elementContext.OriginalMsg = originalContext.OriginalMsg

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

// mustMarshal marshals a value to JSON, panicking on error
// Used in contexts where marshaling should never fail (already valid JSON objects)
func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// This should never happen with valid map[string]interface{} values
		panic(fmt.Sprintf("mustMarshal failed: %v", err))
	}
	return b
}

// ProcessWithSubject is kept for backward compatibility (delegates to ProcessNATS)
func (p *Processor) ProcessWithSubject(subject string, payload []byte, headers map[string]string) ([]*Action, error) {
	return p.ProcessNATS(subject, payload, headers)
}

// Process is kept for backward compatibility (delegates to ProcessNATS)
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

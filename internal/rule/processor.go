// file: internal/rule/processor.go

package rule

import (
	"fmt"
	"sync/atomic"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

type Processor struct {
	index           *RuleIndex
	allRules        []*Rule                // Store all rules for http-gateway
	httpPathIndex   map[string][]*Rule     // NEW: O(1) lookup by HTTP path
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
		httpPathIndex:   make(map[string][]*Rule), // NEW: Initialize HTTP path index
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
	p.httpPathIndex = make(map[string][]*Rule) // NEW: Clear HTTP index
	
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
		
		// NEW: Index HTTP-triggered rules by path for O(1) lookup
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
		"httpPaths", len(p.httpPathIndex)) // NEW: Log unique path count
	
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
// Used by http-gateway to enumerate rules for subscription setup
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
		NewSubjectContext(subject), // NATS context
		nil,                         // No HTTP context
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

	// NEW: O(1) lookup by path using index
	rules := p.findHTTPRules(path, method)
	if len(rules) == 0 {
		return nil, nil
	}

	// Create evaluation context with HTTP request context
	context, err := NewEvaluationContext(
		payload,
		headers,
		nil,                                  // No NATS context
		NewHTTPRequestContext(path, method), // HTTP context
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
// NEW: O(1) lookup using httpPathIndex map
func (p *Processor) findHTTPRules(path, method string) []*Rule {
	// O(1) lookup by path
	rulesForPath, exists := p.httpPathIndex[path]
	if !exists || len(rulesForPath) == 0 {
		p.logger.Debug("no HTTP rules for path", "path", path)
		return nil
	}
	
	// If no method filtering needed, return all rules for this path
	if method == "" {
		p.logger.Debug("HTTP rule lookup complete (all methods)",
			"path", path,
			"matchedRules", len(rulesForPath))
		return rulesForPath
	}
	
	// Filter by method if specified in rule
	// Note: Rules with empty method match ALL methods
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
// Shared logic for both NATS and HTTP processing
func (p *Processor) evaluateRules(rules []*Rule, context *EvaluationContext, triggerType string) ([]*Action, error) {
	var actions []*Action

	for _, rule := range rules {
		p.logger.Debug("evaluating rule", "triggerType", triggerType)

		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
			action, err := p.processAction(&rule.Action, context)
			if err != nil {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("error")
				}
				p.logger.Error("failed to process action", "error", err, "triggerType", triggerType)
				continue
			}
			
			if p.metrics != nil {
				p.metrics.IncTemplateOpsTotal("success")
				p.metrics.IncRuleMatches()
				
				// Track action type for metrics
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
			
			actions = append(actions, action)
		}
	}

	atomic.AddUint64(&p.stats.Processed, 1)
	if len(actions) > 0 {
		atomic.AddUint64(&p.stats.Matched, 1)
	}
	return actions, nil
}

// processAction processes an action (NATS or HTTP)
func (p *Processor) processAction(action *Action, context *EvaluationContext) (*Action, error) {
	processedAction := &Action{}

	if action.NATS != nil {
		natsAction, err := p.processNATSAction(action.NATS, context)
		if err != nil {
			return nil, err
		}
		processedAction.NATS = natsAction
	} else if action.HTTP != nil {
		httpAction, err := p.processHTTPAction(action.HTTP, context)
		if err != nil {
			return nil, err
		}
		processedAction.HTTP = httpAction
	} else {
		return nil, fmt.Errorf("action has no NATS or HTTP configuration")
	}

	return processedAction, nil
}

// processNATSAction processes a NATS action with template substitution
func (p *Processor) processNATSAction(action *NATSAction, context *EvaluationContext) (*NATSAction, error) {
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

	return result, nil
}

// processHTTPAction processes an HTTP action with template substitution
func (p *Processor) processHTTPAction(action *HTTPAction, context *EvaluationContext) (*HTTPAction, error) {
	result := &HTTPAction{
		Passthrough: action.Passthrough,
		Retry:       action.Retry, // Copy retry config as-is
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

	return result, nil
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

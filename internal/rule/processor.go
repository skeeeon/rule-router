// file: internal/rule/processor.go

package rule

import (
	"fmt"
	"sync/atomic"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

type Processor struct {
	index            *RuleIndex
	timeProvider     TimeProvider
	kvContext        *KVContext
	logger           *logger.Logger
	metrics          *metrics.Metrics
	stats            ProcessorStats
	evaluator        *Evaluator
	templater        *TemplateEngine
	sigVerification  *SignatureVerification // NEW
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

func (p *Processor) LoadRules(rules []Rule) error {
	p.logger.Info("loading rules into processor", "ruleCount", len(rules))
	p.index.Clear()
	for i := range rules {
		p.index.Add(&rules[i])
	}
	if p.metrics != nil {
		exactCount, patternCount := p.index.GetRuleCounts()
		p.metrics.SetRulesActive(float64(exactCount + patternCount))
	}
	return nil
}

func (p *Processor) GetSubjects() []string {
	return p.index.GetSubscriptionSubjects()
}

// ProcessWithSubject orchestrates the evaluation of a message.
func (p *Processor) ProcessWithSubject(actualSubject string, payload []byte, headers map[string]string) ([]*Action, error) {
	p.logger.Debug("processing message with subject context", "actualSubject", actualSubject, "payloadSize", len(payload))

	rules := p.index.FindAllMatching(actualSubject)
	if len(rules) == 0 {
		return nil, nil
	}

	// Create the context ONCE with signature verification config
	context, err := NewEvaluationContext(
		payload,
		headers,
		NewSubjectContext(actualSubject),
		p.timeProvider.GetCurrentContext(),
		p.kvContext,
		p.sigVerification, // NEW: Pass signature verification config
		p.logger,          // NEW: Pass logger for signature verification
	)
	if err != nil {
		atomic.AddUint64(&p.stats.Errors, 1)
		if p.metrics != nil {
			p.metrics.IncMessagesTotal("error")
		}
		p.logger.Error("failed to create evaluation context", "error", err, "subject", actualSubject)
		return nil, err
	}

	var actions []*Action
	for _, rule := range rules {
		p.logger.Debug("evaluating rule", "rulePattern", rule.Subject, "actualSubject", actualSubject)

		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
			action, err := p.processAction(rule.Action, context)
			if err != nil {
				if p.metrics != nil {
					p.metrics.IncTemplateOpsTotal("error")
				}
				p.logger.Error("failed to process action template", "error", err, "rulePattern", rule.Subject)
				continue
			}
			if p.metrics != nil {
				p.metrics.IncTemplateOpsTotal("success")
				p.metrics.IncRuleMatches()
				if action.Passthrough {
					p.metrics.IncActionsByType("passthrough")
				} else {
					p.metrics.IncActionsByType("templated")
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

// processAction uses the TemplateEngine to render the final action, including headers.
func (p *Processor) processAction(action *Action, context *EvaluationContext) (*Action, error) {
	processedAction := &Action{
		Passthrough: action.Passthrough,
	}

	// Template the subject
	subject, err := p.templater.Execute(action.Subject, context)
	if err != nil {
		return nil, fmt.Errorf("failed to process subject template: %w", err)
	}
	processedAction.Subject = subject

	// Handle payload
	if action.Passthrough {
		processedAction.RawPayload = context.RawPayload
	} else {
		payload, err := p.templater.Execute(action.Payload, context)
		if err != nil {
			return nil, fmt.Errorf("failed to process payload template: %w", err)
		}
		processedAction.Payload = payload
	}

	// Process headers
	processedAction.Headers, err = p.processHeaders(action, context)
	if err != nil {
		return nil, fmt.Errorf("failed to process headers: %w", err)
	}

	return processedAction, nil
}

// processHeaders templates header values and merges with original headers in passthrough mode.
func (p *Processor) processHeaders(action *Action, context *EvaluationContext) (map[string]string, error) {
	if len(action.Headers) == 0 && !action.Passthrough {
		return nil, nil
	}

	result := make(map[string]string)

	if action.Passthrough && context.Headers != nil {
		for key, value := range context.Headers {
			result[key] = value
		}
	}

	for key, valueTemplate := range action.Headers {
		processedValue, err := p.templater.Execute(valueTemplate, context)
		if err != nil {
			return nil, fmt.Errorf("failed to template header '%s': %w", key, err)
		}
		result[key] = processedValue
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

// Process maintains backward compatibility.
func (p *Processor) Process(subject string, payload []byte) ([]*Action, error) {
	return p.ProcessWithSubject(subject, payload, nil)
}

func (p *Processor) GetStats() ProcessorStats {
	return ProcessorStats{
		Processed: atomic.LoadUint64(&p.stats.Processed),
		Matched:   atomic.LoadUint64(&p.stats.Matched),
		Errors:    atomic.LoadUint64(&p.stats.Errors),
	}
}

func (p *Processor) SetTimeProvider(tp TimeProvider) {
	if tp != nil {
		p.timeProvider = tp
	}
}

func (p *Processor) Close() {
	p.logger.Info("shutting down processor")
}

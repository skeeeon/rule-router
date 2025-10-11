//file: internal/rule/processor.go

package rule

import (
	"fmt"
	"sync/atomic"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

type Processor struct {
	index        *RuleIndex
	timeProvider TimeProvider
	kvContext    *KVContext
	logger       *logger.Logger
	metrics      *metrics.Metrics
	stats        ProcessorStats
	evaluator    *Evaluator      // New
	templater    *TemplateEngine // New
}

type ProcessorStats struct {
	Processed uint64
	Matched   uint64
	Errors    uint64
}

func NewProcessor(log *logger.Logger, metrics *metrics.Metrics, kvCtx *KVContext) *Processor {
	p := &Processor{
		index:        NewRuleIndex(log),
		timeProvider: NewSystemTimeProvider(),
		kvContext:    kvCtx,
		logger:       log,
		metrics:      metrics,
		evaluator:    NewEvaluator(log),
		templater:    NewTemplateEngine(log),
	}

	if kvCtx != nil {
		p.logger.Info("initializing processor with KV support", "buckets", kvCtx.GetAllBuckets())
	} else {
		p.logger.Info("initializing processor without KV support")
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

	// 1. Create the context ONCE.
	context, err := NewEvaluationContext(
		payload,
		headers,
		NewSubjectContext(actualSubject),
		p.timeProvider.GetCurrentContext(),
		p.kvContext,
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

		// 2. Pass context to the Evaluator.
		if rule.Conditions == nil || p.evaluator.Evaluate(rule.Conditions, context) {
			// 3. Pass context to the TemplateEngine via processAction.
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

// processAction uses the TemplateEngine to render the final action.
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

	return processedAction, nil
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

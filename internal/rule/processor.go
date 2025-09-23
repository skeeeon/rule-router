//file: internal/rule/processor.go

package rule

import (
    json "github.com/goccy/go-json"
    "fmt"
    "regexp"
    "strconv"
    "strings"
    "sync/atomic"
    "time"

    "github.com/google/uuid"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
)

// Pre-compiled regex patterns for template processing (clean and efficient)
var (
    // System fields and functions: {@time.hour}, {@uuid7()}, {@timestamp()}, {@subject.1}, {@kv.bucket.key}
    systemFieldPattern = regexp.MustCompile(`\{@([^}]+)\}`)
    
    // Regular message variables: {temperature}, {sensor.location}
    messageVarPattern = regexp.MustCompile(`\{([^}@]+)\}`)
)

type Processor struct {
    index        *RuleIndex
    timeProvider TimeProvider
    kvContext    *KVContext  // Add KV context
    logger       *logger.Logger
    metrics      *metrics.Metrics
    stats        ProcessorStats
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
        kvContext:    kvCtx,  // Store KV context (can be nil if KV is disabled)
        logger:       log,
        metrics:      metrics,
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
        rule := &rules[i]
        p.index.Add(rule)
    }

    if p.metrics != nil {
        exactCount, patternCount := p.index.GetRuleCounts()
        p.metrics.SetRulesActive(float64(exactCount + patternCount))
    }
    
    p.logger.Info("rules loaded successfully",
        "totalRules", len(rules),
        "exactRules", func() int { e, _ := p.index.GetRuleCounts(); return e }(),
        "patternRules", func() int { _, p := p.index.GetRuleCounts(); return p }())
    
    return nil
}

func (p *Processor) GetSubjects() []string {
    subjects := p.index.GetSubscriptionSubjects()
    p.logger.Debug("retrieved subscription subjects from index", "subjectCount", len(subjects))
    return subjects
}

// Process processes a message against rules using the actual NATS subject
// This is the NEW enhanced method that supports wildcards
func (p *Processor) ProcessWithSubject(actualSubject string, payload []byte) ([]*Action, error) {
    p.logger.Debug("processing message with subject context",
        "actualSubject", actualSubject,
        "payloadSize", len(payload))

    // Get time context once for this message processing
    timeCtx := p.timeProvider.GetCurrentContext()

    // Create subject context for template and condition access
    subjectCtx := NewSubjectContext(actualSubject)

    // Find ALL matching rules (exact + patterns)
    rules := p.index.FindAllMatching(actualSubject)
    if len(rules) == 0 {
        p.logger.Debug("no matching rules found for subject", "subject", actualSubject)
        return nil, nil
    }

    p.logger.Debug("found matching rules",
        "subject", actualSubject,
        "matchingRules", len(rules))

    // Parse message payload
    var msgValues map[string]interface{}
    if err := json.Unmarshal(payload, &msgValues); err != nil {
        atomic.AddUint64(&p.stats.Errors, 1)
        if p.metrics != nil {
            p.metrics.IncMessagesTotal("error")
        }
        p.logger.Error("failed to unmarshal message",
            "error", err,
            "subject", actualSubject)
        return nil, fmt.Errorf("failed to unmarshal message: %w", err)
    }

    // Process each matching rule
    var actions []*Action
    for _, rule := range rules {
        p.logger.Debug("evaluating rule",
            "rulePattern", rule.Subject,
            "actualSubject", actualSubject)

        // Evaluate conditions with subject context and KV context support
        if rule.Conditions == nil || p.evaluateConditions(rule.Conditions, msgValues, timeCtx, subjectCtx, p.kvContext) {
            // Process action template with subject context and KV context
            action, err := p.processActionTemplate(rule.Action, msgValues, timeCtx, subjectCtx, p.kvContext)
            if err != nil {
                if p.metrics != nil {
                    p.metrics.IncTemplateOpsTotal("error")
                }
                p.logger.Error("failed to process action template",
                    "error", err,
                    "rulePattern", rule.Subject,
                    "actualSubject", actualSubject)
                continue
            }
            if p.metrics != nil {
                p.metrics.IncTemplateOpsTotal("success")
                p.metrics.IncRuleMatches()
            }
            
            p.logger.Debug("rule matched and action created",
                "rulePattern", rule.Subject,
                "actualSubject", actualSubject,
                "actionSubject", action.Subject)
                
            actions = append(actions, action)
        } else {
            p.logger.Debug("rule conditions not met",
                "rulePattern", rule.Subject,
                "actualSubject", actualSubject)
        }
    }

    // Update stats
    atomic.AddUint64(&p.stats.Processed, 1)
    if len(actions) > 0 {
        atomic.AddUint64(&p.stats.Matched, 1)
    }

    p.logger.Debug("processing complete",
        "subject", actualSubject,
        "rulesEvaluated", len(rules),
        "actionsGenerated", len(actions))

    return actions, nil
}

// Process maintains backward compatibility for existing callers
func (p *Processor) Process(subject string, payload []byte) ([]*Action, error) {
    // For backward compatibility, use the subject as both pattern and actual subject
    return p.ProcessWithSubject(subject, payload)
}

func (p *Processor) processActionTemplate(action *Action, msg map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) (*Action, error) {
    processedAction := &Action{
        Subject: action.Subject,
        Payload: action.Payload,
    }

    // Process subject template if it contains variables
    if strings.Contains(action.Subject, "{") {
        subject, err := p.processTemplate(action.Subject, msg, timeCtx, subjectCtx, kvCtx)
        if err != nil {
            return nil, fmt.Errorf("failed to process subject template: %w", err)
        }
        processedAction.Subject = subject
    }

    // Process payload template
    payload, err := p.processTemplate(action.Payload, msg, timeCtx, subjectCtx, kvCtx)
    if err != nil {
        return nil, fmt.Errorf("failed to process payload template: %w", err)
    }
    processedAction.Payload = payload

    return processedAction, nil
}

// processTemplate handles the enhanced {@} syntax with subject and KV support
func (p *Processor) processTemplate(template string, data map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) (string, error) {
    result := template

    // Process system fields and functions: {@time.hour}, {@uuid7()}, {@timestamp()}, {@subject.1}, {@kv.bucket.key}
    result = systemFieldPattern.ReplaceAllStringFunc(result, func(match string) string {
        systemRef := match[2 : len(match)-1] // remove {@ and }, get "time.hour" or "uuid7()" or "subject.1" or "kv.bucket.key"
        
        if strings.HasSuffix(systemRef, "()") {
            // It's a function: uuid7(), timestamp()
            return p.processSystemFunction(systemRef)
        } else {
            // It's a system field: time.hour, day.name, subject.1, kv.bucket.key
            // NOW PASSING msgData for KV variable resolution
            return p.processSystemField("@"+systemRef, data, timeCtx, subjectCtx, kvCtx) // Add @ back for lookup
        }
    })

    // Process message variables: {temperature}, {sensor.location}
    result = messageVarPattern.ReplaceAllStringFunc(result, func(match string) string {
        fieldPath := match[1 : len(match)-1] // remove { and }
        return p.processMessageVariable(fieldPath, data)
    })

    return result, nil
}

// processSystemFunction handles system functions: uuid7(), timestamp()
func (p *Processor) processSystemFunction(function string) string {
    switch function {
    case "uuid4()":
        id := uuid.New()
        p.logger.Debug("generated UUIDv4", "uuid", id.String())
        return id.String()
    case "uuid7()":
        id, err := uuid.NewV7()
        if err != nil {
            p.logger.Error("failed to generate UUIDv7", "error", err)
            return ""
        }
        p.logger.Debug("generated UUIDv7", "uuid", id.String())
        return id.String()
    case "timestamp()":
        timestamp := time.Now().UTC().Format(time.RFC3339)
        p.logger.Debug("generated timestamp", "timestamp", timestamp)
        return timestamp
    default:
        p.logger.Debug("unknown system function", "function", function)
        return "{@" + function + "}" // Return original
    }
}

// processSystemField handles system time fields, subject fields, and KV fields
// NOTE: This method now receives msgData to support KV variable resolution
func (p *Processor) processSystemField(systemField string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) string {
    // Try KV context first (for @kv.bucket.key fields) - NOW WITH VARIABLE RESOLUTION!
    if strings.HasPrefix(systemField, "@kv") && kvCtx != nil {
        if value, exists := kvCtx.GetFieldWithContext(systemField, msgData, timeCtx, subjectCtx); exists {
            strValue := p.convertToString(value)
            p.logger.Debug("processed KV field", "field", systemField, "value", strValue)
            return strValue
        }
        p.logger.Debug("KV field not found", "field", systemField)
        return systemField // Return original if not found
    }
    
    // Try subject context (for @subject.X fields)
    if strings.HasPrefix(systemField, "@subject") {
        if value, exists := subjectCtx.GetField(systemField); exists {
            strValue := p.convertToString(value)
            p.logger.Debug("processed subject field", "field", systemField, "value", strValue)
            return strValue
        }
    }
    
    // Try time context (for @time.X, @date.X, etc.)
    if value, exists := timeCtx.GetField(systemField); exists {
        strValue := p.convertToString(value)
        p.logger.Debug("processed time field", "field", systemField, "value", strValue)
        return strValue
    }
    
    p.logger.Debug("unknown system field", "field", systemField)
    return systemField // Return original
}

// processMessageVariable handles message field access with nested paths
func (p *Processor) processMessageVariable(fieldPath string, data map[string]interface{}) string {
    path := strings.Split(fieldPath, ".")
    
    value, err := p.getValueFromPath(data, path) // Uses existing method from evaluator.go
    if err != nil {
        p.logger.Debug("message field not found", "path", fieldPath, "error", err)
        return "{" + fieldPath + "}" // Return original
    }
    
    strValue := p.convertToString(value)
    p.logger.Debug("processed message field", "path", fieldPath, "value", strValue)
    return strValue
}

func (p *Processor) convertToString(value interface{}) string {
    switch v := value.(type) {
    case string:
        return v
    case float64:
        return strconv.FormatFloat(v, 'f', -1, 64)
    case int:
        return strconv.Itoa(v)
    case bool:
        return strconv.FormatBool(v)
    case nil:
        return "null"
    case map[string]interface{}, []interface{}:
        jsonBytes, err := json.Marshal(v)
        if err != nil {
            p.logger.Debug("failed to marshal complex value to JSON", "error", err)
            return fmt.Sprintf("%v", v)
        }
        return string(jsonBytes)
    default:
        return fmt.Sprintf("%v", v)
    }
}

// getValueFromPath traverses a nested map using a path array
// This is the same logic used in template processing for consistency
func (p *Processor) getValueFromPath(data map[string]interface{}, path []string) (interface{}, error) {
    var current interface{} = data

    for _, key := range path {
        switch v := current.(type) {
        case map[string]interface{}:
            var ok bool
            current, ok = v[key]
            if !ok {
                return nil, fmt.Errorf("key not found: %s", key)
            }
        case map[interface{}]interface{}:
            var ok bool
            current, ok = v[key]
            if !ok {
                return nil, fmt.Errorf("key not found: %s", key)
            }
        default:
            return nil, fmt.Errorf("invalid path: %s is not a map", key)
        }
    }

    return current, nil
}

func (p *Processor) GetStats() ProcessorStats {
    stats := ProcessorStats{
        Processed: atomic.LoadUint64(&p.stats.Processed),
        Matched:   atomic.LoadUint64(&p.stats.Matched),
        Errors:    atomic.LoadUint64(&p.stats.Errors),
    }

    p.logger.Debug("processor stats retrieved",
        "processed", stats.Processed,
        "matched", stats.Matched,
        "errors", stats.Errors)

    return stats
}

func (p *Processor) Close() {
    p.logger.Info("shutting down processor")
}

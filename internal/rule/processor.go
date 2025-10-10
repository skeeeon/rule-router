//file: internal/rule/processor.go

package rule

import (
    "encoding/json"
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

// OPTIMIZED: Single regex pattern to capture all variables in one pass
// Matches both {@system.field} and {message.field} patterns
var (
    // Combined pattern: captures optional @ prefix and variable content
    // {@time.hour} -> groups: ["@", "time.hour"]  
    // {temperature} -> groups: ["", "temperature"]
    // {@kv.bucket.key:path} -> groups: ["@", "kv.bucket.key:path"]
    // Note: Includes colon (:) to support KV syntax with colon delimiter
    combinedVariablePattern = regexp.MustCompile(`\{(@?)([a-zA-Z0-9_.:()=/-]+)\}`)
)

type Processor struct {
    index        *RuleIndex
    timeProvider TimeProvider
    kvContext    *KVContext  // Add KV context
    logger       *logger.Logger
    metrics      *metrics.Metrics
    stats        ProcessorStats
    traverser    *JSONPathTraverser // Shared JSON path traverser
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
        traverser:    NewJSONPathTraverser(), // Use shared traverser
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
            // Pass raw payload to template processor
            action, err := p.processActionTemplate(
                rule.Action, 
                msgValues, 
                timeCtx, 
                subjectCtx, 
                p.kvContext,
                payload,  // NEW: Pass raw bytes
            )
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
                // NEW: Increment action type metric
                if action.Passthrough {
                    p.metrics.IncActionsByType("passthrough")
                } else {
                    p.metrics.IncActionsByType("templated")
                }
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

func (p *Processor) processActionTemplate(
    action *Action, 
    msg map[string]interface{}, 
    timeCtx *TimeContext, 
    subjectCtx *SubjectContext, 
    kvCtx *KVContext,
    rawPayload []byte,  // NEW PARAMETER: Pass raw bytes from ProcessWithSubject
) (*Action, error) {
    processedAction := &Action{
        Subject:     action.Subject,
        Payload:     action.Payload,
        Passthrough: action.Passthrough, // NEW: Copy flag
    }

    // Process subject template (always - even with passthrough)
    if strings.Contains(action.Subject, "{") {
        subject, err := p.processTemplate(action.Subject, msg, timeCtx, subjectCtx, kvCtx)
        if err != nil {
            return nil, fmt.Errorf("failed to process subject template: %w", err)
        }
        processedAction.Subject = subject
    }

    // NEW: Handle passthrough mode
    if action.Passthrough {
        processedAction.RawPayload = rawPayload  // Store original bytes
        processedAction.Payload = ""             // Clear payload string
        
        p.logger.Debug("action configured as passthrough, preserving original message",
            "subject", processedAction.Subject,
            "payloadSize", len(rawPayload))
        
        return processedAction, nil
    }

    // Existing template processing for payload
    payload, err := p.processTemplate(action.Payload, msg, timeCtx, subjectCtx, kvCtx)
    if err != nil {
        return nil, fmt.Errorf("failed to process payload template: %w", err)
    }
    processedAction.Payload = payload

    return processedAction, nil
}

// ENHANCED: Multi-pass template processing with nested variable support
// Pass 1: Resolve all NON-KV variables (message fields, subject, time, functions)
// Pass 2: Resolve KV variables (which now have their nested variables already resolved)
func (p *Processor) processTemplate(template string, data map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) (string, error) {
    if !strings.Contains(template, "{") {
        // No variables - return as-is
        return template, nil
    }

    // PASS 1: Resolve all NON-KV variables first
    // This allows nested variables like {@kv.bucket.{key}:{path}.field} to work
    result := combinedVariablePattern.ReplaceAllStringFunc(template, func(match string) string {
        // Parse the match: {@time.hour} or {temperature}
        submatches := combinedVariablePattern.FindStringSubmatch(match)
        if len(submatches) != 3 {
            p.logger.Debug("unexpected regex match format in pass 1", "match", match)
            return match // Keep original if parse fails
        }
        
        isSystemVar := submatches[1] == "@"  // Group 1: optional @ prefix
        varContent := submatches[2]          // Group 2: variable content
        
        // SKIP KV variables in pass 1 - they'll be resolved in pass 2
        // This is critical: KV variables may contain nested variables that need resolving first
        if isSystemVar && strings.HasPrefix(varContent, "kv.") {
            p.logger.Debug("skipping KV variable in pass 1, will resolve in pass 2", "match", match)
            return match // Keep KV variables unchanged for pass 2
        }
        
        p.logger.Debug("processing non-KV variable in pass 1",
            "match", match,
            "isSystem", isSystemVar,
            "content", varContent)

        // Resolve non-KV variables
        if isSystemVar {
            // System variable: @time.hour, @subject.1, @uuid7() (but NOT @kv)
            if strings.HasSuffix(varContent, "()") {
                // System function: timestamp(), uuid7()
                return p.processSystemFunction(varContent)
            } else {
                // System field: time.hour, subject.1 (but NOT kv.bucket.key:path)
                return p.processSystemField("@"+varContent, data, timeCtx, subjectCtx, kvCtx)
            }
        } else {
            // Message variable: temperature, sensor.location, readings.0.value
            return p.processMessageVariable(varContent, data)
        }
    })

    // PASS 2: Now resolve KV variables
    // At this point, all nested variables in KV fields have been resolved
    // Example: {@kv.bucket.{key}:{field}.path} → {@kv.bucket.sensor123:temperature.path}
    result = combinedVariablePattern.ReplaceAllStringFunc(result, func(match string) string {
        submatches := combinedVariablePattern.FindStringSubmatch(match)
        if len(submatches) != 3 {
            p.logger.Debug("unexpected regex match format in pass 2", "match", match)
            return "" // Return empty string for malformed variables
        }
        
        isSystemVar := submatches[1] == "@"
        varContent := submatches[2]
        
        // Only process KV variables in pass 2
        if isSystemVar && strings.HasPrefix(varContent, "kv.") {
            p.logger.Debug("processing KV variable in pass 2",
                "match", match,
                "content", varContent)
            // Now process the KV field with all nested variables already resolved
            return p.processSystemField("@"+varContent, data, timeCtx, subjectCtx, kvCtx)
        }
        
        // All non-KV variables should already be resolved in pass 1
        // If we see any here, they're malformed or the regex failed
        p.logger.Debug("unexpected non-KV variable in pass 2, returning empty", "match", match)
        return ""
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
            return "" // ENHANCED: Return empty string on error
        }
        p.logger.Debug("generated UUIDv7", "uuid", id.String())
        return id.String()
    case "timestamp()":
        timestamp := time.Now().UTC().Format(time.RFC3339)
        p.logger.Debug("generated timestamp", "timestamp", timestamp)
        return timestamp
    default:
        p.logger.Debug("unknown system function", "function", function)
        return "" // ENHANCED: Return empty string for unknown functions
    }
}

// ENHANCED: processSystemField with consistent empty string handling and WARN logging
func (p *Processor) processSystemField(systemField string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) string {
    // Try KV context first (for @kv.bucket.key:path fields) - WITH VARIABLE RESOLUTION!
    if strings.HasPrefix(systemField, "@kv") && kvCtx != nil {
        if value, exists := kvCtx.GetFieldWithContext(systemField, msgData, timeCtx, subjectCtx); exists {
            strValue := p.convertToString(value)
            p.logger.Debug("processed KV field", "field", systemField, "value", strValue)
            return strValue
        }
        // UPDATED: Changed from DEBUG to WARN for better visibility
        bucket, key := p.extractBucketAndKey(systemField)
        p.logger.Warn("KV field not found in template - substituting empty string",
            "field", systemField,
            "bucket", bucket,
            "key", key,
            "availableBuckets", kvCtx.GetAllBuckets(),
            "impact", "Template variable will be empty in output",
            "syntax", "Ensure format is @kv.bucket.key:path with colon delimiter")
        return ""
    }
    
    // Try subject context (for @subject.X fields)
    if strings.HasPrefix(systemField, "@subject") {
        if value, exists := subjectCtx.GetField(systemField); exists {
            strValue := p.convertToString(value)
            p.logger.Debug("processed subject field", "field", systemField, "value", strValue)
            return strValue
        }
        // ENHANCED: Return empty string for failed subject lookups  
        p.logger.Debug("subject field not found, returning empty string", "field", systemField)
        return ""
    }
    
    // Try time context (for @time.X, @date.X, etc.)
    if value, exists := timeCtx.GetField(systemField); exists {
        strValue := p.convertToString(value)
        p.logger.Debug("processed time field", "field", systemField, "value", strValue)
        return strValue
    }
    
    // ENHANCED: Return empty string for unknown system fields
    p.logger.Debug("unknown system field, returning empty string", "field", systemField)
    return ""
}

// ENHANCED: processMessageVariable with array support via shared traverser
func (p *Processor) processMessageVariable(fieldPath string, data map[string]interface{}) string {
    path := strings.Split(fieldPath, ".")
    
    // Use shared traverser for consistent array support!
    value, err := p.traverser.TraversePath(data, path)
    if err != nil {
        p.logger.Debug("message field not found, returning empty string", "path", fieldPath, "error", err)
        return "" // ENHANCED: Return empty string instead of original template
    }
    
    strValue := p.convertToString(value)
    p.logger.Debug("processed message field", "path", fieldPath, "value", strValue)
    return strValue
}

// Note: extractBucketAndKey helper method is defined in evaluator.go
// It's shared between processor.go and evaluator.go since both operate
// on the *Processor type and are in the same package

// ENHANCED: convertToString with better nil handling for templates
func (p *Processor) convertToString(value interface{}) string {
    switch v := value.(type) {
    case string:
        return v
    case float64:
        return strconv.FormatFloat(v, 'f', -1, 64)
    case int:
        return strconv.Itoa(v)
    case int64:
        return strconv.FormatInt(v, 10)
    case bool:
        return strconv.FormatBool(v)
    case nil:
        return "" // ENHANCED: Return empty string for nil values in templates
    case map[string]interface{}, []interface{}:
        jsonBytes, err := json.Marshal(v)
        if err != nil {
            p.logger.Debug("failed to marshal complex value to JSON, returning empty string", "error", err)
            return "" // ENHANCED: Return empty string instead of fmt.Sprintf fallback
        }
        return string(jsonBytes)
    default:
        // Try JSON marshaling first for unknown types
        if jsonBytes, err := json.Marshal(v); err == nil {
            return string(jsonBytes)
        }
        // Final fallback - convert to string representation
        return fmt.Sprintf("%v", v)
    }
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

// SetTimeProvider allows injecting a different time provider, primarily for testing.
func (p *Processor) SetTimeProvider(tp TimeProvider) {
	if tp != nil {
		p.timeProvider = tp
	}
}

func (p *Processor) Close() {
    p.logger.Info("shutting down processor")
}

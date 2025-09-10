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

// Pre-compiled regex patterns for template processing (clean and efficient)
var (
    // System fields and functions: {@time.hour}, {@uuid7()}, {@timestamp()}
    systemFieldPattern = regexp.MustCompile(`\{@([^}]+)\}`)
    
    // Regular message variables: {temperature}, {sensor.location}
    messageVarPattern = regexp.MustCompile(`\{([^}@]+)\}`)
)

type Processor struct {
    index        *RuleIndex
    timeProvider TimeProvider
    logger       *logger.Logger
    metrics      *metrics.Metrics
    stats        ProcessorStats
}

type ProcessorStats struct {
    Processed uint64
    Matched   uint64
    Errors    uint64
}

func NewProcessor(log *logger.Logger, metrics *metrics.Metrics) *Processor {
    p := &Processor{
        index:        NewRuleIndex(log),
        timeProvider: NewSystemTimeProvider(),
        logger:       log,
        metrics:      metrics,
    }

    p.logger.Info("initializing processor")
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
        p.metrics.SetRulesActive(float64(len(rules)))
    }
    p.logger.Info("rules loaded successfully", "count", len(rules))
    return nil
}

func (p *Processor) GetTopics() []string {
    topics := p.index.GetTopics()
    p.logger.Debug("retrieved topics from index", "topicCount", len(topics))
    return topics
}

// Process processes a message and returns actions to be executed
func (p *Processor) Process(topic string, payload []byte) ([]*Action, error) {
    p.logger.Debug("processing message",
        "topic", topic,
        "payloadSize", len(payload))

    // Get time context once for this message processing
    timeCtx := p.timeProvider.GetCurrentContext()

    // Find matching rules
    rules := p.index.Find(topic)
    if len(rules) == 0 {
        p.logger.Debug("no matching rules found for topic", "topic", topic)
        return nil, nil
    }

    // Parse message payload
    var msgValues map[string]interface{}
    if err := json.Unmarshal(payload, &msgValues); err != nil {
        atomic.AddUint64(&p.stats.Errors, 1)
        if p.metrics != nil {
            p.metrics.IncMessagesTotal("error")
        }
        p.logger.Error("failed to unmarshal message",
            "error", err,
            "topic", topic)
        return nil, fmt.Errorf("failed to unmarshal message: %w", err)
    }

    // Process each rule
    var actions []*Action
    for _, rule := range rules {
        // Evaluate conditions
        if rule.Conditions == nil || p.evaluateConditions(rule.Conditions, msgValues, timeCtx) {
            // Process action template
            action, err := p.processActionTemplate(rule.Action, msgValues, timeCtx)
            if err != nil {
                if p.metrics != nil {
                    p.metrics.IncTemplateOpsTotal("error")
                }
                p.logger.Error("failed to process action template",
                    "error", err,
                    "topic", rule.Topic)
                continue
            }
            if p.metrics != nil {
                p.metrics.IncTemplateOpsTotal("success")
                p.metrics.IncRuleMatches()
            }
            actions = append(actions, action)
        }
    }

    // Update stats
    atomic.AddUint64(&p.stats.Processed, 1)
    if len(actions) > 0 {
        atomic.AddUint64(&p.stats.Matched, 1)
    }

    return actions, nil
}

func (p *Processor) processActionTemplate(action *Action, msg map[string]interface{}, timeCtx *TimeContext) (*Action, error) {
    processedAction := &Action{
        Topic:   action.Topic,
        Payload: action.Payload,
    }

    // Process topic template if it contains variables
    if strings.Contains(action.Topic, "{") {
        topic, err := p.processTemplate(action.Topic, msg, timeCtx)
        if err != nil {
            return nil, fmt.Errorf("failed to process topic template: %w", err)
        }
        processedAction.Topic = topic
    }

    // Process payload template
    payload, err := p.processTemplate(action.Payload, msg, timeCtx)
    if err != nil {
        return nil, fmt.Errorf("failed to process payload template: %w", err)
    }
    processedAction.Payload = payload

    return processedAction, nil
}

// processTemplate handles the new consistent {@} syntax - clean and efficient
func (p *Processor) processTemplate(template string, data map[string]interface{}, timeCtx *TimeContext) (string, error) {
    result := template

    // Process system fields and functions: {@time.hour}, {@uuid7()}, {@timestamp()}
    result = systemFieldPattern.ReplaceAllStringFunc(result, func(match string) string {
        systemRef := match[2 : len(match)-1] // remove {@ and }, get "time.hour" or "uuid7()"
        
        if strings.HasSuffix(systemRef, "()") {
            // It's a function: uuid7(), timestamp()
            return p.processSystemFunction(systemRef)
        } else {
            // It's a system field: time.hour, day.name
            return p.processSystemField("@"+systemRef, timeCtx) // Add @ back for timeCtx lookup
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

// processSystemField handles system time fields: @time.hour, @day.name
func (p *Processor) processSystemField(systemField string, timeCtx *TimeContext) string {
    if value, exists := timeCtx.GetField(systemField); exists {
        strValue := p.convertToString(value)
        p.logger.Debug("processed system field", "field", systemField, "value", strValue)
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

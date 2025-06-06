//file: internal/rule/processor.go

package rule

import (
    "encoding/json"
    "fmt"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/google/uuid"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
)

// Pre-compiled regex patterns for template processing (performance optimization)
var (
    // New syntax patterns
    newFunctionPattern = regexp.MustCompile(`@\{(uuid[47]\(\)|timestamp\(\))\}`)
    newVarPattern      = regexp.MustCompile(`\{([^}@]+)\}`)
    
    // Legacy syntax patterns for backward compatibility
    legacyFunctionPattern = regexp.MustCompile(`\${(uuid[47]\(\))}`)
    legacyVarPattern     = regexp.MustCompile(`\${([^}]+)}`)
    
    // Time field pattern for both conditions and templates
    timeFieldPattern     = regexp.MustCompile(`\{(@[^}]+)\}`)
)

type ProcessorConfig struct {
    Workers    int
    QueueSize  int
    BatchSize  int
}

type Processor struct {
    index        *RuleIndex
    msgPool      *MessagePool
    resultPool   *ResultPool
    timeProvider TimeProvider
    workers      int
    jobChan      chan *ProcessingMessage
    logger       *logger.Logger
    metrics      *metrics.Metrics
    stats        ProcessorStats
    wg           sync.WaitGroup
}

type ProcessorStats struct {
    Processed uint64
    Matched   uint64
    Errors    uint64
}

func NewProcessor(cfg ProcessorConfig, log *logger.Logger, metrics *metrics.Metrics) *Processor {
    if cfg.Workers <= 0 {
        cfg.Workers = 1
    }
    if cfg.QueueSize <= 0 {
        cfg.QueueSize = 1000
    }

    p := &Processor{
        index:        NewRuleIndex(log),
        msgPool:      NewMessagePool(log),
        resultPool:   NewResultPool(log),
        timeProvider: NewSystemTimeProvider(),
        workers:      cfg.Workers,
        jobChan:      make(chan *ProcessingMessage, cfg.QueueSize),
        logger:       log,
        metrics:      metrics,
    }

    p.logger.Info("initializing processor",
        "workers", cfg.Workers,
        "queueSize", cfg.QueueSize,
        "batchSize", cfg.BatchSize)

    if p.metrics != nil {
        p.metrics.SetWorkerPoolActive(float64(cfg.Workers))
    }
    p.startWorkers()
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

func (p *Processor) GetJobChannel() chan *ProcessingMessage {
    return p.jobChan
}

func (p *Processor) Process(topic string, payload []byte) ([]*Action, error) {
    p.logger.Debug("processing message",
        "topic", topic,
        "payloadSize", len(payload))

    // Get time context once for this message processing
    timeCtx := p.timeProvider.GetCurrentContext()
    p.logger.Debug("created time context",
        "timestamp", timeCtx.timestamp.Format(time.RFC3339),
        "timeFields", len(timeCtx.fields))

    msg := p.msgPool.Get()
    msg.Topic = topic
    msg.Payload = payload

    msg.Rules = p.index.Find(topic)
    if len(msg.Rules) == 0 {
        p.logger.Debug("no matching rules found for topic", "topic", topic)
        p.msgPool.Put(msg)
        return nil, nil
    }

    if err := json.Unmarshal(payload, &msg.Values); err != nil {
        p.msgPool.Put(msg)
        atomic.AddUint64(&p.stats.Errors, 1)
        if p.metrics != nil {
            p.metrics.IncMessagesTotal("error")
        }
        p.logger.Error("failed to unmarshal message",
            "error", err,
            "topic", topic)
        return nil, fmt.Errorf("failed to unmarshal message: %w", err)
    }

    for _, rule := range msg.Rules {
        // Pass timeCtx to condition evaluation
        if rule.Conditions == nil || p.evaluateConditions(rule.Conditions, msg.Values, timeCtx) {
            // Pass timeCtx to action template processing
            action, err := p.processActionTemplate(rule.Action, msg.Values, timeCtx)
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
            msg.Actions = append(msg.Actions, action)
        }
    }

    actions := make([]*Action, len(msg.Actions))
    copy(actions, msg.Actions)

    atomic.AddUint64(&p.stats.Processed, 1)
    if len(actions) > 0 {
        atomic.AddUint64(&p.stats.Matched, 1)
        p.logger.Debug("message processing complete",
            "topic", topic,
            "matchedActions", len(actions))
    }

    p.msgPool.Put(msg)
    return actions, nil
}

func (p *Processor) processActionTemplate(action *Action, msg map[string]interface{}, timeCtx *TimeContext) (*Action, error) {
    processedAction := &Action{
        Topic:   action.Topic,
        Payload: action.Payload,
    }

    // Check for both old and new template syntax in topic
    if strings.Contains(action.Topic, "{") {
        topic, err := p.processTemplate(action.Topic, msg, timeCtx)
        if err != nil {
            return nil, fmt.Errorf("failed to process topic template: %w", err)
        }
        processedAction.Topic = topic
    }

    payload, err := p.processTemplate(action.Payload, msg, timeCtx)
    if err != nil {
        return nil, fmt.Errorf("failed to process payload template: %w", err)
    }
    processedAction.Payload = payload

    return processedAction, nil
}

// processTemplate handles both old ${var} and new {var}/@{func()} syntax for backward compatibility
// Uses pre-compiled regex patterns for better performance
func (p *Processor) processTemplate(template string, data map[string]interface{}, timeCtx *TimeContext) (string, error) {
    p.logger.Debug("processing template",
        "template", template,
        "dataKeys", getMapKeys(data),
        "timeFields", timeCtx.GetAllFieldNames())

    result := template

    // Handle time field replacements first: {@time.hour}, {@day.name}, etc.
    result = timeFieldPattern.ReplaceAllStringFunc(result, func(match string) string {
        fieldName := match[1 : len(match)-1] // remove { and }
        
        if value, exists := timeCtx.GetField(fieldName); exists {
            strValue := p.convertToString(value)
            p.logger.Debug("time field processed in template",
                "field", fieldName,
                "value", strValue)
            return strValue
        }
        
        p.logger.Debug("unknown time field in template",
            "field", fieldName,
            "availableTimeFields", timeCtx.GetAllFieldNames())
        return match // Keep original if unknown
    })

    // Handle new function syntax: @{function()} using pre-compiled regex
    result = newFunctionPattern.ReplaceAllStringFunc(result, func(match string) string {
        return p.processFunctionTemplate(match, false) // false = new syntax
    })

    // Handle legacy function syntax: ${function()} for backward compatibility
    result = legacyFunctionPattern.ReplaceAllStringFunc(result, func(match string) string {
        return p.processFunctionTemplate(match, true) // true = legacy syntax
    })

    // Handle new variable syntax: {variable} using pre-compiled regex
    result = newVarPattern.ReplaceAllStringFunc(result, func(match string) string {
        return p.processVariableTemplate(match, data, false) // false = new syntax
    })

    // Handle legacy variable syntax: ${variable} for backward compatibility
    result = legacyVarPattern.ReplaceAllStringFunc(result, func(match string) string {
        return p.processVariableTemplate(match, data, true) // true = legacy syntax
    })

    return result, nil
}

// processFunctionTemplate handles function template processing
func (p *Processor) processFunctionTemplate(match string, isLegacy bool) string {
    var function string
    if isLegacy {
        function = match[2 : len(match)-1] // remove ${ and }
    } else {
        function = match[2 : len(match)-1] // remove @{ and }
    }

    switch function {
    case "uuid4()":
        id := uuid.New()
        syntaxType := "new"
        if isLegacy {
            syntaxType = "legacy"
        }
        p.logger.Debug("generated UUIDv4", "uuid", id.String(), "syntax", syntaxType)
        return id.String()
    case "uuid7()":
        id, err := uuid.NewV7()
        if err != nil {
            syntaxType := "new"
            if isLegacy {
                syntaxType = "legacy"
            }
            p.logger.Error("failed to generate UUIDv7", "error", err, "syntax", syntaxType)
            return ""
        }
        syntaxType := "new"
        if isLegacy {
            syntaxType = "legacy"
        }
        p.logger.Debug("generated UUIDv7", "uuid", id.String(), "syntax", syntaxType)
        return id.String()
    case "timestamp()":
        timestamp := time.Now().UTC().Format(time.RFC3339)
        p.logger.Debug("generated timestamp", "timestamp", timestamp)
        return timestamp
    default:
        p.logger.Debug("unknown function in template",
            "function", function,
            "syntax", map[bool]string{true: "legacy", false: "new"}[isLegacy])
        return match
    }
}

// processVariableTemplate handles variable template processing
func (p *Processor) processVariableTemplate(match string, data map[string]interface{}, isLegacy bool) string {
    var pathStr string
    if isLegacy {
        pathStr = match[2 : len(match)-1] // remove ${ and }
    } else {
        pathStr = match[1 : len(match)-1] // remove { and }
    }

    path := strings.Split(pathStr, ".")

    value, err := p.getValueFromPath(data, path)
    if err != nil {
        syntaxType := "new"
        if isLegacy {
            syntaxType = "legacy"
        }
        p.logger.Debug("template value not found",
            "path", strings.Join(path, "."),
            "error", err,
            "syntax", syntaxType)
        return match
    }

    strValue := p.convertToString(value)
    syntaxType := "new"
    if isLegacy {
        syntaxType = "legacy"
    }
    p.logger.Debug("template variable processed",
        "path", strings.Join(path, "."),
        "value", strValue,
        "syntax", syntaxType)
    return strValue
}

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
            p.logger.Debug("failed to marshal complex value to JSON",
                "error", err)
            return fmt.Sprintf("%v", v)
        }
        return string(jsonBytes)
    default:
        return fmt.Sprintf("%v", v)
    }
}

func (p *Processor) startWorkers() {
    p.logger.Info("starting worker pool",
        "workerCount", p.workers)

    for i := 0; i < p.workers; i++ {
        p.wg.Add(1)
        go p.worker()
    }
}

func (p *Processor) worker() {
    defer p.wg.Done()

    for msg := range p.jobChan {
        p.processMessage(msg)
    }
}

func (p *Processor) processMessage(msg *ProcessingMessage) {
    defer p.msgPool.Put(msg)

    if err := json.Unmarshal(msg.Payload, &msg.Values); err != nil {
        atomic.AddUint64(&p.stats.Errors, 1)
        if p.metrics != nil {
            p.metrics.IncMessagesTotal("error")
        }
        p.logger.Error("failed to unmarshal message",
            "error", err,
            "topic", msg.Topic)
        return
    }

    atomic.AddUint64(&p.stats.Processed, 1)
    if len(msg.Actions) > 0 {
        atomic.AddUint64(&p.stats.Matched, 1)
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
    close(p.jobChan)
    p.wg.Wait()
}

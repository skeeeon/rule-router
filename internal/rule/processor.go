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

type ProcessorConfig struct {
    Workers    int
    QueueSize  int
    BatchSize  int
}

type Processor struct {
    index      *RuleIndex
    msgPool    *MessagePool
    resultPool *ResultPool
    workers    int
    jobChan    chan *ProcessingMessage
    logger     *logger.Logger
    metrics    *metrics.Metrics
    stats      ProcessorStats
    wg         sync.WaitGroup
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
        index:      NewRuleIndex(log),
        msgPool:    NewMessagePool(log),
        resultPool: NewResultPool(log),
        workers:    cfg.Workers,
        jobChan:    make(chan *ProcessingMessage, cfg.QueueSize),
        logger:     log,
        metrics:    metrics,
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
        if rule.Conditions == nil || p.evaluateConditions(rule.Conditions, msg.Values) {
            action, err := p.processActionTemplate(rule.Action, msg.Values)
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

func (p *Processor) processActionTemplate(action *Action, msg map[string]interface{}) (*Action, error) {
    processedAction := &Action{
        Topic:   action.Topic,
        Payload: action.Payload,
    }

    // Check for both old and new template syntax in topic
    if strings.Contains(action.Topic, "{") {
        topic, err := p.processTemplate(action.Topic, msg)
        if err != nil {
            return nil, fmt.Errorf("failed to process topic template: %w", err)
        }
        processedAction.Topic = topic
    }

    payload, err := p.processTemplate(action.Payload, msg)
    if err != nil {
        return nil, fmt.Errorf("failed to process payload template: %w", err)
    }
    processedAction.Payload = payload

    return processedAction, nil
}

// processTemplate handles both old ${var} and new {var}/@{func()} syntax for backward compatibility
func (p *Processor) processTemplate(template string, data map[string]interface{}) (string, error) {
    p.logger.Debug("processing template",
        "template", template,
        "dataKeys", getMapKeys(data))

    result := template

    // Handle new function syntax: @{function()}
    newFunctionPattern := regexp.MustCompile(`@\{(uuid[47]\(\)|timestamp\(\))\}`)
    result = newFunctionPattern.ReplaceAllStringFunc(result, func(match string) string {
        // Extract function name from @{function()}
        function := match[2 : len(match)-1] // remove @{ and }

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
            p.logger.Debug("unknown function in template",
                "function", function)
            return match
        }
    })

    // Handle legacy function syntax: ${function()} for backward compatibility
    legacyFunctionPattern := regexp.MustCompile(`\${(uuid[47]\(\))}`)
    result = legacyFunctionPattern.ReplaceAllStringFunc(result, func(match string) string {
        // Extract function name from ${function()}
        function := match[2 : len(match)-1] // remove ${ and }

        switch function {
        case "uuid4()":
            id := uuid.New()
            p.logger.Debug("generated UUIDv4 (legacy syntax)", "uuid", id.String())
            return id.String()
        case "uuid7()":
            id, err := uuid.NewV7()
            if err != nil {
                p.logger.Error("failed to generate UUIDv7 (legacy syntax)", "error", err)
                return ""
            }
            p.logger.Debug("generated UUIDv7 (legacy syntax)", "uuid", id.String())
            return id.String()
        default:
            p.logger.Debug("unknown function in legacy template",
                "function", function)
            return match
        }
    })

    // Handle new variable syntax: {variable}
    newVarPattern := regexp.MustCompile(`\{([^}@]+)\}`)
    result = newVarPattern.ReplaceAllStringFunc(result, func(match string) string {
        path := strings.Split(match[1:len(match)-1], ".") // remove { and }

        value, err := p.getValueFromPath(data, path)
        if err != nil {
            p.logger.Debug("template value not found",
                "path", strings.Join(path, "."),
                "error", err)
            return match
        }

        strValue := p.convertToString(value)
        p.logger.Debug("template variable processed",
            "path", strings.Join(path, "."),
            "value", strValue)
        return strValue
    })

    // Handle legacy variable syntax: ${variable} for backward compatibility
    legacyVarPattern := regexp.MustCompile(`\${([^}]+)}`)
    result = legacyVarPattern.ReplaceAllStringFunc(result, func(match string) string {
        path := strings.Split(match[2:len(match)-1], ".") // remove ${ and }

        value, err := p.getValueFromPath(data, path)
        if err != nil {
            p.logger.Debug("legacy template value not found",
                "path", strings.Join(path, "."),
                "error", err)
            return match
        }

        strValue := p.convertToString(value)
        p.logger.Debug("legacy template variable processed",
            "path", strings.Join(path, "."),
            "value", strValue)
        return strValue
    })

    return result, nil
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

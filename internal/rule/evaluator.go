//file: internal/rule/evaluator.go

package rule

import (
    "fmt"
    "strconv"
    "strings"
)

func (p *Processor) evaluateConditions(conditions *Conditions, msg map[string]interface{}, timeCtx *TimeContext) bool {
    if conditions == nil || (len(conditions.Items) == 0 && len(conditions.Groups) == 0) {
        p.logger.Debug("no conditions to evaluate")
        return true
    }

    p.logger.Debug("evaluating condition group",
        "operator", conditions.Operator,
        "numConditions", len(conditions.Items),
        "numGroups", len(conditions.Groups))

    results := make([]bool, 0, len(conditions.Items)+len(conditions.Groups))

    for i, condition := range conditions.Items {
        result := p.evaluateCondition(&condition, msg, timeCtx)
        results = append(results, result)
        
        p.logger.Debug("evaluated individual condition",
            "index", i,
            "field", condition.Field,
            "operator", condition.Operator,
            "value", condition.Value,
            "result", result)
    }

    for i, group := range conditions.Groups {
        result := p.evaluateConditions(&group, msg, timeCtx)
        results = append(results, result)
        
        p.logger.Debug("evaluated nested group",
            "index", i,
            "operator", group.Operator,
            "result", result)
    }

    var finalResult bool
    switch conditions.Operator {
    case "and":
        finalResult = true
        for _, result := range results {
            if !result {
                finalResult = false
                break
            }
        }
    case "or":
        finalResult = false
        for _, result := range results {
            if result {
                finalResult = true
                break
            }
        }
    default:
        p.logger.Error("unknown logical operator", "operator", conditions.Operator)
        return false
    }

    p.logger.Debug("condition group evaluation complete",
        "operator", conditions.Operator,
        "result", finalResult)

    return finalResult
}

func (p *Processor) evaluateCondition(cond *Condition, msg map[string]interface{}, timeCtx *TimeContext) bool {
    var value interface{}
    var ok bool
    
    // Check if this is a system time field
    if strings.HasPrefix(cond.Field, "@") {
        value, ok = timeCtx.GetField(cond.Field)
        if !ok {
            p.logger.Debug("unknown system time field in condition",
                "field", cond.Field,
                "availableTimeFields", timeCtx.GetAllFieldNames())
            return false
        }
        p.logger.Debug("evaluating time field condition",
            "field", cond.Field,
            "operator", cond.Operator,
            "expectedValue", cond.Value,
            "actualTimeValue", value)
    } else {
        // Regular message field
        value, ok = msg[cond.Field]
        if !ok {
            p.logger.Debug("field not found in message",
                "field", cond.Field,
                "availableFields", getMapKeys(msg))
            return false
        }
        p.logger.Debug("evaluating message field condition",
            "field", cond.Field,
            "operator", cond.Operator,
            "expectedValue", cond.Value,
            "actualValue", value)
    }

    var result bool
    switch cond.Operator {
    case "eq":
        result = value == cond.Value
    case "neq":
        result = value != cond.Value
    case "gt", "lt", "gte", "lte":
        result = p.compareNumeric(value, cond.Value, cond.Operator)
    case "exists":
        result = value != nil
    case "contains":
        result = strings.Contains(fmt.Sprint(value), fmt.Sprint(cond.Value))
    default:
        p.logger.Error("unknown operator", "operator", cond.Operator)
        return false
    }

    p.logger.Debug("condition evaluation result",
        "field", cond.Field,
        "operator", cond.Operator,
        "expectedValue", cond.Value,
        "actualValue", value,
        "result", result)

    return result
}

func (p *Processor) compareNumeric(a, b interface{}, op string) bool {
    var numA, numB float64
    var err error

    switch v := a.(type) {
    case float64:
        numA = v
    case int:
        numA = float64(v)
    case string:
        numA, err = strconv.ParseFloat(v, 64)
        if err != nil {
            p.logger.Debug("failed to convert first value to number",
                "value", v,
                "error", err)
            return false
        }
    default:
        p.logger.Debug("first value is not a number",
            "value", v,
            "type", fmt.Sprintf("%T", v))
        return false
    }

    switch v := b.(type) {
    case float64:
        numB = v
    case int:
        numB = float64(v)
    case string:
        numB, err = strconv.ParseFloat(v, 64)
        if err != nil {
            p.logger.Debug("failed to convert second value to number",
                "value", v,
                "error", err)
            return false
        }
    default:
        p.logger.Debug("second value is not a number",
            "value", v,
            "type", fmt.Sprintf("%T", v))
        return false
    }

    switch op {
    case "gt":
        return numA > numB
    case "lt":
        return numA < numB
    case "gte":
        return numA >= numB
    case "lte":
        return numA <= numB
    default:
        return false
    }
}

func getMapKeys(m map[string]interface{}) []string {
    keys := make([]string, 0, len(m))
    for k := range m {
        keys = append(keys, k)
    }
    return keys
}

//file: internal/rule/evaluator.go

package rule

import (
	"fmt"
	"strconv"
	"strings"

	"rule-router/internal/logger"
)

// Evaluator processes rule conditions against an EvaluationContext.
type Evaluator struct {
	logger *logger.Logger
}

// NewEvaluator creates a new Evaluator.
func NewEvaluator(log *logger.Logger) *Evaluator {
	return &Evaluator{logger: log}
}

// Evaluate checks if a message satisfies the conditions within a group.
func (e *Evaluator) Evaluate(conditions *Conditions, context *EvaluationContext) bool {
	if conditions == nil || (len(conditions.Items) == 0 && len(conditions.Groups) == 0) {
		e.logger.Debug("no conditions to evaluate")
		return true
	}

	e.logger.Debug("evaluating condition group",
		"operator", conditions.Operator,
		"numConditions", len(conditions.Items),
		"numGroups", len(conditions.Groups))

	results := make([]bool, 0, len(conditions.Items)+len(conditions.Groups))

	for i, condition := range conditions.Items {
		result := e.evaluateCondition(&condition, context)
		results = append(results, result)
		e.logger.Debug("evaluated individual condition",
			"index", i, "field", condition.Field, "operator", condition.Operator, "value", condition.Value, "result", result)
	}

	for i, group := range conditions.Groups {
		result := e.Evaluate(&group, context)
		results = append(results, result)
		e.logger.Debug("evaluated nested group", "index", i, "operator", group.Operator, "result", result)
	}

	switch conditions.Operator {
	case "and":
		for _, result := range results {
			if !result {
				return false
			}
		}
		return true
	case "or":
		for _, result := range results {
			if result {
				return true
			}
		}
		return false
	default:
		e.logger.Error("unknown logical operator", "operator", conditions.Operator)
		return false
	}
}

// evaluateCondition evaluates a single condition using the centralized context.
func (e *Evaluator) evaluateCondition(cond *Condition, context *EvaluationContext) bool {
	actualValue, exists := context.ResolveValue(cond.Field)

	if !exists {
		// If the field doesn't exist, the only condition that can pass is 'exists: false' (implicitly)
		// or an explicit check for a non-existent field.
		// For simplicity, we treat 'exists' as a special operator.
		if cond.Operator == "exists" {
			return false // The field does not exist.
		}
		// For all other operators, a non-existent field fails the condition.
		return false
	}

	// If we are here, the field exists.
	if cond.Operator == "exists" {
		return actualValue != nil
	}

	var result bool
	switch cond.Operator {
	case "eq":
		result = e.compareValues(actualValue, cond.Value, "eq")
	case "neq":
		result = e.compareValues(actualValue, cond.Value, "neq")
	case "gt", "lt", "gte", "lte":
		result = e.compareNumeric(actualValue, cond.Value, cond.Operator)
	case "contains":
		result = e.compareContains(actualValue, cond.Value)
	case "not_contains":
		result = !e.compareContains(actualValue, cond.Value)
	case "in":
		result = e.compareIn(actualValue, cond.Value)
	case "not_in":
		result = !e.compareIn(actualValue, cond.Value)
	default:
		e.logger.Error("unknown operator", "operator", cond.Operator)
		return false
	}

	e.logger.Debug("condition evaluation result",
		"field", cond.Field, "operator", cond.Operator, "expectedValue", cond.Value, "actualValue", actualValue, "result", result)

	return result
}

// --- Comparison Helpers ---

func (e *Evaluator) compareValues(a, b interface{}, op string) bool {
	// ... (omitted for brevity - this logic is identical to the original file)
	if a == nil && b == nil {
		return op == "eq"
	}
	if a == nil || b == nil {
		return op == "neq"
	}

	equal := false
	switch va := a.(type) {
	case string:
		if vb, ok := b.(string); ok {
			equal = va == vb
		} else {
			equal = va == e.convertToString(b)
		}
	case float64:
		switch vb := b.(type) {
		case float64:
			equal = va == vb
		case int:
			equal = va == float64(vb)
		case string:
			if parsed, err := strconv.ParseFloat(vb, 64); err == nil {
				equal = va == parsed
			}
		}
	case int:
		switch vb := b.(type) {
		case int:
			equal = va == vb
		case float64:
			equal = float64(va) == vb
		case string:
			if parsed, err := strconv.Atoi(vb); err == nil {
				equal = va == parsed
			}
		}
	case bool:
		if vb, ok := b.(bool); ok {
			equal = va == vb
		} else if vb, ok := b.(string); ok {
			if parsed, err := strconv.ParseBool(vb); err == nil {
				equal = va == parsed
			}
		}
	default:
		equal = e.convertToString(a) == e.convertToString(b)
	}

	if op == "eq" {
		return equal
	}
	return !equal
}

func (e *Evaluator) compareContains(fieldValue, searchValue interface{}) bool {
	// ... (omitted for brevity - this logic is identical to the original file)
	if arr, isArray := fieldValue.([]interface{}); isArray {
		for _, item := range arr {
			if e.compareValues(item, searchValue, "eq") {
				return true
			}
		}
		return false
	}
	fieldStr := e.convertToString(fieldValue)
	searchStr := e.convertToString(searchValue)
	return strings.Contains(fieldStr, searchStr)
}

func (e *Evaluator) compareIn(fieldValue, allowedValues interface{}) bool {
	// ... (omitted for brevity - this logic is identical to the original file)
	arr, isArray := allowedValues.([]interface{})
	if !isArray {
		return false
	}
	for _, item := range arr {
		if e.compareValues(fieldValue, item, "eq") {
			return true
		}
	}
	return false
}

func (e *Evaluator) compareNumeric(a, b interface{}, op string) bool {
	// ... (omitted for brevity - this logic is identical to the original file)
	var numA, numB float64
	var err error

	numA, err = e.toFloat(a)
	if err != nil {
		return false
	}
	numB, err = e.toFloat(b)
	if err != nil {
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

func (e *Evaluator) toFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case string:
		return strconv.ParseFloat(val, 64)
	default:
		return 0, fmt.Errorf("not a number")
	}
}

func (e *Evaluator) convertToString(value interface{}) string {
	// ... (omitted for brevity - this logic is identical to the original file)
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%v", value)
}

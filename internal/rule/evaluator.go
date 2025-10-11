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
// Uses short-circuit evaluation for optimal performance:
// - AND: Returns false on first false condition (remaining conditions skipped)
// - OR: Returns true on first true condition (remaining conditions skipped)
func (e *Evaluator) Evaluate(conditions *Conditions, context *EvaluationContext) bool {
	if conditions == nil || (len(conditions.Items) == 0 && len(conditions.Groups) == 0) {
		e.logger.Debug("no conditions to evaluate")
		return true
	}

	e.logger.Debug("evaluating condition group",
		"operator", conditions.Operator,
		"numConditions", len(conditions.Items),
		"numGroups", len(conditions.Groups))

	// Short-circuit evaluation based on operator
	if conditions.Operator == "and" {
		return e.evaluateAND(conditions, context)
	} else if conditions.Operator == "or" {
		return e.evaluateOR(conditions, context)
	} else {
		e.logger.Error("unknown logical operator", "operator", conditions.Operator)
		return false
	}
}

// evaluateAND evaluates with short-circuit on first false
func (e *Evaluator) evaluateAND(conditions *Conditions, context *EvaluationContext) bool {
	// Evaluate individual conditions - stop on first false
	for i, condition := range conditions.Items {
		result := e.evaluateCondition(&condition, context)
		
		e.logger.Debug("evaluated AND condition",
			"index", i,
			"field", condition.Field,
			"operator", condition.Operator,
			"value", condition.Value,
			"result", result)
		
		if !result {
			e.logger.Debug("AND group short-circuited on condition",
				"failedIndex", i,
				"field", condition.Field,
				"totalConditions", len(conditions.Items),
				"skippedConditions", len(conditions.Items)-i-1)
			return false // Short-circuit: no need to check remaining
		}
	}

	// Evaluate nested groups - stop on first false
	for i, group := range conditions.Groups {
		result := e.Evaluate(&group, context)
		
		e.logger.Debug("evaluated AND nested group",
			"index", i,
			"operator", group.Operator,
			"result", result)
		
		if !result {
			e.logger.Debug("AND group short-circuited on nested group",
				"failedGroupIndex", i,
				"totalGroups", len(conditions.Groups),
				"skippedGroups", len(conditions.Groups)-i-1)
			return false // Short-circuit: no need to check remaining
		}
	}

	e.logger.Debug("AND group: all conditions passed")
	return true
}

// evaluateOR evaluates with short-circuit on first true
func (e *Evaluator) evaluateOR(conditions *Conditions, context *EvaluationContext) bool {
	// Evaluate individual conditions - stop on first true
	for i, condition := range conditions.Items {
		result := e.evaluateCondition(&condition, context)
		
		e.logger.Debug("evaluated OR condition",
			"index", i,
			"field", condition.Field,
			"operator", condition.Operator,
			"value", condition.Value,
			"result", result)
		
		if result {
			e.logger.Debug("OR group short-circuited on condition",
				"successIndex", i,
				"field", condition.Field,
				"totalConditions", len(conditions.Items),
				"skippedConditions", len(conditions.Items)-i-1)
			return true // Short-circuit: no need to check remaining
		}
	}

	// Evaluate nested groups - stop on first true
	for i, group := range conditions.Groups {
		result := e.Evaluate(&group, context)
		
		e.logger.Debug("evaluated OR nested group",
			"index", i,
			"operator", group.Operator,
			"result", result)
		
		if result {
			e.logger.Debug("OR group short-circuited on nested group",
				"successGroupIndex", i,
				"totalGroups", len(conditions.Groups),
				"skippedGroups", len(conditions.Groups)-i-1)
			return true // Short-circuit: no need to check remaining
		}
	}

	e.logger.Debug("OR group: no conditions passed")
	return false
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
	// Check if fieldValue is an array
	if arr, isArray := fieldValue.([]interface{}); isArray {
		// Array membership check
		for _, item := range arr {
			if e.compareValues(item, searchValue, "eq") {
				return true
			}
		}
		return false
	}

	// String substring check
	fieldStr := e.convertToString(fieldValue)
	searchStr := e.convertToString(searchValue)
	return strings.Contains(fieldStr, searchStr)
}

func (e *Evaluator) compareIn(fieldValue, allowedValues interface{}) bool {
	// Check if allowedValues is an array
	arr, isArray := allowedValues.([]interface{})
	if !isArray {
		return false
	}

	// Check if fieldValue is in the array
	for _, item := range arr {
		if e.compareValues(fieldValue, item, "eq") {
			return true
		}
	}
	return false
}

func (e *Evaluator) compareNumeric(a, b interface{}, op string) bool {
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
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%v", value)
}

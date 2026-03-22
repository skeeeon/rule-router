// file: internal/rule/evaluator.go

package rule

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// Clock skew tolerance for "recent" operator
const clockSkewTolerance = 5 * time.Second

// Evaluator processes rule conditions against an EvaluationContext
type Evaluator struct {
	logger  *logger.Logger
	metrics *metrics.Metrics
}

// NewEvaluator creates a new Evaluator
func NewEvaluator(log *logger.Logger) *Evaluator {
	return &Evaluator{logger: log}
}

// SetMetrics sets the metrics collector (optional)
func (e *Evaluator) SetMetrics(m *metrics.Metrics) {
	e.metrics = m
}

// Evaluate checks if a message satisfies the conditions
func (e *Evaluator) Evaluate(conditions *Conditions, context *EvaluationContext) bool {
	if conditions == nil || (len(conditions.Items) == 0 && len(conditions.Groups) == 0) {
		return true
	}

	if conditions.Operator == "and" {
		return e.evaluateAND(conditions, context)
	} else if conditions.Operator == "or" {
		return e.evaluateOR(conditions, context)
	} else {
		e.logger.Error("unknown logical operator", "operator", conditions.Operator)
		return false
	}
}

func (e *Evaluator) evaluateAND(conditions *Conditions, context *EvaluationContext) bool {
	for _, condition := range conditions.Items {
		if !e.evaluateCondition(&condition, context) {
			return false
		}
	}

	for _, group := range conditions.Groups {
		if !e.Evaluate(&group, context) {
			return false
		}
	}

	return true
}

func (e *Evaluator) evaluateOR(conditions *Conditions, context *EvaluationContext) bool {
	for _, condition := range conditions.Items {
		if e.evaluateCondition(&condition, context) {
			return true
		}
	}

	for _, group := range conditions.Groups {
		if e.Evaluate(&group, context) {
			return true
		}
	}

	return false
}

func (e *Evaluator) evaluateCondition(cond *Condition, context *EvaluationContext) bool {
	// Resolve LEFT side (field) - uses pre-computed path when available
	leftValue, err := resolveConditionValueFast(cond.Field, cond.fieldVarName, cond.fieldPath, context)
	if err != nil {
		e.logger.Warn("failed to resolve condition field",
			"field", cond.Field,
			"operator", cond.Operator,
			"error", err)
		return false
	}

	// Check existence before proceeding (except for 'exists' operator)
	if leftValue == nil && cond.Operator != "exists" {
		return false
	}

	// Handle 'exists' operator specially
	if cond.Operator == "exists" {
		return leftValue != nil
	}

	// Handle array operators (any/all/none) - pass through to existing handler
	if cond.Operator == "any" || cond.Operator == "all" || cond.Operator == "none" {
		return e.evaluateArrayCondition(leftValue, cond, context)
	}

	// Resolve RIGHT side (value) - uses pre-computed path when available
	rightValue, err := resolveConditionValueFast(cond.Value, cond.valueVarName, cond.valuePath, context)
	if err != nil {
		e.logger.Warn("failed to resolve condition value",
			"field", cond.Field,
			"operator", cond.Operator,
			"value", cond.Value,
			"error", err)
		return false
	}

	// Perform comparison with both sides resolved
	switch cond.Operator {
	case "eq":
		return e.compareValues(leftValue, rightValue, "eq")
	case "neq":
		return e.compareValues(leftValue, rightValue, "neq")
	case "gt", "lt", "gte", "lte":
		return e.compareNumeric(leftValue, rightValue, cond.Operator)
	case "contains":
		return e.compareContains(leftValue, rightValue)
	case "not_contains":
		return !e.compareContains(leftValue, rightValue)
	case "in":
		return e.compareIn(leftValue, rightValue)
	case "not_in":
		return !e.compareIn(leftValue, rightValue)
	case "recent":
		return e.compareRecent(leftValue, rightValue, context)
	default:
		e.logger.Error("unknown operator", "operator", cond.Operator, "field", cond.Field)
		return false
	}
}

// evaluateArrayCondition handles array operators: any, all, none
// Now supports primitive array elements via ensureObject wrapping
func (e *Evaluator) evaluateArrayCondition(fieldValue interface{}, cond *Condition, context *EvaluationContext) bool {
	array, ok := fieldValue.([]interface{})
	if !ok {
		e.logger.Debug("array operator used on non-array field",
			"field", cond.Field,
			"operator", cond.Operator,
			"actualType", fmt.Sprintf("%T", fieldValue))

		if e.metrics != nil {
			e.metrics.IncArrayOperatorEvaluations(cond.Operator, false)
		}
		return false
	}

	if cond.Conditions == nil {
		e.logger.Error("array operator missing nested conditions",
			"field", cond.Field,
			"operator", cond.Operator)

		if e.metrics != nil {
			e.metrics.IncArrayOperatorEvaluations(cond.Operator, false)
		}
		return false
	}

	matchCount := 0

	for i, element := range array {
		// Wrap primitives, pass objects through
		elementMap := ensureObject(element)

		// Create element context for array element processing
		elementContext := context.WithElement(elementMap)

		if e.Evaluate(cond.Conditions, elementContext) {
			matchCount++

			// Short-circuit for "any"
			if cond.Operator == "any" {
				if e.metrics != nil {
					e.metrics.IncArrayOperatorEvaluations(cond.Operator, true)
				}
				return true
			}

			// Short-circuit for "none"
			if cond.Operator == "none" {
				if e.metrics != nil {
					e.metrics.IncArrayOperatorEvaluations(cond.Operator, false)
				}
				return false
			}
		} else {
			// Short-circuit for "all"
			if cond.Operator == "all" {
				e.logger.Debug("array 'all' failed",
					"field", cond.Field,
					"failedIndex", i,
					"checked", i+1,
					"total", len(array))

				if e.metrics != nil {
					e.metrics.IncArrayOperatorEvaluations(cond.Operator, false)
				}
				return false
			}
		}
	}

	// Final evaluation after checking all elements
	var result bool
	switch cond.Operator {
	case "any":
		result = matchCount > 0
	case "all":
		result = matchCount > 0 && matchCount == len(array)
	case "none":
		result = matchCount == 0
	default:
		e.logger.Error("invalid array operator", "operator", cond.Operator)
		result = false
	}

	if e.metrics != nil {
		e.metrics.IncArrayOperatorEvaluations(cond.Operator, result)
	}

	return result
}

// compareRecent checks if a timestamp is within tolerance
func (e *Evaluator) compareRecent(msgTimestamp, tolerance interface{}, context *EvaluationContext) bool {
	toleranceStr, ok := tolerance.(string)
	if !ok {
		return false
	}

	duration, err := time.ParseDuration(toleranceStr)
	if err != nil {
		return false
	}

	ts, err := e.parseTimestamp(msgTimestamp)
	if err != nil {
		return false
	}

	now := context.Time.timestamp
	diff := now.Sub(ts)

	if diff < -clockSkewTolerance {
		return false
	}

	return diff <= duration
}

// parseTimestamp flexibly parses timestamps
func (e *Evaluator) parseTimestamp(value interface{}) (time.Time, error) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid numeric timestamp: %w", err)
		}
		sec, dec := math.Modf(f)
		return time.Unix(int64(sec), int64(dec*1e9)), nil
	case float64:
		sec, dec := math.Modf(v)
		return time.Unix(int64(sec), int64(dec*1e9)), nil
	case int64:
		return time.Unix(v, 0), nil
	case int:
		return time.Unix(int64(v), 0), nil
	case string:
		return time.Parse(time.RFC3339, v)
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type: %T", v)
	}
}

// --- Comparison Helpers ---

func (e *Evaluator) compareValues(a, b interface{}, op string) bool {
	if a == nil && b == nil {
		return op == "eq"
	}
	if a == nil || b == nil {
		return op == "neq"
	}

	// Normalize json.Number to float64 before comparison so all numeric
	// paths go through the existing float64/int branches.
	a = normalizeNumber(a)
	b = normalizeNumber(b)

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
	case json.Number:
		return val.Float64()
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

// normalizeNumber converts json.Number to a native Go numeric type
// so callers don't need separate json.Number branches in type switches.
// Returns float64 for decimals, int for integers that fit.
func normalizeNumber(v interface{}) interface{} {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := n.Int64(); err == nil {
		return int(i)
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

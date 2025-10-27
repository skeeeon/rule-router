// file: internal/rule/evaluator.go

package rule

import (
	json "github.com/goccy/go-json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"rule-router/internal/logger"
)

// Clock skew tolerance for "recent" operator (allows messages slightly in the future)
const clockSkewTolerance = 5 * time.Second

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
			return false
		}
	}

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
			return false
		}
	}

	e.logger.Debug("AND group: all conditions passed")
	return true
}

func (e *Evaluator) evaluateOR(conditions *Conditions, context *EvaluationContext) bool {
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
			return true
		}
	}

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
			return true
		}
	}

	e.logger.Debug("OR group: no conditions passed")
	return false
}

func (e *Evaluator) evaluateCondition(cond *Condition, context *EvaluationContext) bool {
	actualValue, exists := context.ResolveValue(cond.Field)

	if !exists {
		if cond.Operator == "exists" {
			return false
		}
		return false
	}

	if cond.Operator == "exists" {
		return actualValue != nil
	}

	// NEW: Handle array operators (any/all/none)
	if cond.Operator == "any" || cond.Operator == "all" || cond.Operator == "none" {
		return e.evaluateArrayCondition(actualValue, cond, context)
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
	case "recent":
		result = e.compareRecent(actualValue, cond.Value, context)
	default:
		e.logger.Error("unknown operator", "operator", cond.Operator)
		return false
	}

	e.logger.Debug("condition evaluation result",
		"field", cond.Field, "operator", cond.Operator, "expectedValue", cond.Value, "actualValue", actualValue, "result", result)

	return result
}

// evaluateArrayCondition handles array operators: any, all, none
// NEW: Implements array element iteration with nested condition evaluation
func (e *Evaluator) evaluateArrayCondition(fieldValue interface{}, cond *Condition, context *EvaluationContext) bool {
	e.logger.Debug("evaluating array condition",
		"field", cond.Field,
		"operator", cond.Operator)

	// 1. Ensure the field value is an array
	array, ok := fieldValue.([]interface{})
	if !ok {
		e.logger.Debug("array operator used on non-array field",
			"field", cond.Field,
			"operator", cond.Operator,
			"actualType", fmt.Sprintf("%T", fieldValue))
		return false
	}

	// 2. Ensure nested conditions exist
	if cond.Conditions == nil {
		e.logger.Error("array operator missing nested conditions",
			"field", cond.Field,
			"operator", cond.Operator)
		return false
	}

	e.logger.Debug("array condition setup complete",
		"field", cond.Field,
		"operator", cond.Operator,
		"arrayLength", len(array),
		"nestedConditions", cond.Conditions.Operator)

	// 3. Iterate over array elements and evaluate nested conditions
	matchCount := 0
	
	for i, element := range array {
		// Convert element to map for evaluation
		elementMap, ok := element.(map[string]interface{})
		if !ok {
			e.logger.Debug("array element is not an object, skipping",
				"field", cond.Field,
				"index", i,
				"elementType", fmt.Sprintf("%T", element))
			continue
		}

		// 4. Create evaluation context for this array element
		elementContext, err := e.createElementContext(elementMap, context)
		if err != nil {
			e.logger.Error("failed to create context for array element",
				"field", cond.Field,
				"index", i,
				"error", err)
			continue
		}

		// 5. Evaluate nested conditions against this element
		elementMatches := e.Evaluate(cond.Conditions, elementContext)
		
		e.logger.Debug("array element evaluation",
			"field", cond.Field,
			"index", i,
			"matches", elementMatches)

		if elementMatches {
			matchCount++
			
			// Short-circuit optimization for "any" - exit as soon as we find a match
			if cond.Operator == "any" {
				e.logger.Debug("array operator 'any' short-circuited",
					"field", cond.Field,
					"matchedIndex", i,
					"totalElements", len(array),
					"skippedElements", len(array)-i-1)
				return true
			}
			
			// Short-circuit optimization for "none" - exit as soon as we find a match (failure case)
			if cond.Operator == "none" {
				e.logger.Debug("array operator 'none' short-circuited (found match)",
					"field", cond.Field,
					"matchedIndex", i,
					"totalElements", len(array))
				return false
			}
		} else {
			// Short-circuit optimization for "all" - exit as soon as we find a non-match
			if cond.Operator == "all" {
				e.logger.Debug("array operator 'all' short-circuited (found non-match)",
					"field", cond.Field,
					"failedIndex", i,
					"totalElements", len(array),
					"skippedElements", len(array)-i-1)
				return false
			}
		}
	}

	// 6. Final evaluation based on operator
	var result bool
	switch cond.Operator {
	case "any":
		result = matchCount > 0
	case "all":
		result = matchCount == len(array)
	case "none":
		result = matchCount == 0
	default:
		e.logger.Error("invalid array operator", "operator", cond.Operator)
		return false
	}

	e.logger.Debug("array condition evaluation complete",
		"field", cond.Field,
		"operator", cond.Operator,
		"arrayLength", len(array),
		"matchCount", matchCount,
		"result", result)

	return result
}

// createElementContext creates a new evaluation context for an array element
// CRITICAL: Preserves reference to OriginalMsg for @msg prefix support
func (e *Evaluator) createElementContext(element map[string]interface{}, originalContext *EvaluationContext) (*EvaluationContext, error) {
	// Marshal element to bytes for context creation
	elementBytes, err := json.Marshal(element)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal array element: %w", err)
	}

	// Create new context with element as the current message
	elementContext, err := NewEvaluationContext(
		elementBytes,
		originalContext.Headers,
		originalContext.Subject,
		originalContext.HTTP,
		originalContext.Time,
		originalContext.KV,
		originalContext.sigVerification,
		e.logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create element context: %w", err)
	}

	// CRITICAL: Preserve reference to original message for @msg prefix
	// This allows nested conditions to access root message fields via @msg.field
	elementContext.OriginalMsg = originalContext.OriginalMsg

	return elementContext, nil
}

// compareRecent checks if a timestamp is within a tolerance duration of current time.
// Supports Unix seconds (int/float) and RFC3339 strings.
// Allows small future tolerance (5s) to account for clock skew.
func (e *Evaluator) compareRecent(msgTimestamp, tolerance interface{}, context *EvaluationContext) bool {
	// Parse tolerance duration string
	toleranceStr, ok := tolerance.(string)
	if !ok {
		e.logger.Debug("recent operator: tolerance must be a duration string", "tolerance", tolerance)
		return false
	}
	
	duration, err := time.ParseDuration(toleranceStr)
	if err != nil {
		e.logger.Debug("recent operator: invalid duration",
			"tolerance", toleranceStr,
			"error", err)
		return false
	}

	// Parse message timestamp
	ts, err := e.parseTimestamp(msgTimestamp)
	if err != nil {
		e.logger.Debug("recent operator: failed to parse timestamp",
			"value", msgTimestamp,
			"error", err)
		return false
	}

	// Get current time from context
	now := context.Time.timestamp
	diff := now.Sub(ts)

	// Reject if timestamp is too far in the future (beyond clock skew tolerance)
	if diff < -clockSkewTolerance {
		e.logger.Debug("recent operator: timestamp too far in future",
			"timestamp", ts,
			"now", now,
			"diff", diff,
			"maxFutureTolerance", clockSkewTolerance)
		return false
	}

	// Accept if within tolerance window
	isRecent := diff <= duration

	e.logger.Debug("recent operator evaluation",
		"timestamp", ts,
		"now", now,
		"diff", diff,
		"tolerance", duration,
		"result", isRecent)

	return isRecent
}

// parseTimestamp flexibly parses timestamps from various formats.
// Standardized on Unix seconds (int/float) with RFC3339 as fallback.
func (e *Evaluator) parseTimestamp(value interface{}) (time.Time, error) {
	switch v := value.(type) {
	case float64:
		// Unix seconds (may have fractional part for milliseconds)
		sec, dec := math.Modf(v)
		return time.Unix(int64(sec), int64(dec*1e9)), nil
	case int64:
		// Unix seconds
		return time.Unix(v, 0), nil
	case int:
		// Unix seconds
		return time.Unix(int64(v), 0), nil
	case string:
		// RFC3339 format as fallback
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

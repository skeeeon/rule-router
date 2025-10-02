//file: internal/rule/evaluator.go

package rule

import (
	"fmt"
	"strconv"
	"strings"
)

func (p *Processor) evaluateConditions(conditions *Conditions, msg map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) bool {
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
		result := p.evaluateCondition(&condition, msg, timeCtx, subjectCtx, kvCtx)
		results = append(results, result)

		p.logger.Debug("evaluated individual condition",
			"index", i,
			"field", condition.Field,
			"operator", condition.Operator,
			"value", condition.Value,
			"result", result)
	}

	for i, group := range conditions.Groups {
		result := p.evaluateConditions(&group, msg, timeCtx, subjectCtx, kvCtx)
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

// ENHANCED: evaluateCondition with better KV handling and error logging
func (p *Processor) evaluateCondition(cond *Condition, msg map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext, kvCtx *KVContext) bool {
	var value interface{}
	var ok bool

	// Check if this is a system field (time, subject, or KV)
	if strings.HasPrefix(cond.Field, "@") {
		// ENHANCED: Try KV context first with improved error handling
		if strings.HasPrefix(cond.Field, "@kv") && kvCtx != nil {
			value, ok = kvCtx.GetFieldWithContext(cond.Field, msg, timeCtx, subjectCtx)
			if ok {
				p.logger.Debug("evaluating KV field condition",
					"field", cond.Field,
					"operator", cond.Operator,
					"expectedValue", cond.Value,
					"actualKVValue", value)
			} else {
				// UPDATED: Changed from DEBUG to WARN with more context
				bucket, key := p.extractBucketAndKey(cond.Field)
				p.logger.Warn("KV field not found in rule condition - condition will fail",
					"field", cond.Field,
					"bucket", bucket,
					"key", key,
					"availableBuckets", kvCtx.GetAllBuckets(),
					"impact", "Rule condition evaluates to false")
				// For conditions, missing KV values should cause condition to fail
				return false
			}
		} else if strings.HasPrefix(cond.Field, "@subject") {
			// Try subject context
			value, ok = subjectCtx.GetField(cond.Field)
			if ok {
				p.logger.Debug("evaluating subject field condition",
					"field", cond.Field,
					"operator", cond.Operator,
					"expectedValue", cond.Value,
					"actualSubjectValue", value)
			}
		}

		// If not found in KV or subject context, try time context
		if !ok {
			value, ok = timeCtx.GetField(cond.Field)
			if ok {
				p.logger.Debug("evaluating time field condition",
					"field", cond.Field,
					"operator", cond.Operator,
					"expectedValue", cond.Value,
					"actualTimeValue", value)
			}
		}

		// ENHANCED: Better error reporting for unknown system fields
		if !ok {
			p.logger.Debug("unknown system field in condition",
				"field", cond.Field,
				"availableTimeFields", timeCtx.GetAllFieldNames(),
				"availableSubjectFields", subjectCtx.GetAllFieldNames(),
				"kvEnabled", kvCtx != nil)
			return false
		}
	} else {
		// Regular message field - check for top-level first for performance
		if !strings.Contains(cond.Field, ".") {
			value, ok = msg[cond.Field]
			if !ok {
				p.logger.Debug("top-level field not found in message",
					"field", cond.Field,
					"availableTopLevelFields", getMapKeys(msg))
				return false
			}
		} else {
			// Nested field - use shared traverser
			path := strings.Split(cond.Field, ".")
			var err error
			value, err = TraverseJSONPath(msg, path)
			if err != nil {
				p.logger.Debug("field not found in message",
					"field", cond.Field,
					"path", path,
					"error", err,
					"availableTopLevelFields", getMapKeys(msg))
				return false
			}
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
		result = p.compareValues(value, cond.Value, "eq")
	case "neq":
		result = p.compareValues(value, cond.Value, "neq")
	case "gt", "lt", "gte", "lte":
		result = p.compareNumeric(value, cond.Value, cond.Operator)
	case "exists":
		result = value != nil
	case "contains":
		result = p.compareContains(value, cond.Value)
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

// NEW: compareValues handles equality/inequality with type awareness
func (p *Processor) compareValues(a, b interface{}, op string) bool {
	// Handle nil values
	if a == nil && b == nil {
		return op == "eq"
	}
	if a == nil || b == nil {
		return op == "neq"
	}

	// Try direct comparison first
	equal := false
	switch va := a.(type) {
	case string:
		if vb, ok := b.(string); ok {
			equal = va == vb
		} else {
			// Convert b to string for comparison
			equal = va == p.convertToString(b)
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
		// Fallback to string comparison
		equal = p.convertToString(a) == p.convertToString(b)
	}

	if op == "eq" {
		return equal
	} else { // "neq"
		return !equal
	}
}

// NEW: compareContains handles string contains operations
func (p *Processor) compareContains(a, b interface{}) bool {
	aStr := p.convertToString(a)
	bStr := p.convertToString(b)

	return strings.Contains(aStr, bStr)
}

func (p *Processor) compareNumeric(a, b interface{}, op string) bool {
	var numA, numB float64
	var err error

	switch v := a.(type) {
	case float64:
		numA = v
	case int:
		numA = float64(v)
	case int64:
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
	case int64:
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

// REMOVED getValueFromPath - now using shared TraverseJSONPath from json_traversal.go

func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

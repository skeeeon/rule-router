// file: internal/rule/evaluator_test.go

package rule

import (
	"fmt"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"rule-router/internal/logger"
)

// Helper to create an evaluator for testing.
func newTestEvaluator() *Evaluator {
	return NewEvaluator(logger.NewNopLogger())
}

// Helper to create a basic evaluation context for NATS-based tests.
func newTestContext(data map[string]interface{}, subject string) *EvaluationContext {
	subjectCtx := NewSubjectContext(subject)
	timeCtx := NewSystemTimeProvider().GetCurrentContext()

	payload, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal test data: %v", err))
	}

	ctx, err := NewEvaluationContext(
		payload,
		nil, // headers
		subjectCtx,
		nil, // httpCtx
		timeCtx,
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNopLogger(),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create evaluation context: %v", err))
	}

	return ctx
}

// TestEvaluateCondition_Equality tests eq and neq operators
func TestEvaluateCondition_Equality(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "string equals - match",
			condition: Condition{Field: "{status}", Operator: "eq", Value: "active"},
			data:      map[string]interface{}{"status": "active"},
			want:      true,
		},
		{
			name:      "string not equals - match",
			condition: Condition{Field: "{status}", Operator: "neq", Value: "error"},
			data:      map[string]interface{}{"status": "active"},
			want:      true,
		},
		{
			name:      "type coercion - string to int",
			condition: Condition{Field: "{port}", Operator: "eq", Value: "8080"},
			data:      map[string]interface{}{"port": 8080},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_Numeric tests numeric comparison operators
func TestEvaluateCondition_Numeric(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "greater than - true",
			condition: Condition{Field: "{temperature}", Operator: "gt", Value: 25},
			data:      map[string]interface{}{"temperature": 30},
			want:      true,
		},
		{
			name:      "less than or equal - equal",
			condition: Condition{Field: "{count}", Operator: "lte", Value: 10},
			data:      map[string]interface{}{"count": 10},
			want:      true,
		},
		{
			name:      "string number comparison",
			condition: Condition{Field: "{port}", Operator: "gt", Value: "8000"},
			data:      map[string]interface{}{"port": "8080"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_Exists tests the exists operator
func TestEvaluateCondition_Exists(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "field exists",
			condition: Condition{Field: "{temperature}", Operator: "exists"},
			data:      map[string]interface{}{"temperature": 25},
			want:      true,
		},
		{
			name:      "field does not exist",
			condition: Condition{Field: "{humidity}", Operator: "exists"},
			data:      map[string]interface{}{"temperature": 25},
			want:      false,
		},
		{
			name:      "field exists with nil value",
			condition: Condition{Field: "{value}", Operator: "exists"},
			data:      map[string]interface{}{"value": nil},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_NestedFields tests nested field access
func TestEvaluateCondition_NestedFields(t *testing.T) {
	evaluator := newTestEvaluator()
	data := map[string]interface{}{
		"user": map[string]interface{}{
			"profile": map[string]interface{}{
				"email": "john@example.com",
			},
		},
	}
	condition := Condition{Field: "{user.profile.email}", Operator: "eq", Value: "john@example.com"}

	context := newTestContext(data, "test.subject")
	if !evaluator.evaluateCondition(&condition, context) {
		t.Error("Failed to evaluate nested field condition")
	}
}

// TestEvaluateCondition_TimeFields tests time-based conditions
func TestEvaluateCondition_TimeFields(t *testing.T) {
	evaluator := newTestEvaluator()
	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	timeProvider := NewMockTimeProvider(fixedTime)

	context, _ := NewEvaluationContext([]byte("{}"), nil, NewSubjectContext("test.subject"), nil, timeProvider.GetCurrentContext(), nil, nil, logger.NewNopLogger())
	condition := Condition{Field: "{@time.hour}", Operator: "eq", Value: 14}

	if !evaluator.evaluateCondition(&condition, context) {
		t.Error("Failed to evaluate time field condition")
	}
}

// TestEvaluateCondition_SubjectFields tests subject-based conditions
func TestEvaluateCondition_SubjectFields(t *testing.T) {
	evaluator := newTestEvaluator()
	context := newTestContext(map[string]interface{}{}, "sensors.temperature.room1")
	condition := Condition{Field: "{@subject.1}", Operator: "eq", Value: "temperature"}

	if !evaluator.evaluateCondition(&condition, context) {
		t.Error("Failed to evaluate subject field condition")
	}
}

// TestEvaluateConditions_LogicalOperators tests AND/OR logic
func TestEvaluateConditions_LogicalOperators(t *testing.T) {
	evaluator := newTestEvaluator()
	conditions := Conditions{
		Operator: "and",
		Items: []Condition{
			{Field: "{temperature}", Operator: "gt", Value: 20},
			{Field: "{status}", Operator: "eq", Value: "active"},
		},
	}
	data := map[string]interface{}{"temperature": 25, "status": "active"}

	context := newTestContext(data, "test.subject")
	if !evaluator.Evaluate(&conditions, context) {
		t.Error("Failed to evaluate AND condition group")
	}
}

// TestEvaluateConditions_NestedGroups tests nested condition groups
func TestEvaluateConditions_NestedGroups(t *testing.T) {
	evaluator := newTestEvaluator()
	conditions := Conditions{
		Operator: "and",
		Items:    []Condition{{Field: "{active}", Operator: "eq", Value: true}},
		Groups: []Conditions{
			{
				Operator: "or",
				Items: []Condition{
					{Field: "{temperature}", Operator: "gt", Value: 30},
					{Field: "{humidity}", Operator: "gt", Value: 80},
				},
			},
		},
	}
	data := map[string]interface{}{"active": true, "temperature": 35, "humidity": 70}

	context := newTestContext(data, "test.subject")
	if !evaluator.Evaluate(&conditions, context) {
		t.Error("Failed to evaluate nested condition group")
	}
}

// ========================================
// VARIABLE-TO-VARIABLE COMPARISON TESTS
// ========================================

// TestEvaluator_VariableToVariableComparison tests the new variable comparison feature
func TestEvaluator_VariableToVariableComparison(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name: "number to number - greater than (true)",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"temperature": 105,
				"threshold":   100,
			},
			want: true,
		},
		{
			name: "number to number - greater than (false)",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"temperature": 95,
				"threshold":   100,
			},
			want: false,
		},
		{
			name: "number to number - equals (true)",
			condition: Condition{
				Field:    "{count}",
				Operator: "eq",
				Value:    "{expected_count}",
			},
			data: map[string]interface{}{
				"count":          42,
				"expected_count": 42,
			},
			want: true,
		},
		{
			name: "string to string - equals (true)",
			condition: Condition{
				Field:    "{status}",
				Operator: "eq",
				Value:    "{expected_status}",
			},
			data: map[string]interface{}{
				"status":          "active",
				"expected_status": "active",
			},
			want: true,
		},
		{
			name: "string to string - not equals (true)",
			condition: Condition{
				Field:    "{status}",
				Operator: "neq",
				Value:    "{expected_status}",
			},
			data: map[string]interface{}{
				"status":          "active",
				"expected_status": "inactive",
			},
			want: true,
		},
		{
			name: "float comparison - less than or equal (true)",
			condition: Condition{
				Field:    "{current_value}",
				Operator: "lte",
				Value:    "{max_value}",
			},
			data: map[string]interface{}{
				"current_value": 99.5,
				"max_value":     100.0,
			},
			want: true,
		},
		{
			name: "boolean comparison - equals (true)",
			condition: Condition{
				Field:    "{enabled}",
				Operator: "eq",
				Value:    "{expected_enabled}",
			},
			data: map[string]interface{}{
				"enabled":          true,
				"expected_enabled": true,
			},
			want: true,
		},
		{
			name: "timestamp comparison - greater than (true)",
			condition: Condition{
				Field:    "{end_time}",
				Operator: "gt",
				Value:    "{start_time}",
			},
			data: map[string]interface{}{
				"end_time":   1700000000,
				"start_time": 1699000000,
			},
			want: true,
		},
		{
			name: "nested field to nested field comparison",
			condition: Condition{
				Field:    "{sensor.reading}",
				Operator: "gt",
				Value:    "{sensor.threshold}",
			},
			data: map[string]interface{}{
				"sensor": map[string]interface{}{
					"reading":   150,
					"threshold": 100,
				},
			},
			want: true,
		},
		{
			name: "variable not found in value - should fail gracefully",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "gt",
				Value:    "{missing_threshold}",
			},
			data: map[string]interface{}{
				"temperature": 105,
			},
			want: false,
		},
		{
			name: "variable not found in field - should fail gracefully",
			condition: Condition{
				Field:    "{missing_temperature}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"threshold": 100,
			},
			want: false,
		},
		{
			name: "both variables missing - should fail gracefully",
			condition: Condition{
				Field:    "{missing_field}",
				Operator: "gt",
				Value:    "{missing_value}",
			},
			data: map[string]interface{}{
				"other": "data",
			},
			want: false,
		},
		{
			name: "type coercion - string to number comparison",
			condition: Condition{
				Field:    "{string_number}",
				Operator: "gt",
				Value:    "{number}",
			},
			data: map[string]interface{}{
				"string_number": "100",
				"number":        50,
			},
			want: true,
		},
		{
			name: "variable to literal - should work (backward compatibility)",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "gt",
				Value:    100,
			},
			data: map[string]interface{}{
				"temperature": 105,
			},
			want: true,
		},
		{
			name: "literal field to variable value - field is not template",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "eq",
				Value:    "{status}",
			},
			data: map[string]interface{}{
				"temperature": "active",
				"status":      "active",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v\nField: %v, Value: %v, Data: %+v",
					got, tt.want, tt.condition.Field, tt.condition.Value, tt.data)
			}
		})
	}
}

// TestEvaluator_VariableComparison_WithSystemVariables tests variable comparisons using system variables
func TestEvaluator_VariableComparison_WithSystemVariables(t *testing.T) {
	evaluator := newTestEvaluator()
	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	timeProvider := NewMockTimeProvider(fixedTime)

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name: "compare message field to system time",
			condition: Condition{
				Field:    "{expected_hour}",
				Operator: "eq",
				Value:    "{@time.hour}",
			},
			data: map[string]interface{}{
				"expected_hour": 14,
			},
			want: true,
		},
		{
			name: "compare system time to literal",
			condition: Condition{
				Field:    "{@time.hour}",
				Operator: "gte",
				Value:    9,
			},
			data: map[string]interface{}{},
			want: true,
		},
		{
			name: "compare two system variables",
			condition: Condition{
				Field:    "{@time.hour}",
				Operator: "lt",
				Value:    "{@time.minute}",
			},
			data: map[string]interface{}{},
			want: true, // hour=14, minute=30, so 14 < 30
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(tt.data)
			context, _ := NewEvaluationContext(
				payload,
				nil,
				NewSubjectContext("test.subject"),
				nil,
				timeProvider.GetCurrentContext(),
				nil,
				nil,
				logger.NewNopLogger(),
			)
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluator_VariableComparison_TypePreservation tests that types are preserved correctly
func TestEvaluator_VariableComparison_TypePreservation(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name: "float to float - precise comparison",
			condition: Condition{
				Field:    "{reading}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"reading":   25.5,
				"threshold": 25.4,
			},
			want: true,
		},
		{
			name: "int to int comparison",
			condition: Condition{
				Field:    "{count}",
				Operator: "eq",
				Value:    "{expected}",
			},
			data: map[string]interface{}{
				"count":    42,
				"expected": 42,
			},
			want: true,
		},
		{
			name: "mixed int and float - type coercion",
			condition: Condition{
				Field:    "{int_value}",
				Operator: "lt",
				Value:    "{float_value}",
			},
			data: map[string]interface{}{
				"int_value":   42,
				"float_value": 42.5,
			},
			want: true,
		},
		{
			name: "string comparison - case sensitive",
			condition: Condition{
				Field:    "{status}",
				Operator: "eq",
				Value:    "{expected}",
			},
			data: map[string]interface{}{
				"status":   "Active",
				"expected": "active",
			},
			want: false, // Case sensitive
		},
		{
			name: "boolean comparison",
			condition: Condition{
				Field:    "{flag1}",
				Operator: "eq",
				Value:    "{flag2}",
			},
			data: map[string]interface{}{
				"flag1": true,
				"flag2": true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluator_VariableComparison_RealWorldScenarios tests realistic use cases
func TestEvaluator_VariableComparison_RealWorldScenarios(t *testing.T) {
	evaluator := newTestEvaluator()

	t.Run("booking validation - end after start", func(t *testing.T) {
		data := map[string]interface{}{
			"booking": map[string]interface{}{
				"start_time": 1700000000,
				"end_time":   1700003600, // 1 hour later
			},
		}
		condition := Condition{
			Field:    "{booking.end_time}",
			Operator: "gt",
			Value:    "{booking.start_time}",
		}
		context := newTestContext(data, "bookings.validate")
		if !evaluator.evaluateCondition(&condition, context) {
			t.Error("Booking validation should pass when end_time > start_time")
		}
	})

	t.Run("temperature threshold check", func(t *testing.T) {
		data := map[string]interface{}{
			"sensor": map[string]interface{}{
				"current_temp": 85,
				"max_temp":     80,
			},
		}
		condition := Condition{
			Field:    "{sensor.current_temp}",
			Operator: "gt",
			Value:    "{sensor.max_temp}",
		}
		context := newTestContext(data, "sensors.check")
		if !evaluator.evaluateCondition(&condition, context) {
			t.Error("Should trigger alert when current_temp > max_temp")
		}
	})

	t.Run("permission level check", func(t *testing.T) {
		data := map[string]interface{}{
			"user": map[string]interface{}{
				"level": 5,
			},
			"resource": map[string]interface{}{
				"required_level": 3,
			},
		}
		condition := Condition{
			Field:    "{user.level}",
			Operator: "gte",
			Value:    "{resource.required_level}",
		}
		context := newTestContext(data, "access.check")
		if !evaluator.evaluateCondition(&condition, context) {
			t.Error("User should have access when level >= required_level")
		}
	})

	t.Run("rate limit check", func(t *testing.T) {
		data := map[string]interface{}{
			"current_requests": 450,
			"max_requests":     500,
		}
		condition := Condition{
			Field:    "{current_requests}",
			Operator: "lt",
			Value:    "{max_requests}",
		}
		context := newTestContext(data, "api.request")
		if !evaluator.evaluateCondition(&condition, context) {
			t.Error("Should allow request when current < max")
		}
	})

	t.Run("range validation - within bounds", func(t *testing.T) {
		data := map[string]interface{}{
			"value": 50,
			"min":   10,
			"max":   100,
		}
		conditions := Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{value}", Operator: "gte", Value: "{min}"},
				{Field: "{value}", Operator: "lte", Value: "{max}"},
			},
		}
		context := newTestContext(data, "validation.range")
		if !evaluator.Evaluate(&conditions, context) {
			t.Error("Value should be within range")
		}
	})
}

// TestEvaluator_VariableComparison_ErrorHandling tests error conditions
func TestEvaluator_VariableComparison_ErrorHandling(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
		desc      string
	}{
		{
			name: "nil value in comparison",
			condition: Condition{
				Field:    "{value}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"value":     nil,
				"threshold": 100,
			},
			want: false,
			desc: "Should fail gracefully when field is nil",
		},
		{
			name: "comparing against nil",
			condition: Condition{
				Field:    "{value}",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"value":     100,
				"threshold": nil,
			},
			want: false,
			desc: "Should fail gracefully when value is nil",
		},
		{
			name: "incompatible type comparison",
			condition: Condition{
				Field:    "{text}",
				Operator: "gt",
				Value:    "{number}",
			},
			data: map[string]interface{}{
				"text":   "hello",
				"number": 42,
			},
			want: false,
			desc: "Should fail when comparing incompatible types",
		},
		{
			name: "malformed template in field - no closing brace",
			condition: Condition{
				Field:    "{temperature",
				Operator: "gt",
				Value:    "{threshold}",
			},
			data: map[string]interface{}{
				"temperature": 100,
				"threshold":   50,
			},
			want: false,
			desc: "Should fail with malformed template",
		},
		{
			name: "empty template in value",
			condition: Condition{
				Field:    "{temperature}",
				Operator: "gt",
				Value:    "{}",
			},
			data: map[string]interface{}{
				"temperature": 100,
			},
			want: false,
			desc: "Should fail with empty template",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.condition, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v (%s)", got, tt.want, tt.desc)
			}
		})
	}
}

// ========================================
// ARRAY OPERATOR TESTS
// ========================================

// TestArrayOperator_Any tests the "any" array operator
func TestArrayOperator_Any(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name string
		data map[string]interface{}
		cond Condition
		want bool
	}{
		{
			name: "any - one match",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "inactive"},
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "inactive"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "any - multiple matches",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "inactive"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "any - no matches",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "inactive"},
					map[string]interface{}{"status": "inactive"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "any - empty array",
			data: map[string]interface{}{
				"items": []interface{}{},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "any - mixed array with non-objects",
			data: map[string]interface{}{
				"items": []interface{}{
					"not-an-object",
					42,
					map[string]interface{}{"status": "active"},
					true,
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "any - only non-objects",
			data: map[string]interface{}{
				"items": []interface{}{"string", 42, true, 3.14},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_All tests the "all" array operator
func TestArrayOperator_All(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name string
		data map[string]interface{}
		cond Condition
		want bool
	}{
		{
			name: "all - all match",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "all - one doesn't match",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "inactive"},
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "all - empty array",
			data: map[string]interface{}{
				"items": []interface{}{},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "all - STRICT: mixed array fails immediately",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
					"not-an-object", // FAILS HERE
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "all - STRICT: non-object at end fails",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
					map[string]interface{}{"status": "active"},
					42, // FAILS HERE
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "all - STRICT: only non-objects",
			data: map[string]interface{}{
				"items": []interface{}{"string", 42, true},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_None tests the "none" array operator
func TestArrayOperator_None(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name string
		data map[string]interface{}
		cond Condition
		want bool
	}{
		{
			name: "none - no matches",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "inactive"},
					map[string]interface{}{"status": "inactive"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "none - one match",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "inactive"},
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "none - empty array",
			data: map[string]interface{}{
				"items": []interface{}{},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "none - mixed array with non-objects (no object matches)",
			data: map[string]interface{}{
				"items": []interface{}{
					"not-an-object",
					42,
					map[string]interface{}{"status": "inactive"},
					true,
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
		{
			name: "none - only non-objects",
			data: map[string]interface{}{
				"items": []interface{}{"string", 42, true},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_ComplexConditions tests nested AND/OR within array operators
func TestArrayOperator_ComplexConditions(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name string
		data map[string]interface{}
		cond Condition
		want bool
	}{
		{
			name: "any with AND conditions",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active", "priority": "high"},
					map[string]interface{}{"status": "inactive", "priority": "high"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items: []Condition{
						{Field: "{status}", Operator: "eq", Value: "active"},
						{Field: "{priority}", Operator: "eq", Value: "high"},
					},
				},
			},
			want: true,
		},
		{
			name: "all with OR conditions",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active", "priority": "high"},
					map[string]interface{}{"status": "inactive", "priority": "high"},
					map[string]interface{}{"status": "active", "priority": "low"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "or",
					Items: []Condition{
						{Field: "{status}", Operator: "eq", Value: "active"},
						{Field: "{priority}", Operator: "eq", Value: "high"},
					},
				},
			},
			want: true,
		},
		{
			name: "any with nested field access",
			data: map[string]interface{}{
				"notifications": []interface{}{
					map[string]interface{}{
						"event": map[string]interface{}{
							"type": "MOTION_DETECTED",
						},
					},
					map[string]interface{}{
						"event": map[string]interface{}{
							"type": "DOOR_OPENED",
						},
					},
				},
			},
			cond: Condition{
				Field:    "{notifications}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{event.type}", Operator: "eq", Value: "MOTION_DETECTED"}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_EdgeCases tests edge cases and error conditions
func TestArrayOperator_EdgeCases(t *testing.T) {
	evaluator := newTestEvaluator()

	tests := []struct {
		name string
		data map[string]interface{}
		cond Condition
		want bool
	}{
		{
			name: "array operator on non-array field",
			data: map[string]interface{}{
				"items": "not-an-array",
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "array operator on non-existent field",
			data: map[string]interface{}{
				"other": "value",
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
		},
		{
			name: "array operator with nil nested conditions",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:      "{items}",
				Operator:   "any",
				Conditions: nil,
			},
			want: false,
		},
		{
			name: "single element array - all matches",
			data: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"status": "active"},
				},
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_RealWorldScenario tests a realistic batch notification use case
func TestArrayOperator_RealWorldScenario(t *testing.T) {
	evaluator := newTestEvaluator()

	// Real-world batch notification payload
	data := map[string]interface{}{
		"siteId": "site-123",
		"type":   "NOTIFICATION",
		"notification": []interface{}{
			map[string]interface{}{
				"id":   "evt-001",
				"type": "DEVICE_MOTION_START",
				"event": map[string]interface{}{
					"alarmId":   "alarm-abc",
					"alarmName": "Motion Detected",
				},
			},
			map[string]interface{}{
				"id":   "evt-002",
				"type": "DEVICE_DOOR_OPEN",
				"event": map[string]interface{}{
					"alarmId":   "alarm-xyz",
					"alarmName": "Door Opened",
				},
			},
			map[string]interface{}{
				"id":   "evt-003",
				"type": "DEVICE_MOTION_START",
				"event": map[string]interface{}{
					"alarmId":   "alarm-def",
					"alarmName": "Motion Detected",
				},
			},
		},
	}

	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{
			name: "check if ANY notification is motion",
			cond: Condition{
				Field:    "{notification}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{type}", Operator: "eq", Value: "DEVICE_MOTION_START"}},
				},
			},
			want: true,
		},
		{
			name: "check if ALL notifications are motion (should be false)",
			cond: Condition{
				Field:    "{notification}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{type}", Operator: "eq", Value: "DEVICE_MOTION_START"}},
				},
			},
			want: false,
		},
		{
			name: "check if NONE are temperature alerts",
			cond: Condition{
				Field:    "{notification}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{type}", Operator: "eq", Value: "DEVICE_TEMP_ALERT"}},
				},
			},
			want: true,
		},
		{
			name: "check if ANY have specific alarm ID with nested field",
			cond: Condition{
				Field:    "{notification}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{event.alarmId}", Operator: "eq", Value: "alarm-abc"}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(data, "device.events")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArrayOperator_ShortCircuitBehavior tests that operators short-circuit correctly
func TestArrayOperator_ShortCircuitBehavior(t *testing.T) {
	evaluator := newTestEvaluator()

	// Large array where first element matches (for "any")
	largeArrayFirstMatch := []interface{}{
		map[string]interface{}{"status": "active"},
	}
	for i := 0; i < 1000; i++ {
		largeArrayFirstMatch = append(largeArrayFirstMatch, map[string]interface{}{"status": "inactive"})
	}

	// Large array where first element doesn't match (for "all")
	largeArrayFirstNoMatch := []interface{}{
		map[string]interface{}{"status": "inactive"},
	}
	for i := 0; i < 1000; i++ {
		largeArrayFirstNoMatch = append(largeArrayFirstNoMatch, map[string]interface{}{"status": "active"})
	}

	tests := []struct {
		name  string
		data  map[string]interface{}
		cond  Condition
		want  bool
		desc  string
	}{
		{
			name: "any short-circuits on first match",
			data: map[string]interface{}{
				"items": largeArrayFirstMatch,
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "any",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: true,
			desc: "Should return true immediately without checking remaining 1000 elements",
		},
		{
			name: "all short-circuits on first non-match",
			data: map[string]interface{}{
				"items": largeArrayFirstNoMatch,
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "all",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
			desc: "Should return false immediately without checking remaining 1000 elements",
		},
		{
			name: "none short-circuits on first match",
			data: map[string]interface{}{
				"items": largeArrayFirstMatch,
			},
			cond: Condition{
				Field:    "{items}",
				Operator: "none",
				Conditions: &Conditions{
					Operator: "and",
					Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
				},
			},
			want: false,
			desc: "Should return false immediately without checking remaining 1000 elements",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestContext(tt.data, "test.subject")
			got := evaluator.evaluateCondition(&tt.cond, context)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v - %s", got, tt.want, tt.desc)
			}
		})
	}
}

// --- Benchmarks ---

func BenchmarkEvaluateCondition_Simple(b *testing.B) {
	evaluator := newTestEvaluator()
	condition := Condition{Field: "{temperature}", Operator: "gt", Value: 25}
	context := newTestContext(map[string]interface{}{"temperature": 30}, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&condition, context)
	}
}

func BenchmarkEvaluateConditions_Complex(b *testing.B) {
	evaluator := newTestEvaluator()
	conditions := Conditions{
		Operator: "and",
		Items:    []Condition{{Field: "{active}", Operator: "eq", Value: true}},
		Groups: []Conditions{
			{
				Operator: "or",
				Items: []Condition{
					{Field: "{temperature}", Operator: "gt", Value: 30},
					{Field: "{user.profile.tier}", Operator: "eq", Value: "premium"},
				},
			},
		},
	}
	data := map[string]interface{}{
		"active":      true,
		"temperature": 35,
		"user":        map[string]interface{}{"profile": map[string]interface{}{"tier": "standard"}},
	}
	context := newTestContext(data, "sensors.temperature")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.Evaluate(&conditions, context)
	}
}

// BenchmarkEvaluateCondition_VariableComparison benchmarks variable-to-variable comparisons
func BenchmarkEvaluateCondition_VariableComparison(b *testing.B) {
	evaluator := newTestEvaluator()
	condition := Condition{Field: "{temperature}", Operator: "gt", Value: "{threshold}"}
	context := newTestContext(map[string]interface{}{"temperature": 105, "threshold": 100}, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&condition, context)
	}
}

// BenchmarkArrayOperator_Any benchmarks "any" operator performance
func BenchmarkArrayOperator_Any(b *testing.B) {
	evaluator := newTestEvaluator()

	// 100-element array with match at position 50
	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		status := "inactive"
		if i == 50 {
			status = "active"
		}
		items[i] = map[string]interface{}{"status": status}
	}

	data := map[string]interface{}{"items": items}
	cond := Condition{
		Field:    "{items}",
		Operator: "any",
		Conditions: &Conditions{
			Operator: "and",
			Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
		},
	}
	context := newTestContext(data, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&cond, context)
	}
}

// BenchmarkArrayOperator_All benchmarks "all" operator performance
func BenchmarkArrayOperator_All(b *testing.B) {
	evaluator := newTestEvaluator()

	// 100-element array where all match
	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]interface{}{"status": "active"}
	}

	data := map[string]interface{}{"items": items}
	cond := Condition{
		Field:    "{items}",
		Operator: "all",
		Conditions: &Conditions{
			Operator: "and",
			Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
		},
	}
	context := newTestContext(data, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&cond, context)
	}
}

// BenchmarkArrayOperator_None benchmarks "none" operator performance
func BenchmarkArrayOperator_None(b *testing.B) {
	evaluator := newTestEvaluator()

	// 100-element array where none match
	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]interface{}{"status": "inactive"}
	}

	data := map[string]interface{}{"items": items}
	cond := Condition{
		Field:    "{items}",
		Operator: "none",
		Conditions: &Conditions{
			Operator: "and",
			Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
		},
	}
	context := newTestContext(data, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&cond, context)
	}
}

// BenchmarkArrayOperator_ShortCircuit benchmarks short-circuit performance
func BenchmarkArrayOperator_ShortCircuit(b *testing.B) {
	evaluator := newTestEvaluator()

	// 1000-element array with match at first position
	items := make([]interface{}, 1000)
	items[0] = map[string]interface{}{"status": "active"}
	for i := 1; i < 1000; i++ {
		items[i] = map[string]interface{}{"status": "inactive"}
	}

	data := map[string]interface{}{"items": items}
	cond := Condition{
		Field:    "{items}",
		Operator: "any",
		Conditions: &Conditions{
			Operator: "and",
			Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
		},
	}
	context := newTestContext(data, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&cond, context)
	}
}

// BenchmarkArrayOperator_MixedArray benchmarks performance with mixed types
func BenchmarkArrayOperator_MixedArray(b *testing.B) {
	evaluator := newTestEvaluator()

	// 100-element mixed array
	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		if i%3 == 0 {
			items[i] = "string"
		} else if i%3 == 1 {
			items[i] = 42
		} else {
			items[i] = map[string]interface{}{"status": "active"}
		}
	}

	data := map[string]interface{}{"items": items}
	cond := Condition{
		Field:    "{items}",
		Operator: "any",
		Conditions: &Conditions{
			Operator: "and",
			Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
		},
	}
	context := newTestContext(data, "test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluator.evaluateCondition(&cond, context)
	}
}

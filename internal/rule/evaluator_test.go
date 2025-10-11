// file: internal/rule/evaluator_test.go

package rule

import (
	"testing"
	"time"

	"rule-router/internal/logger"
)

// Helper to create an evaluator for testing.
func newTestEvaluator() *Evaluator {
	return NewEvaluator(logger.NewNopLogger())
}

// Helper to create a basic evaluation context for tests.
func newTestContext(data map[string]interface{}, subject string) *EvaluationContext {
	// For most evaluator tests, we don't need real time or KV.
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext(subject)

	// We can ignore the error here as test data is controlled.
	ctx, _ := NewEvaluationContext([]byte("{}"), nil, subjectCtx, timeCtx, nil)
	ctx.Msg = data // Directly set the unmarshalled message data.
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
			condition: Condition{Field: "status", Operator: "eq", Value: "active"},
			data:      map[string]interface{}{"status": "active"},
			want:      true,
		},
		{
			name:      "string not equals - match",
			condition: Condition{Field: "status", Operator: "neq", Value: "error"},
			data:      map[string]interface{}{"status": "active"},
			want:      true,
		},
		{
			name:      "type coercion - string to int",
			condition: Condition{Field: "port", Operator: "eq", Value: "8080"},
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
			condition: Condition{Field: "temperature", Operator: "gt", Value: 25},
			data:      map[string]interface{}{"temperature": 30},
			want:      true,
		},
		{
			name:      "less than or equal - equal",
			condition: Condition{Field: "count", Operator: "lte", Value: 10},
			data:      map[string]interface{}{"count": 10},
			want:      true,
		},
		{
			name:      "string number comparison",
			condition: Condition{Field: "port", Operator: "gt", Value: "8000"},
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
			condition: Condition{Field: "temperature", Operator: "exists"},
			data:      map[string]interface{}{"temperature": 25},
			want:      true,
		},
		{
			name:      "field does not exist",
			condition: Condition{Field: "humidity", Operator: "exists"},
			data:      map[string]interface{}{"temperature": 25},
			want:      false,
		},
		{
			name:      "field exists with nil value",
			condition: Condition{Field: "value", Operator: "exists"},
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
	condition := Condition{Field: "user.profile.email", Operator: "eq", Value: "john@example.com"}

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

	context, _ := NewEvaluationContext([]byte("{}"), nil, NewSubjectContext("test.subject"), timeProvider.GetCurrentContext(), nil)
	condition := Condition{Field: "@time.hour", Operator: "eq", Value: 14}

	if !evaluator.evaluateCondition(&condition, context) {
		t.Error("Failed to evaluate time field condition")
	}
}

// TestEvaluateCondition_SubjectFields tests subject-based conditions
func TestEvaluateCondition_SubjectFields(t *testing.T) {
	evaluator := newTestEvaluator()
	context := newTestContext(map[string]interface{}{}, "sensors.temperature.room1")
	condition := Condition{Field: "@subject.1", Operator: "eq", Value: "temperature"}

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
			{Field: "temperature", Operator: "gt", Value: 20},
			{Field: "status", Operator: "eq", Value: "active"},
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
		Items: []Condition{{Field: "active", Operator: "eq", Value: true}},
		Groups: []Conditions{
			{
				Operator: "or",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 30},
					{Field: "humidity", Operator: "gt", Value: 80},
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

// --- Benchmarks ---

func BenchmarkEvaluateCondition_Simple(b *testing.B) {
	evaluator := newTestEvaluator()
	condition := Condition{Field: "temperature", Operator: "gt", Value: 25}
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
		Items:    []Condition{{Field: "active", Operator: "eq", Value: true}},
		Groups: []Conditions{
			{
				Operator: "or",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 30},
					{Field: "user.profile.tier", Operator: "eq", Value: "premium"},
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

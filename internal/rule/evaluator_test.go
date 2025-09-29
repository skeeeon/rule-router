package rule

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"rule-router/internal/logger"
)

// Helper to create processor for testing
func newTestProcessor() *Processor {
	zapLogger := zap.NewNop()
	return &Processor{
		logger: &logger.Logger{Logger: zapLogger},
	}
}

// TestEvaluateCondition_Equality tests eq and neq operators
func TestEvaluateCondition_Equality(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

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
			name:      "string equals - no match",
			condition: Condition{Field: "status", Operator: "eq", Value: "active"},
			data:      map[string]interface{}{"status": "inactive"},
			want:      false,
		},
		{
			name:      "string not equals - match",
			condition: Condition{Field: "status", Operator: "neq", Value: "error"},
			data:      map[string]interface{}{"status": "active"},
			want:      true,
		},
		{
			name:      "integer equals",
			condition: Condition{Field: "temperature", Operator: "eq", Value: 25},
			data:      map[string]interface{}{"temperature": 25},
			want:      true,
		},
		{
			name:      "float equals",
			condition: Condition{Field: "temperature", Operator: "eq", Value: 25.5},
			data:      map[string]interface{}{"temperature": 25.5},
			want:      true,
		},
		{
			name:      "boolean equals true",
			condition: Condition{Field: "active", Operator: "eq", Value: true},
			data:      map[string]interface{}{"active": true},
			want:      true,
		},
		{
			name:      "boolean equals false",
			condition: Condition{Field: "active", Operator: "eq", Value: false},
			data:      map[string]interface{}{"active": false},
			want:      true,
		},
		{
			name:      "nil equals nil",
			condition: Condition{Field: "value", Operator: "eq", Value: nil},
			data:      map[string]interface{}{"value": nil},
			want:      true,
		},
		{
			name:      "type coercion - string to int",
			condition: Condition{Field: "port", Operator: "eq", Value: "8080"},
			data:      map[string]interface{}{"port": 8080},
			want:      true, // Type coercion: "8080" == 8080
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_Numeric tests numeric comparison operators
func TestEvaluateCondition_Numeric(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

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
			name:      "greater than - false",
			condition: Condition{Field: "temperature", Operator: "gt", Value: 25},
			data:      map[string]interface{}{"temperature": 20},
			want:      false,
		},
		{
			name:      "greater than - equal (false)",
			condition: Condition{Field: "temperature", Operator: "gt", Value: 25},
			data:      map[string]interface{}{"temperature": 25},
			want:      false,
		},
		{
			name:      "less than - true",
			condition: Condition{Field: "temperature", Operator: "lt", Value: 25},
			data:      map[string]interface{}{"temperature": 20},
			want:      true,
		},
		{
			name:      "greater than or equal - equal",
			condition: Condition{Field: "count", Operator: "gte", Value: 10},
			data:      map[string]interface{}{"count": 10},
			want:      true,
		},
		{
			name:      "greater than or equal - greater",
			condition: Condition{Field: "count", Operator: "gte", Value: 10},
			data:      map[string]interface{}{"count": 15},
			want:      true,
		},
		{
			name:      "less than or equal - equal",
			condition: Condition{Field: "count", Operator: "lte", Value: 10},
			data:      map[string]interface{}{"count": 10},
			want:      true,
		},
		{
			name:      "float comparison",
			condition: Condition{Field: "temperature", Operator: "gt", Value: 25.5},
			data:      map[string]interface{}{"temperature": 25.7},
			want:      true,
		},
		{
			name:      "int vs float comparison",
			condition: Condition{Field: "temperature", Operator: "gt", Value: 25},
			data:      map[string]interface{}{"temperature": 25.5},
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
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_Exists tests the exists operator
func TestEvaluateCondition_Exists(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

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
			want:      false, // Current behavior: exists checks value != nil
		},
		{
			name:      "field exists with zero value",
			condition: Condition{Field: "count", Operator: "exists"},
			data:      map[string]interface{}{"count": 0},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_Contains tests string contains operator
func TestEvaluateCondition_Contains(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "string contains - match",
			condition: Condition{Field: "message", Operator: "contains", Value: "error"},
			data:      map[string]interface{}{"message": "an error occurred"},
			want:      true,
		},
		{
			name:      "string contains - no match",
			condition: Condition{Field: "message", Operator: "contains", Value: "warning"},
			data:      map[string]interface{}{"message": "an error occurred"},
			want:      false,
		},
		{
			name:      "string contains - case sensitive",
			condition: Condition{Field: "message", Operator: "contains", Value: "Error"},
			data:      map[string]interface{}{"message": "an error occurred"},
			want:      false,
		},
		{
			name:      "string contains - substring at start",
			condition: Condition{Field: "message", Operator: "contains", Value: "an"},
			data:      map[string]interface{}{"message": "an error occurred"},
			want:      true,
		},
		{
			name:      "string contains - substring at end",
			condition: Condition{Field: "message", Operator: "contains", Value: "occurred"},
			data:      map[string]interface{}{"message": "an error occurred"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_NestedFields tests nested field access
func TestEvaluateCondition_NestedFields(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "two level nesting",
			condition: Condition{Field: "user.name", Operator: "eq", Value: "John"},
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "John",
				},
			},
			want: true,
		},
		{
			name:      "three level nesting",
			condition: Condition{Field: "user.profile.email", Operator: "eq", Value: "john@example.com"},
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"profile": map[string]interface{}{
						"email": "john@example.com",
					},
				},
			},
			want: true,
		},
		{
			name:      "nested numeric comparison",
			condition: Condition{Field: "device.settings.threshold", Operator: "gt", Value: 30},
			data: map[string]interface{}{
				"device": map[string]interface{}{
					"settings": map[string]interface{}{
						"threshold": 35,
					},
				},
			},
			want: true,
		},
		{
			name:      "missing nested field",
			condition: Condition{Field: "user.profile.missing", Operator: "eq", Value: "test"},
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "John",
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_TimeFields tests time-based conditions
func TestEvaluateCondition_TimeFields(t *testing.T) {
	processor := newTestProcessor()
	
	// Friday, March 15, 2024 at 14:30 UTC
	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	timeProvider := NewMockTimeProvider(fixedTime)
	timeCtx := timeProvider.GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name      string
		condition Condition
		want      bool
	}{
		{
			name:      "time hour equals",
			condition: Condition{Field: "@time.hour", Operator: "eq", Value: 14},
			want:      true,
		},
		{
			name:      "time hour greater than",
			condition: Condition{Field: "@time.hour", Operator: "gte", Value: 9},
			want:      true,
		},
		{
			name:      "business hours check",
			condition: Condition{Field: "@time.hour", Operator: "lt", Value: 17},
			want:      true,
		},
		{
			name:      "day name equals",
			condition: Condition{Field: "@day.name", Operator: "eq", Value: "friday"},
			want:      true,
		},
		{
			name:      "day number weekday check",
			condition: Condition{Field: "@day.number", Operator: "lte", Value: 5},
			want:      true,
		},
		{
			name:      "date year check",
			condition: Condition{Field: "@date.year", Operator: "eq", Value: 2024},
			want:      true,
		},
	}

	data := map[string]interface{}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_SubjectFields tests subject-based conditions
func TestEvaluateCondition_SubjectFields(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature.room1")

	tests := []struct {
		name      string
		condition Condition
		want      bool
	}{
		{
			name:      "subject token equals",
			condition: Condition{Field: "@subject.0", Operator: "eq", Value: "sensors"},
			want:      true,
		},
		{
			name:      "subject token not equals",
			condition: Condition{Field: "@subject.1", Operator: "neq", Value: "humidity"},
			want:      true,
		},
		{
			name:      "subject count check",
			condition: Condition{Field: "@subject.count", Operator: "eq", Value: 3},
			want:      true,
		},
		{
			name:      "subject contains check",
			condition: Condition{Field: "@subject", Operator: "contains", Value: "temperature"},
			want:      true,
		},
	}

	data := map[string]interface{}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateConditions_LogicalOperators tests AND/OR logic
func TestEvaluateConditions_LogicalOperators(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name       string
		conditions Conditions
		data       map[string]interface{}
		want       bool
	}{
		{
			name: "AND - all true",
			conditions: Conditions{
				Operator: "and",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 20},
					{Field: "status", Operator: "eq", Value: "active"},
				},
			},
			data: map[string]interface{}{
				"temperature": 25,
				"status":      "active",
			},
			want: true,
		},
		{
			name: "AND - one false",
			conditions: Conditions{
				Operator: "and",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 20},
					{Field: "status", Operator: "eq", Value: "active"},
				},
			},
			data: map[string]interface{}{
				"temperature": 25,
				"status":      "inactive",
			},
			want: false,
		},
		{
			name: "OR - all false",
			conditions: Conditions{
				Operator: "or",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 30},
					{Field: "status", Operator: "eq", Value: "error"},
				},
			},
			data: map[string]interface{}{
				"temperature": 25,
				"status":      "active",
			},
			want: false,
		},
		{
			name: "OR - one true",
			conditions: Conditions{
				Operator: "or",
				Items: []Condition{
					{Field: "temperature", Operator: "gt", Value: 30},
					{Field: "status", Operator: "eq", Value: "active"},
				},
			},
			data: map[string]interface{}{
				"temperature": 25,
				"status":      "active",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateConditions(&tt.conditions, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateConditions() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateConditions_NestedGroups tests nested condition groups
func TestEvaluateConditions_NestedGroups(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name       string
		conditions Conditions
		data       map[string]interface{}
		want       bool
	}{
		{
			name: "nested AND within OR",
			conditions: Conditions{
				Operator: "or",
				Groups: []Conditions{
					{
						Operator: "and",
						Items: []Condition{
							{Field: "temperature", Operator: "gt", Value: 30},
							{Field: "humidity", Operator: "gt", Value: 80},
						},
					},
					{
						Operator: "and",
						Items: []Condition{
							{Field: "status", Operator: "eq", Value: "critical"},
						},
					},
				},
			},
			data: map[string]interface{}{
				"temperature": 25,
				"humidity":    70,
				"status":      "critical",
			},
			want: true, // Second group matches
		},
		{
			name: "complex nested conditions",
			conditions: Conditions{
				Operator: "and",
				Items: []Condition{
					{Field: "active", Operator: "eq", Value: true},
				},
				Groups: []Conditions{
					{
						Operator: "or",
						Items: []Condition{
							{Field: "temperature", Operator: "gt", Value: 30},
							{Field: "humidity", Operator: "gt", Value: 80},
						},
					},
				},
			},
			data: map[string]interface{}{
				"active":      true,
				"temperature": 35,
				"humidity":    70,
			},
			want: true, // active=true AND (temp>30 OR humidity>80)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateConditions(&tt.conditions, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateConditions() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEvaluateCondition_EdgeCases tests edge cases and error conditions
func TestEvaluateCondition_EdgeCases(t *testing.T) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	tests := []struct {
		name      string
		condition Condition
		data      map[string]interface{}
		want      bool
	}{
		{
			name:      "missing field returns false",
			condition: Condition{Field: "missing", Operator: "eq", Value: "test"},
			data:      map[string]interface{}{"other": "value"},
			want:      false,
		},
		{
			name:      "numeric comparison with non-numeric",
			condition: Condition{Field: "value", Operator: "gt", Value: 10},
			data:      map[string]interface{}{"value": "not a number"},
			want:      false,
		},
		{
			name:      "unknown operator returns false",
			condition: Condition{Field: "value", Operator: "unknown", Value: "test"},
			data:      map[string]interface{}{"value": "test"},
			want:      false,
		},
		{
			name:      "empty string comparison",
			condition: Condition{Field: "value", Operator: "eq", Value: ""},
			data:      map[string]interface{}{"value": ""},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processor.evaluateCondition(&tt.condition, tt.data, timeCtx, subjectCtx, nil)
			if got != tt.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

// BenchmarkEvaluateCondition_Simple benchmarks single condition evaluation
func BenchmarkEvaluateCondition_Simple(b *testing.B) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	condition := Condition{Field: "temperature", Operator: "gt", Value: 25}
	data := map[string]interface{}{"temperature": 30}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.evaluateCondition(&condition, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkEvaluateCondition_Nested benchmarks nested field condition
func BenchmarkEvaluateCondition_Nested(b *testing.B) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	condition := Condition{Field: "user.profile.age", Operator: "gte", Value: 18}
	data := map[string]interface{}{
		"user": map[string]interface{}{
			"profile": map[string]interface{}{
				"age": 25,
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.evaluateCondition(&condition, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkEvaluateCondition_TimeField benchmarks time field condition
func BenchmarkEvaluateCondition_TimeField(b *testing.B) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	condition := Condition{Field: "@time.hour", Operator: "gte", Value: 9}
	data := map[string]interface{}{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.evaluateCondition(&condition, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkEvaluateConditions_SimpleAnd benchmarks AND condition group
func BenchmarkEvaluateConditions_SimpleAnd(b *testing.B) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	conditions := Conditions{
		Operator: "and",
		Items: []Condition{
			{Field: "temperature", Operator: "gt", Value: 20},
			{Field: "status", Operator: "eq", Value: "active"},
			{Field: "humidity", Operator: "lt", Value: 80},
		},
	}
	data := map[string]interface{}{
		"temperature": 25,
		"status":      "active",
		"humidity":    70,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.evaluateConditions(&conditions, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkEvaluateConditions_Complex benchmarks complex nested conditions
func BenchmarkEvaluateConditions_Complex(b *testing.B) {
	processor := newTestProcessor()
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature")

	conditions := Conditions{
		Operator: "and",
		Items: []Condition{
			{Field: "active", Operator: "eq", Value: true},
			{Field: "@time.hour", Operator: "gte", Value: 9},
		},
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
		"user": map[string]interface{}{
			"profile": map[string]interface{}{
				"tier": "standard",
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.evaluateConditions(&conditions, data, timeCtx, subjectCtx, nil)
	}
}

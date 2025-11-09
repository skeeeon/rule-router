// file: internal/rule/condition_resolver_test.go

package rule

import (
	"testing"

	"rule-router/internal/logger"
)

func TestIsTemplate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "simple message field",
			input:    "{temperature}",
			expected: true,
		},
		{
			name:     "system time field",
			input:    "{@time.hour}",
			expected: true,
		},
		{
			name:     "KV field",
			input:    "{@kv.config.sensor:max}",
			expected: true,
		},
		{
			name:     "nested message field",
			input:    "{sensor.reading.value}",
			expected: true,
		},
		{
			name:     "not a template - no braces",
			input:    "temperature",
			expected: false,
		},
		{
			name:     "not a template - literal number",
			input:    "30",
			expected: false,
		},
		{
			name:     "empty braces",
			input:    "{}",
			expected: true, // Contains braces, but ExtractVariable will return ""
		},
		{
			name:     "partial brace - opening only",
			input:    "{temperature",
			expected: false,
		},
		{
			name:     "partial brace - closing only",
			input:    "temperature}",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTemplate(tt.input) // ✅ Fixed: Uppercase
			if result != tt.expected {
				t.Errorf("IsTemplate(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractVariable(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple field",
			input:    "{temperature}",
			expected: "temperature",
		},
		{
			name:     "nested field",
			input:    "{sensor.reading.value}",
			expected: "sensor.reading.value",
		},
		{
			name:     "system time field",
			input:    "{@time.hour}",
			expected: "@time.hour",
		},
		{
			name:     "system subject field",
			input:    "{@subject.1}",
			expected: "@subject.1",
		},
		{
			name:     "KV field with colon",
			input:    "{@kv.config.sensor:max}",
			expected: "@kv.config.sensor:max",
		},
		{
			name:     "KV field with nested path",
			input:    "{@kv.customer_data.{customer_id}:profile.name}",
			expected: "@kv.customer_data.{customer_id}:profile.name",
		},
		{
			name:     "not a template - no braces",
			input:    "temperature",
			expected: "",
		},
		{
			name:     "empty braces",
			input:    "{}",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "{  }",
			expected: "",
		},
		{
			name:     "partial brace - opening only",
			input:    "{temperature",
			expected: "",
		},
		{
			name:     "partial brace - closing only",
			input:    "temperature}",
			expected: "",
		},
		{
			name:     "with surrounding whitespace",
			input:    "  {temperature}  ",
			expected: "temperature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractVariable(tt.input) // ✅ Fixed: Uppercase
			if result != tt.expected {
				t.Errorf("ExtractVariable(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolveConditionValue(t *testing.T) {
	log := logger.NewNopLogger() // ✅ Fixed: Use NewNopLogger()

	// Create a simple test context with known values
	// ✅ Fixed: Removed unused msgData, create context directly
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature.room1")

	context, err := NewEvaluationContext(
		[]byte(`{"temperature":25.5,"status":"active","count":42,"enabled":true,"sensor":{"id":"sensor-001","type":"temperature"}}`),
		map[string]string{"X-Device-ID": "device-123"},
		subjectCtx,
		nil,
		timeCtx,
		nil, // No KV context for these tests
		nil, // No signature verification
		log,
	)
	if err != nil {
		t.Fatalf("Failed to create context: %v", err)
	}

	tests := []struct {
		name        string
		input       interface{}
		expectValue interface{}
		expectError bool
	}{
		{
			name:        "literal number",
			input:       30,
			expectValue: 30,
			expectError: false,
		},
		{
			name:        "literal string",
			input:       "active",
			expectValue: "active",
			expectError: false,
		},
		{
			name:        "literal bool",
			input:       true,
			expectValue: true,
			expectError: false,
		},
		{
			name:        "template - message field",
			input:       "{temperature}",
			expectValue: 25.5,
			expectError: false,
		},
		{
			name:        "template - nested message field",
			input:       "{sensor.id}",
			expectValue: "sensor-001",
			expectError: false,
		},
		{
			name:        "template - system time field",
			input:       "{@time.hour}",
			expectValue: timeCtx.fields["@time.hour"],
			expectError: false,
		},
		{
			name:        "template - subject field",
			input:       "{@subject.1}",
			expectValue: "temperature",
			expectError: false,
		},
		{
			name:        "template - header field",
			input:       "{@header.X-Device-ID}",
			expectValue: "device-123",
			expectError: false,
		},
		{
			name:        "template - nonexistent field",
			input:       "{nonexistent}",
			expectValue: nil,
			expectError: true,
		},
		{
			name:        "malformed template - empty braces",
			input:       "{}",
			expectValue: "{}",
			expectError: false, // Treated as literal
		},
		{
			name:        "not a template - literal with text",
			input:       "some text",
			expectValue: "some text",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveConditionValue(tt.input, context)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none. Result: %v", result)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Compare values with type awareness
			if !compareTestValues(result, tt.expectValue) {
				t.Errorf("resolveConditionValue(%v) = %v (type %T), expected %v (type %T)",
					tt.input, result, result, tt.expectValue, tt.expectValue)
			}
		})
	}
}

func TestResolveConditionValue_TypePreservation(t *testing.T) {
	log := logger.NewNopLogger() // ✅ Fixed: Use NewNopLogger()

	// ✅ Fixed: Removed unused msgData, create context directly
	context, err := NewEvaluationContext(
		[]byte(`{"num_int":42,"num_float":3.14,"str":"hello","bool_true":true,"bool_false":false,"arr":[1,2,3],"obj":{"nested":"value"}}`),
		nil,
		nil,
		nil,
		NewSystemTimeProvider().GetCurrentContext(),
		nil,
		nil,
		log,
	)
	if err != nil {
		t.Fatalf("Failed to create context: %v", err)
	}

	tests := []struct {
		name         string
		input        string
		expectedType string
	}{
		{
			name:         "integer preserved",
			input:        "{num_int}",
			expectedType: "float64", // JSON unmarshals to float64
		},
		{
			name:         "float preserved",
			input:        "{num_float}",
			expectedType: "float64",
		},
		{
			name:         "string preserved",
			input:        "{str}",
			expectedType: "string",
		},
		{
			name:         "boolean true preserved",
			input:        "{bool_true}",
			expectedType: "bool",
		},
		{
			name:         "boolean false preserved",
			input:        "{bool_false}",
			expectedType: "bool",
		},
		{
			name:         "array preserved",
			input:        "{arr}",
			expectedType: "[]interface {}",
		},
		{
			name:         "object preserved",
			input:        "{obj}",
			expectedType: "map[string]interface {}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveConditionValue(tt.input, context)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			actualType := getTypeName(result)
			if actualType != tt.expectedType {
				t.Errorf("Type mismatch: got %s, expected %s", actualType, tt.expectedType)
			}
		})
	}
}

// Helper functions for tests

func compareTestValues(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// For numeric comparison, handle float64/int conversion
	switch va := a.(type) {
	case float64:
		switch vb := b.(type) {
		case float64:
			return va == vb
		case int:
			return va == float64(vb)
		}
	case int:
		switch vb := b.(type) {
		case int:
			return va == vb
		case float64:
			return float64(va) == vb
		}
	}

	// For other types, direct comparison
	return a == b
}

func getTypeName(v interface{}) string {
	if v == nil {
		return "nil"
	}
	return typeToString(v)
}

func typeToString(v interface{}) string {
	switch v.(type) {
	case int:
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case []interface{}:
		return "[]interface {}"
	case map[string]interface{}:
		return "map[string]interface {}"
	default:
		return "unknown"
	}
}

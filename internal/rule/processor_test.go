// file: internal/rule/processor_test.go

package rule

import (
	"strings"
	"testing"
	"time"

	"rule-router/internal/logger"
)

// Helper to create a template engine for testing.
func newTestTemplateEngine() *TemplateEngine {
	return NewTemplateEngine(logger.NewNopLogger())
}

// Helper to create a context for template tests.
func newTemplateTestContext(data map[string]interface{}, subject string, t time.Time) *EvaluationContext {
	timeProvider := NewMockTimeProvider(t)
	ctx, _ := NewEvaluationContext([]byte("{}"), nil, NewSubjectContext(subject), timeProvider.GetCurrentContext(), nil)
	ctx.Msg = data
	return ctx
}

// TestProcessor_Orchestration verifies the processor correctly calls evaluator and templater.
func TestProcessor_Orchestration(t *testing.T) {
	log := logger.NewNopLogger()
	processor := NewProcessor(log, nil, nil)

	rules := []Rule{
		{
			Subject: "test.subject",
			Conditions: &Conditions{
				Operator: "and",
				Items:    []Condition{{Field: "status", Operator: "eq", Value: "active"}},
			},
			Action: &Action{
				Subject: "out.subject.{device_id}",
				Payload: `{"id": "{device_id}"}`,
			},
		},
	}
	processor.LoadRules(rules)

	// Case 1: Condition matches
	payloadMatch := []byte(`{"status": "active", "device_id": "dev123"}`)
	actions, err := processor.ProcessWithSubject("test.subject", payloadMatch, nil)
	if err != nil {
		t.Fatalf("ProcessWithSubject returned an error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}
	if actions[0].Subject != "out.subject.dev123" {
		t.Errorf("Unexpected action subject: got %s, want out.subject.dev123", actions[0].Subject)
	}
	if actions[0].Payload != `{"id": "dev123"}` {
		t.Errorf("Unexpected action payload: got %s", actions[0].Payload)
	}

	// Case 2: Condition does not match
	payloadNoMatch := []byte(`{"status": "inactive", "device_id": "dev456"}`)
	actions, err = processor.ProcessWithSubject("test.subject", payloadNoMatch, nil)
	if err != nil {
		t.Fatalf("ProcessWithSubject returned an error: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("Expected 0 actions, got %d", len(actions))
	}
}

// TestTemplateEngine_BasicVariables tests simple message field substitution
func TestTemplateEngine_BasicVariables(t *testing.T) {
	engine := newTestTemplateEngine()
	tests := []struct {
		name     string
		template string
		data     map[string]interface{}
		want     string
	}{
		{
			name:     "single variable",
			template: "Temperature is {temperature}",
			data:     map[string]interface{}{"temperature": 25.5},
			want:     "Temperature is 25.5",
		},
		{
			name:     "missing variable returns empty string",
			template: "Value: {missing_field}",
			data:     map[string]interface{}{"temperature": 25},
			want:     "Value: ",
		},
		{
			name:     "nil value returns empty string",
			template: "Value: {null_field}",
			data:     map[string]interface{}{"null_field": nil},
			want:     "Value: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTemplateTestContext(tt.data, "test.subject", time.Now())
			got, err := engine.Execute(tt.template, context)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Execute() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTemplateEngine_NestedFields tests dot-notation field access
func TestTemplateEngine_NestedFields(t *testing.T) {
	engine := newTestTemplateEngine()
	template := "Email: {user.profile.email}"
	data := map[string]interface{}{
		"user": map[string]interface{}{
			"profile": map[string]interface{}{
				"email": "john@example.com",
			},
		},
	}
	want := "Email: john@example.com"

	context := newTemplateTestContext(data, "test.subject", time.Now())
	got, err := engine.Execute(template, context)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != want {
		t.Errorf("Execute() = %q, want %q", got, want)
	}
}

// TestTemplateEngine_SystemFunctions tests system function calls
func TestTemplateEngine_SystemFunctions(t *testing.T) {
	engine := newTestTemplateEngine()
	template := "{@uuid4()}"
	context := newTemplateTestContext(map[string]interface{}{}, "test.subject", time.Now())

	got, err := engine.Execute(template, context)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(got) != 36 { // Basic validation for UUID
		t.Errorf("Expected a UUID, got %q", got)
	}
}

// TestTemplateEngine_TimeFields tests time system fields
func TestTemplateEngine_TimeFields(t *testing.T) {
	engine := newTestTemplateEngine()
	fixedTime := time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC)
	template := "Hour: {@time.hour}"
	want := "Hour: 14"

	context := newTemplateTestContext(map[string]interface{}{}, "test.subject", fixedTime)
	got, err := engine.Execute(template, context)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != want {
		t.Errorf("Execute() = %q, want %q", got, want)
	}
}

// TestTemplateEngine_SubjectFields tests subject token access
func TestTemplateEngine_SubjectFields(t *testing.T) {
	engine := newTestTemplateEngine()
	template := "Location: {@subject.2}"
	want := "Location: room1"

	context := newTemplateTestContext(map[string]interface{}{}, "sensors.temperature.room1", time.Now())
	got, err := engine.Execute(template, context)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != want {
		t.Errorf("Execute() = %q, want %q", got, want)
	}
}

// TestTemplateEngine_ComplexTemplates tests realistic complex templates
func TestTemplateEngine_ComplexTemplates(t *testing.T) {
	engine := newTestTemplateEngine()
	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	template := `{ "device": "{device_id}", "type": "{@subject.1}", "hour": {@time.hour} }`
	data := map[string]interface{}{"device_id": "sensor001"}
	contains := []string{`"device": "sensor001"`, `"type": "temperature"`, `"hour": 14`}

	context := newTemplateTestContext(data, "sensors.temperature.room101", fixedTime)
	got, err := engine.Execute(template, context)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, substr := range contains {
		if !strings.Contains(got, substr) {
			t.Errorf("Execute() result doesn't contain %q\nGot: %s", substr, got)
		}
	}
}

// --- Benchmarks ---

func BenchmarkTemplateEngine_Simple(b *testing.B) {
	engine := newTestTemplateEngine()
	template := "Device {device_id} reports {temperature}°C"
	data := map[string]interface{}{"device_id": "sensor001", "temperature": 25.5}
	context := newTemplateTestContext(data, "sensors.temperature", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

func BenchmarkTemplateEngine_Complex(b *testing.B) {
	engine := newTestTemplateEngine()
	template := `{ "id": "{device_id}", "type": "{@subject.1}", "ts": "{@timestamp()}" }`
	data := map[string]interface{}{"device_id": "sensor001"}
	context := newTemplateTestContext(data, "sensors.temperature.room101", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

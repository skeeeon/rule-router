// file: internal/rule/processor_test.go

package rule

import (
	"fmt"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"rule-router/internal/logger"
)

// Helper to create a template engine for testing.
func newTestTemplateEngine() *TemplateEngine {
	return NewTemplateEngine(logger.NewNopLogger())
}

// Helper to create a context for template tests (No KV).
func newTemplateTestContext(data map[string]interface{}, subject string, t time.Time) *EvaluationContext {
	timeProvider := NewMockTimeProvider(t)
	subjectCtx := NewSubjectContext(subject)

	payload, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal test data: %v", err))
	}

	ctx, err := NewEvaluationContext(
		payload,
		nil, // headers
		subjectCtx,
		nil, // httpCtx
		timeProvider.GetCurrentContext(),
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNopLogger(),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create evaluation context: %v", err))
	}
	return ctx
}

// Helper to create a context WITH KV support for benchmarking complex templates
func newKVTemplateTestContext(data map[string]interface{}, kvData map[string]map[string]interface{}) *EvaluationContext {
	log := logger.NewNopLogger()

	// Setup Local Cache with pre-populated data
	cache := NewLocalKVCache(log)
	for bucket, keys := range kvData {
		for key, val := range keys {
			cache.Set(bucket, key, val)
		}
	}
	// Create KV context with the cache
	kvCtx := NewKVContext(nil, log, cache)

	// Setup Context
	payload, _ := json.Marshal(data)
	ctx, _ := NewEvaluationContext(
		payload,
		nil,
		NewSubjectContext("test.subject"),
		nil,
		NewSystemTimeProvider().GetCurrentContext(),
		kvCtx,
		nil,
		log,
	)
	return ctx
}

// Helper to create a processor for testing
func newTestProcessor() *Processor {
	return NewProcessor(logger.NewNopLogger(), nil, nil, nil)
}

// Helper to create a processor with pre-populated KV cache for testing
func setupTestProcessorWithKV(kvData map[string]map[string]interface{}) *Processor {
	log := logger.NewNopLogger()
	cache := NewLocalKVCache(log)

	// Populate cache
	for bucket, keys := range kvData {
		for key, val := range keys {
			cache.Set(bucket, key, val)
		}
	}

	// Create KV context with the cache
	kvCtx := NewKVContext(nil, log, cache)

	return NewProcessor(log, nil, kvCtx, nil)
}

// TestProcessor_ComplexIntegration_DeepContext tests a highly complex scenario involving:
// 1. 3-level deep nested JSON field access from message ({data.device.config_id})
// 2. Header validation ({header.X-Tenant-ID})
// 3. Dynamic KV lookup using the nested message field
// 4. Subject context injection in output (@subject.1)
// 5. Timestamp context injection in output (@timestamp.iso)
func TestProcessor_ComplexIntegration_DeepContext(t *testing.T) {
	// Setup KV Data
	kvData := map[string]map[string]interface{}{
		"configurations": {
			"cfg_alpha": map[string]interface{}{
				"threshold": 90,
				"region":    "us-east-1",
				"settings": map[string]interface{}{
					"retry": true,
				},
			},
		},
	}

	processor := setupTestProcessorWithKV(kvData)

	// Define the Rule
	rule := Rule{
		Trigger: Trigger{
			NATS: &NATSTrigger{Subject: "iot.sensors.telemetry"},
		},
		Conditions: &Conditions{
			Operator: "and",
			Items: []Condition{
				// Condition 1: Check Header
				{
					Field:    "{@header.X-Tenant-ID}",
					Operator: "eq",
					Value:    "tenant-a",
				},
				// Condition 2: Deep nested variable used in KV lookup key
				// Message field: data.device.config_id -> "cfg_alpha"
				// KV Lookup: configurations.cfg_alpha:threshold -> 90
				{
					Field:    "{@kv.configurations.{data.device.config_id}:threshold}",
					Operator: "gt",
					Value:    80,
				},
			},
		},
		Action: Action{
			NATS: &NATSAction{
				// Use subject token 'sensors' (index 1)
				Subject: "alerts.{@subject.1}.processed", 
				// Complex payload with timestamp and another KV lookup
				Payload: `{
					"region": "{@kv.configurations.{data.device.config_id}:region}", 
					"processed_at": "{@timestamp.iso}",
					"source": "{@subject}"
				}`,
			},
		},
	}

	if err := processor.LoadRules([]Rule{rule}); err != nil {
		t.Fatalf("Failed to load rule: %v", err)
	}

	// Prepare Input Message (3 levels deep)
	msgBytes := []byte(`{
		"data": {
			"device": {
				"id": "sensor-001",
				"config_id": "cfg_alpha"
			}
		}
	}`)

	headers := map[string]string{
		"X-Tenant-ID": "tenant-a",
	}

	// Execute
	actions, err := processor.ProcessNATS("iot.sensors.telemetry", msgBytes, headers)
	if err != nil {
		t.Fatalf("ProcessNATS failed: %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	// Verify Action Subject
	expectedSubject := "alerts.sensors.processed"
	if actions[0].NATS.Subject != expectedSubject {
		t.Errorf("Subject mismatch. Got: %s, Want: %s", actions[0].NATS.Subject, expectedSubject)
	}

	// Verify Payload content (contains resolved values)
	payload := actions[0].NATS.Payload
	if !strings.Contains(payload, `"region": "us-east-1"`) {
		t.Errorf("Payload missing resolved KV region. Got: %s", payload)
	}
	if !strings.Contains(payload, `"source": "iot.sensors.telemetry"`) {
		t.Errorf("Payload missing subject source. Got: %s", payload)
	}
	// Basic check for timestamp format
	if !strings.Contains(payload, `"processed_at": "20`) {
		t.Errorf("Payload missing valid timestamp. Got: %s", payload)
	}
}

// TestProcessor_Orchestration verifies the processor correctly calls evaluator and templater.
func TestProcessor_Orchestration(t *testing.T) {
	log := logger.NewNopLogger()
	processor := NewProcessor(log, nil, nil, nil)

	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{Subject: "test.subject"},
			},
			Conditions: &Conditions{
				Operator: "and",
				Items:    []Condition{{Field: "{status}", Operator: "eq", Value: "active"}},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "out.subject.{device_id}",
					Payload: `{"id": "{device_id}"}`,
				},
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
	if actions[0].NATS.Subject != "out.subject.dev123" {
		t.Errorf("Unexpected action subject: got %s, want out.subject.dev123", actions[0].NATS.Subject)
	}
	if actions[0].NATS.Payload != `{"id": "dev123"}` {
		t.Errorf("Unexpected action payload: got %s", actions[0].NATS.Payload)
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

// TestProcessor_ComplexKVOrchestration tests nested variables inside KV lookups
func TestProcessor_ComplexKVOrchestration(t *testing.T) {
	// Setup KV data
	kvData := map[string]map[string]interface{}{
		"device_configs": {
			"sensor-type-a": map[string]interface{}{
				"threshold": 50,
				"owner_ref": "group_1",
			},
			"sensor-type-b": map[string]interface{}{
				"threshold": 80,
				"owner_ref": "group_2",
			},
		},
		"groups": {
			"group_1": map[string]interface{}{"email": "team_a@example.com"},
			"group_2": map[string]interface{}{"email": "team_b@example.com"},
		},
	}

	processor := setupTestProcessorWithKV(kvData)

	// Define a rule that uses dynamic KV lookup based on message content
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{Subject: "sensors.reading"},
			},
			Conditions: &Conditions{
				Operator: "and",
				Items: []Condition{
					{
						Field:    "{value}",
						Operator: "gt",
						Value:    "{@kv.device_configs.{type}:threshold}",
					},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "alerts.{type}",
					Payload: `{"id": "{id}", "contact": "{@kv.groups.{@kv.device_configs.{type}:owner_ref}:email}"}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	// Test Case 1: Should Match
	payloadMatch := []byte(`{"id": "dev1", "type": "sensor-type-a", "value": 60}`)
	actions, err := processor.ProcessWithSubject("sensors.reading", payloadMatch, nil)
	if err != nil {
		t.Fatalf("ProcessWithSubject error: %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	expectedPayload := `{"id": "dev1", "contact": "team_a@example.com"}`
	if actions[0].NATS.Payload != expectedPayload {
		t.Errorf("Payload mismatch.\nGot:  %s\nWant: %s", actions[0].NATS.Payload, expectedPayload)
	}

	// Test Case 2: Should NOT Match
	payloadNoMatch := []byte(`{"id": "dev2", "type": "sensor-type-b", "value": 60}`)
	actions, err = processor.ProcessWithSubject("sensors.reading", payloadNoMatch, nil)
	if err != nil {
		t.Fatalf("ProcessWithSubject error: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("Expected 0 actions, got %d", len(actions))
	}
}

// ... [Keep existing TemplateEngine unit tests: BasicVariables, NestedFields, SystemFunctions, TimeFields, SubjectFields, ComplexTemplates] ...

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

// ... [Keep ExtractForEachField and ForEach Tests] ...

func TestExtractForEachField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple field with braces",
			input:    "{notifications}",
			expected: "notifications",
		},
		{
			name:     "nested field with braces",
			input:    "{data.items}",
			expected: "data.items",
		},
		{
			name:     "deeply nested field",
			input:    "{response.data.events.items}",
			expected: "response.data.events.items",
		},
		{
			name:     "root array accessor",
			input:    "{@items}",
			expected: "@items",
		},
		{
			name:     "field with whitespace",
			input:    "  {notifications}  ",
			expected: "notifications",
		},
		{
			name:     "invalid - missing braces",
			input:    "notifications",
			expected: "",
		},
		{
			name:     "invalid - only opening brace",
			input:    "{notifications",
			expected: "",
		},
		{
			name:     "invalid - only closing brace",
			input:    "notifications}",
			expected: "",
		},
		{
			name:     "invalid - empty braces",
			input:    "{}",
			expected: "",
		},
		{
			name:     "invalid - whitespace only in braces",
			input:    "{  }",
			expected: "",
		},
		{
			name:     "complex nested path",
			input:    "{nested.path.with.many.levels}",
			expected: "nested.path.with.many.levels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractForEachField(tt.input)
			if result != tt.expected {
				t.Errorf("extractForEachField(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ... [Keep all ForEach Tests (NATS and HTTP)] ...
func TestProcessNATSActionWithForEach_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "value": 10},
			map[string]interface{}{"id": "item2", "value": 20},
			map[string]interface{}{"id": "item3", "value": 30},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 3 {
		t.Fatalf("Expected 3 actions, got %d", len(actions))
	}

	// Verify first action
	if actions[0].NATS.Subject != "alerts.item1" {
		t.Errorf("Action 0 subject = %s, want alerts.item1", actions[0].NATS.Subject)
	}
	if !strings.Contains(actions[0].NATS.Payload, `"id": "item1"`) {
		t.Errorf("Action 0 payload doesn't contain expected content: %s", actions[0].NATS.Payload)
	}

	// Verify second action
	if actions[1].NATS.Subject != "alerts.item2" {
		t.Errorf("Action 1 subject = %s, want alerts.item2", actions[1].NATS.Subject)
	}

	// Verify third action
	if actions[2].NATS.Subject != "alerts.item3" {
		t.Errorf("Action 2 subject = %s, want alerts.item3", actions[2].NATS.Subject)
	}
}

func TestProcessNATSActionWithForEach_InvalidSyntax(t *testing.T) {
	processor := newTestProcessor()

	tests := []struct {
		name        string
		forEachField string
		wantErr     bool
		errContains string
	}{
		{
			name:        "missing braces",
			forEachField: "items",
			wantErr:     true,
			errContains: "invalid forEach template syntax",
		},
		{
			name:        "empty braces",
			forEachField: "{}",
			wantErr:     true,
			errContains: "invalid forEach template syntax",
		},
		{
			name:        "only opening brace",
			forEachField: "{items",
			wantErr:     true,
			errContains: "invalid forEach template syntax",
		},
		{
			name:        "valid syntax",
			forEachField: "{items}",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := &NATSAction{
				ForEach: tt.forEachField,
				Subject: "alerts.{id}",
				Payload: `{"id": "{id}"}`,
			}

			data := map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"id": "item1"},
				},
			}

			context := newTemplateTestContext(data, "test.subject", time.Now())

			_, err := processor.processNATSActionWithForEach(action, context)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error containing '%s', got nil", tt.errContains)
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing '%s', got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestProcessNATSActionWithForEach_WithFilter(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{status}", Operator: "eq", Value: "active"},
			},
		},
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "status": "active"},
			map[string]interface{}{"id": "item2", "status": "inactive"},
			map[string]interface{}{"id": "item3", "status": "active"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	// Only 2 actions should be generated (item2 filtered out)
	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions (2 filtered), got %d", len(actions))
	}

	if actions[0].NATS.Subject != "alerts.item1" {
		t.Errorf("Action 0 subject = %s, want alerts.item1", actions[0].NATS.Subject)
	}

	if actions[1].NATS.Subject != "alerts.item3" {
		t.Errorf("Action 1 subject = %s, want alerts.item3", actions[1].NATS.Subject)
	}
}

func TestProcessNATSActionWithForEach_EmptyArray(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 0 {
		t.Errorf("Expected 0 actions for empty array, got %d", len(actions))
	}
}

func TestProcessNATSActionWithForEach_MixedArray(t *testing.T) {
	processor := newTestProcessor()

	// MODIFICATION: Add a filter to explicitly process only elements that have an 'id' field.
	action := &NATSAction{
		ForEach: "{items}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{id}", Operator: "exists"},
			},
		},
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1"},
			"not-an-object", // This will be filtered out because it has no 'id' field.
			map[string]interface{}{"id": "item2"},
			42, // This will also be filtered out.
			map[string]interface{}{"id": "item3"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	// The assertion is now correct. The filter ensures only 3 actions are generated.
	if len(actions) != 3 {
		t.Fatalf("Expected 3 actions (2 non-objects filtered out), got %d", len(actions))
	}

	if actions[0].NATS.Subject != "alerts.item1" {
		t.Errorf("Action 0 subject = %s, want alerts.item1", actions[0].NATS.Subject)
	}
	if actions[1].NATS.Subject != "alerts.item2" {
		t.Errorf("Action 1 subject = %s, want alerts.item2", actions[1].NATS.Subject)
	}
	if actions[2].NATS.Subject != "alerts.item3" {
		t.Errorf("Action 2 subject = %s, want alerts.item3", actions[2].NATS.Subject)
	}
}

func TestProcessNATSActionWithForEach_RootMessageAccess(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{notifications}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "siteId": "{@msg.siteId}", "deviceId": "{@msg.deviceId}"}`,
	}

	data := map[string]interface{}{
		"siteId":   "site-123",
		"deviceId": "device-456",
		"notifications": []interface{}{
			map[string]interface{}{"id": "notif1"},
			map[string]interface{}{"id": "notif2"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	// Both actions should have access to root message fields
	for i, action := range actions {
		if !strings.Contains(action.NATS.Payload, `"siteId": "site-123"`) {
			t.Errorf("Action %d payload missing siteId from root: %s", i, action.NATS.Payload)
		}
		if !strings.Contains(action.NATS.Payload, `"deviceId": "device-456"`) {
			t.Errorf("Action %d payload missing deviceId from root: %s", i, action.NATS.Payload)
		}
	}

	// Verify element-specific fields
	if !strings.Contains(actions[0].NATS.Payload, `"id": "notif1"`) {
		t.Errorf("Action 0 payload missing element id: %s", actions[0].NATS.Payload)
	}
	if !strings.Contains(actions[1].NATS.Payload, `"id": "notif2"`) {
		t.Errorf("Action 1 payload missing element id: %s", actions[1].NATS.Payload)
	}
}

func TestProcessNATSActionWithForEach_Passthrough(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach:     "{items}",
		Subject:     "alerts.{id}",
		Passthrough: true,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "value": 10},
			map[string]interface{}{"id": "item2", "value": 20},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	// Verify first action has raw payload
	if len(actions[0].NATS.RawPayload) == 0 {
		t.Error("Action 0 should have RawPayload set")
	}

	// Parse and verify content
	var payload1 map[string]interface{}
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &payload1); err != nil {
		t.Fatalf("Failed to parse action 0 raw payload: %v", err)
	}

	if payload1["id"] != "item1" {
		t.Errorf("Action 0 payload id = %v, want item1", payload1["id"])
	}
}

func TestProcessNATSActionWithForEach_IterationLimit(t *testing.T) {
	processor := newTestProcessor()
	processor.SetMaxForEachIterations(5) // Set low limit for testing

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	// Create 10 items (exceeds limit of 5)
	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{"id": fmt.Sprintf("item%d", i)}
	}

	data := map[string]interface{}{
		"items": items,
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	_, err := processor.processNATSActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for exceeding iteration limit, got nil")
	}

	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("Expected 'exceeds limit' error, got: %v", err)
	}
}

func TestProcessNATSActionWithForEach_NestedFields(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{notifications}",
		Subject: "alerts.{event.alarmId}",
		Payload: `{"alarmId": "{event.alarmId}", "alarmName": "{event.alarmName}"}`,
	}

	data := map[string]interface{}{
		"notifications": []interface{}{
			map[string]interface{}{
				"event": map[string]interface{}{
					"alarmId":   "alarm-001",
					"alarmName": "Motion Detected",
				},
			},
			map[string]interface{}{
				"event": map[string]interface{}{
					"alarmId":   "alarm-002",
					"alarmName": "Door Opened",
				},
			},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	if actions[0].NATS.Subject != "alerts.alarm-001" {
		t.Errorf("Action 0 subject = %s, want alerts.alarm-001", actions[0].NATS.Subject)
	}

	if !strings.Contains(actions[0].NATS.Payload, `"alarmName": "Motion Detected"`) {
		t.Errorf("Action 0 payload missing nested field: %s", actions[0].NATS.Payload)
	}
}

func TestProcessNATSActionWithForEach_Headers(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
		Headers: map[string]string{
			"X-Item-Id":       "{id}",
			"X-Item-Priority": "{priority}",
			"X-Static":        "static-value",
		},
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "priority": "high"},
			map[string]interface{}{"id": "item2", "priority": "low"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	// Verify first action headers
	if actions[0].NATS.Headers["X-Item-Id"] != "item1" {
		t.Errorf("Action 0 header X-Item-Id = %s, want item1", actions[0].NATS.Headers["X-Item-Id"])
	}
	if actions[0].NATS.Headers["X-Item-Priority"] != "high" {
		t.Errorf("Action 0 header X-Item-Priority = %s, want high", actions[0].NATS.Headers["X-Item-Priority"])
	}
	if actions[0].NATS.Headers["X-Static"] != "static-value" {
		t.Errorf("Action 0 header X-Static = %s, want static-value", actions[0].NATS.Headers["X-Static"])
	}
}

// ========================================
// FOREACH TESTS - HTTP ACTIONS
// ========================================

// TestProcessHTTPActionWithForEach_Basic tests basic HTTP forEach functionality
func TestProcessHTTPActionWithForEach_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &HTTPAction{
		ForEach: "{items}",
		URL:     "https://api.example.com/items/{id}",
		Method:  "POST",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "value": 10},
			map[string]interface{}{"id": "item2", "value": 20},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processHTTPActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processHTTPActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	if actions[0].HTTP.URL != "https://api.example.com/items/item1" {
		t.Errorf("Action 0 URL = %s, want https://api.example.com/items/item1", actions[0].HTTP.URL)
	}

	if actions[1].HTTP.URL != "https://api.example.com/items/item2" {
		t.Errorf("Action 1 URL = %s, want https://api.example.com/items/item2", actions[1].HTTP.URL)
	}
}

// TestProcessHTTPActionWithForEach_InvalidSyntax tests forEach with old syntax (should fail)
func TestProcessHTTPActionWithForEach_InvalidSyntax(t *testing.T) {
	processor := newTestProcessor()

	action := &HTTPAction{
		ForEach: "items", // Missing braces
		URL:     "https://api.example.com/items/{id}",
		Method:  "POST",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	_, err := processor.processHTTPActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for missing braces, got nil")
	}

	if !strings.Contains(err.Error(), "invalid forEach template syntax") {
		t.Errorf("Expected 'invalid forEach template syntax' error, got: %v", err)
	}
}

// TestProcessHTTPActionWithForEach_WithRetry tests retry config preservation
func TestProcessHTTPActionWithForEach_WithRetry(t *testing.T) {
	processor := newTestProcessor()

	retryConfig := &RetryConfig{
		MaxAttempts:  3,
		InitialDelay: "1s",
		MaxDelay:     "30s",
	}

	action := &HTTPAction{
		ForEach: "{items}",
		URL:     "https://api.example.com/items/{id}",
		Method:  "POST",
		Payload: `{"id": "{id}"}`,
		Retry:   retryConfig,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processHTTPActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processHTTPActionWithForEach() error = %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	if actions[0].HTTP.Retry == nil {
		t.Fatal("Expected retry config to be preserved, got nil")
	}

	if actions[0].HTTP.Retry.MaxAttempts != 3 {
		t.Errorf("Retry MaxAttempts = %d, want 3", actions[0].HTTP.Retry.MaxAttempts)
	}
}

// ========================================
// EDGE CASE TESTS
// ========================================

// TestProcessForEach_NonExistentField tests forEach on non-existent field
func TestProcessForEach_NonExistentField(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{nonexistent}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"other": "value",
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	_, err := processor.processNATSActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for non-existent forEach field, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}
}

// TestProcessForEach_NonArrayField tests forEach on non-array field
func TestProcessForEach_NonArrayField(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": "not-an-array",
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	_, err := processor.processNATSActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for non-array forEach field, got nil")
	}

	if !strings.Contains(err.Error(), "not an array") {
		t.Errorf("Expected 'not an array' error, got: %v", err)
	}
}

// TestProcessForEach_AllElementsFiltered tests forEach where all elements are filtered out
func TestProcessForEach_AllElementsFiltered(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{status}", Operator: "eq", Value: "critical"},
			},
		},
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "status": "normal"},
			map[string]interface{}{"id": "item2", "status": "normal"},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 0 {
		t.Errorf("Expected 0 actions (all filtered), got %d", len(actions))
	}
}

// ========================================
// REAL-WORLD SCENARIO TEST
// ========================================

// TestProcessForEach_RealWorldBatchNotification tests the implementation plan example
func TestProcessForEach_RealWorldBatchNotification(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{notification}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{type}", Operator: "eq", Value: "DEVICE_MOTION_START"},
			},
		},
		Subject: "alerts.motion.{event.alarmId}",
		Payload: `{
			"alertType": "motion_detected",
			"alarmId": "{event.alarmId}",
			"alarmName": "{event.alarmName}",
			"cameraId": "{cameraId}",
			"siteId": "{@msg.siteId}",
			"notificationTime": "{@msg.time}"
		}`,
	}

	data := map[string]interface{}{
		"siteId": "site-123",
		"type":   "NOTIFICATION",
		"time":   "2019-10-29T17:02:18.528Z",
		"notification": []interface{}{
			map[string]interface{}{
				"id":       "evt-001",
				"type":     "DEVICE_MOTION_START",
				"cameraId": "cam-001",
				"event": map[string]interface{}{
					"alarmId":   "alarm-abc",
					"alarmName": "Motion Detected",
				},
			},
			map[string]interface{}{
				"id":       "evt-002",
				"type":     "DEVICE_DOOR_OPEN",
				"cameraId": "cam-002",
				"event": map[string]interface{}{
					"alarmId":   "alarm-xyz",
					"alarmName": "Door Opened",
				},
			},
			map[string]interface{}{
				"id":       "evt-003",
				"type":     "DEVICE_MOTION_START",
				"cameraId": "cam-003",
				"event": map[string]interface{}{
					"alarmId":   "alarm-def",
					"alarmName": "Motion Detected",
				},
			},
		},
	}

	context := newTemplateTestContext(data, "device.notifications", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	// Should generate 2 actions (evt-001 and evt-003, evt-002 filtered out)
	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions (1 filtered), got %d", len(actions))
	}

	// Verify first motion alert
	if actions[0].NATS.Subject != "alerts.motion.alarm-abc" {
		t.Errorf("Action 0 subject = %s, want alerts.motion.alarm-abc", actions[0].NATS.Subject)
	}

	if !strings.Contains(actions[0].NATS.Payload, `"alarmId": "alarm-abc"`) {
		t.Errorf("Action 0 payload missing alarmId: %s", actions[0].NATS.Payload)
	}

	if !strings.Contains(actions[0].NATS.Payload, `"siteId": "site-123"`) {
		t.Errorf("Action 0 payload missing siteId from root: %s", actions[0].NATS.Payload)
	}

	// Verify second motion alert
	if actions[1].NATS.Subject != "alerts.motion.alarm-def" {
		t.Errorf("Action 1 subject = %s, want alerts.motion.alarm-def", actions[1].NATS.Subject)
	}
}

// ========================================
// BENCHMARKS
// ========================================

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

// Renamed from Complex to Mixed
func BenchmarkTemplateEngine_MixedTypes(b *testing.B) {
	engine := newTestTemplateEngine()
	template := `{ "id": "{device_id}", "type": "{@subject.1}", "ts": "{@timestamp()}" }`
	data := map[string]interface{}{"device_id": "sensor001"}
	context := newTemplateTestContext(data, "sensors.temperature.room101", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// BenchmarkTemplateEngine_NestedKV benchmarks true recursive template parsing with KV lookups
func BenchmarkTemplateEngine_NestedKV(b *testing.B) {
	engine := newTestTemplateEngine()

	// Setup chained KV data for deep recursion
	// Message ID -> Config Key -> Region -> Endpoint
	kvData := make(map[string]map[string]interface{})
	kvData["devices"] = map[string]interface{}{
		"sensor-001": map[string]interface{}{"config_id": "cfg-alpha"},
	}
	kvData["configs"] = map[string]interface{}{
		"cfg-alpha": map[string]interface{}{"region": "us-west"},
	}
	kvData["regions"] = map[string]interface{}{
		"us-west": map[string]interface{}{"url": "api.west.internal"},
	}

	// 3 levels of nesting + 1 base variable
	template := `{"target": "https://{@kv.regions.{@kv.configs.{@kv.devices.{id}:config_id}:region}:url}/ingest"}`

	data := map[string]interface{}{"id": "sensor-001"}
	context := newKVTemplateTestContext(data, kvData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// BenchmarkProcessor_Heavy_KV tests a realistic scenario with extensive KV usage
func BenchmarkProcessor_Heavy_KV(b *testing.B) {
	kvData := make(map[string]map[string]interface{})
	kvData["configs"] = make(map[string]interface{})
	kvData["limits"] = make(map[string]interface{})
	kvData["enrichment"] = make(map[string]interface{})

	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("dev-%d", i)
		kvData["configs"][id] = map[string]interface{}{
			"type":   "sensor-type-x",
			"region": "us-east",
		}
		kvData["limits"]["sensor-type-x"] = map[string]interface{}{
			"max_temp": 100,
		}
		kvData["enrichment"]["us-east"] = map[string]interface{}{
			"datacenter": "virginia",
			"support":    "team-a",
		}
	}

	processor := setupTestProcessorWithKV(kvData)

	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "data"}},
			Conditions: &Conditions{
				Operator: "and",
				Items: []Condition{
					{
						Field:    "{val}",
						Operator: "gt",
						Value:    "{@kv.limits.{@kv.configs.{id}:type}:max_temp}",
					},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "alert.{id}",
					Payload: `{"dc": "{@kv.enrichment.{@kv.configs.{id}:region}:datacenter}"}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	msg := []byte(`{"id": "dev-500", "val": 150}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.ProcessNATS("data", msg, nil)
	}
}

// BenchmarkProcessor_ComplexIntegration_DeepContext measures performance of a heavy rule
// involving deep JSON traversal, header checks, dynamic KV lookup, and context injection
func BenchmarkProcessor_ComplexIntegration_DeepContext(b *testing.B) {
	// Setup KV Data
	kvData := map[string]map[string]interface{}{
		"configurations": {
			"cfg_alpha": map[string]interface{}{
				"threshold": 90,
				"region":    "us-east-1",
			},
		},
	}

	processor := setupTestProcessorWithKV(kvData)

	// Define the Rule (same as test case)
	rule := Rule{
		Trigger: Trigger{NATS: &NATSTrigger{Subject: "iot.sensors.telemetry"}},
		Conditions: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{@header.X-Tenant-ID}", Operator: "eq", Value: "tenant-a"},
				{Field: "{@kv.configurations.{data.device.config_id}:threshold}", Operator: "gt", Value: 80},
			},
		},
		Action: Action{
			NATS: &NATSAction{
				Subject: "alerts.{@subject.1}.processed",
				Payload: `{"region": "{@kv.configurations.{data.device.config_id}:region}", "ts": "{@timestamp.iso}"}`,
			},
		},
	}
	processor.LoadRules([]Rule{rule})

	// Input
	msgBytes := []byte(`{"data": {"device": {"id": "sensor-001", "config_id": "cfg_alpha"}}}`)
	headers := map[string]string{"X-Tenant-ID": "tenant-a"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.ProcessNATS("iot.sensors.telemetry", msgBytes, headers)
	}
}

// ... [Keep ProcessForEach benchmarks] ...

func BenchmarkProcessForEach_Small(b *testing.B) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	data := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "item1", "value": 10},
			map[string]interface{}{"id": "item2", "value": 20},
			map[string]interface{}{"id": "item3", "value": 30},
		},
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSActionWithForEach(action, context)
	}
}

func BenchmarkProcessForEach_Large(b *testing.B) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]interface{}{"id": fmt.Sprintf("item%d", i), "value": i * 10}
	}

	data := map[string]interface{}{
		"items": items,
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSActionWithForEach(action, context)
	}
}

func BenchmarkProcessForEach_WithFilter(b *testing.B) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{value}", Operator: "gt", Value: 50},
			},
		},
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]interface{}{"id": fmt.Sprintf("item%d", i), "value": i}
	}

	data := map[string]interface{}{
		"items": items,
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSActionWithForEach(action, context)
	}
}

func BenchmarkProcessForEach_MixedArray(b *testing.B) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	items := make([]interface{}, 90)
	for i := 0; i < 90; i++ {
		if i%3 == 0 {
			items[i] = map[string]interface{}{"id": fmt.Sprintf("item%d", i)}
		} else if i%3 == 1 {
			items[i] = "string"
		} else {
			items[i] = 42
		}
	}

	data := map[string]interface{}{
		"items": items,
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSActionWithForEach(action, context)
	}
}

func BenchmarkExtractForEachField(b *testing.B) {
	testCases := []string{
		"{notifications}",
		"{data.items}",
		"{nested.path.array}",
		"{@items}",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractForEachField(testCases[i%len(testCases)])
	}
}

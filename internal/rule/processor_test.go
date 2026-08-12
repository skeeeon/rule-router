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
	return NewTemplateEngine(logger.NewNop())
}

// Helper to create a context for template tests (No KV).
func newTemplateTestContext(data map[string]any, subject string, t time.Time) *EvaluationContext {
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
		timeProvider.CurrentContext(),
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNop(),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create evaluation context: %v", err))
	}
	return ctx
}

// Helper to create a context WITH KV support for benchmarking complex templates
func newKVTemplateTestContext(data map[string]any, kvData map[string]map[string]any) *EvaluationContext {
	log := logger.NewNop()

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
		NewSystemTimeProvider().CurrentContext(),
		kvCtx,
		nil,
		log,
	)
	return ctx
}

// Helper to create a processor for testing
// actionsOf flattens a Process* call to every action it evaluated, immediate
// and trailing-throttled alike. Most tests predate trailing mode and only care
// what a rule produced, not when it runs. Tests that do care about the split
// read Outcome.Immediate / Outcome.Deferred directly.
func actionsOf(o Outcome, err error) ([]*Action, error) {
	return o.All(), err
}

func newTestProcessor(opts ...Option) *Processor {
	return NewProcessor(logger.NewNop(), opts...)
}

// Helper to create a processor with pre-populated KV cache for testing
func setupTestProcessorWithKV(kvData map[string]map[string]any) *Processor {
	log := logger.NewNop()
	cache := NewLocalKVCache(log)

	// Populate cache
	for bucket, keys := range kvData {
		for key, val := range keys {
			cache.Set(bucket, key, val)
		}
	}

	// Create KV context with the cache
	kvCtx := NewKVContext(nil, log, cache)

	return NewProcessor(log, WithKVContext(kvCtx))
}

// TestProcessor_ComplexIntegration_DeepContext tests a highly complex scenario involving:
// 1. 3-level deep nested JSON field access from message ({data.device.config_id})
// 2. Header validation ({header.X-Tenant-ID})
// 3. Dynamic KV lookup using the nested message field
// 4. Subject context injection in output (@subject.1)
// 5. Timestamp context injection in output (@timestamp.iso)
func TestProcessor_ComplexIntegration_DeepContext(t *testing.T) {
	// Setup KV Data
	kvData := map[string]map[string]any{
		"configurations": {
			"cfg_alpha": map[string]any{
				"threshold": 90,
				"region":    "us-east-1",
				"settings": map[string]any{
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
	actions, err := actionsOf(processor.ProcessNATS("iot.sensors.telemetry", msgBytes, headers))
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
	log := logger.NewNop()
	processor := NewProcessor(log)

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
	actions, err := actionsOf(processor.ProcessWithSubject("test.subject", payloadMatch, nil))
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
	actions, err = actionsOf(processor.ProcessWithSubject("test.subject", payloadNoMatch, nil))
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
	kvData := map[string]map[string]any{
		"device_configs": {
			"sensor-type-a": map[string]any{
				"threshold": 50,
				"owner_ref": "group_1",
			},
			"sensor-type-b": map[string]any{
				"threshold": 80,
				"owner_ref": "group_2",
			},
		},
		"groups": {
			"group_1": map[string]any{"email": "team_a@example.com"},
			"group_2": map[string]any{"email": "team_b@example.com"},
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
	actions, err := actionsOf(processor.ProcessWithSubject("sensors.reading", payloadMatch, nil))
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
	actions, err = actionsOf(processor.ProcessWithSubject("sensors.reading", payloadNoMatch, nil))
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
		data     map[string]any
		want     string
	}{
		{
			name:     "single variable",
			template: "Temperature is {temperature}",
			data:     map[string]any{"temperature": 25.5},
			want:     "Temperature is 25.5",
		},
		{
			name:     "missing variable returns empty string",
			template: "Value: {missing_field}",
			data:     map[string]any{"temperature": 25},
			want:     "Value: ",
		},
		{
			name:     "nil value returns empty string",
			template: "Value: {null_field}",
			data:     map[string]any{"null_field": nil},
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
	data := map[string]any{
		"user": map[string]any{
			"profile": map[string]any{
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
	context := newTemplateTestContext(map[string]any{}, "test.subject", time.Now())

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

	context := newTemplateTestContext(map[string]any{}, "test.subject", fixedTime)
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

	context := newTemplateTestContext(map[string]any{}, "sensors.temperature.room1", time.Now())
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
	data := map[string]any{"device_id": "sensor001"}
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

// Note: ExtractVariable is tested in condition_resolver_test.go
// The forEach template extraction uses ExtractVariable from condition_resolver.go

// ... [ForEach Tests (NATS and HTTP)] ...
func TestProcessNATSActionWithForEach_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}", "value": {value}}`,
	}

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "value": 10},
			map[string]any{"id": "item2", "value": 20},
			map[string]any{"id": "item3", "value": 30},
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
		name         string
		forEachField string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "missing braces",
			forEachField: "items",
			wantErr:      true,
			errContains:  "invalid forEach template syntax",
		},
		{
			name:         "empty braces",
			forEachField: "{}",
			wantErr:      true,
			errContains:  "invalid forEach template syntax",
		},
		{
			name:         "only opening brace",
			forEachField: "{items",
			wantErr:      true,
			errContains:  "invalid forEach template syntax",
		},
		{
			name:         "valid syntax",
			forEachField: "{items}",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := &NATSAction{
				ForEach: tt.forEachField,
				Subject: "alerts.{id}",
				Payload: `{"id": "{id}"}`,
			}

			data := map[string]any{
				"items": []any{
					map[string]any{"id": "item1"},
				},
			}

			context := newTemplateTestContext(data, "test.subject", time.Now())

			_, err := processor.processNATSActionWithForEach(action, context)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errContains)
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "status": "active"},
			map[string]any{"id": "item2", "status": "inactive"},
			map[string]any{"id": "item3", "status": "active"},
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

	data := map[string]any{
		"items": []any{},
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1"},
			"not-an-object", // This will be filtered out because it has no 'id' field.
			map[string]any{"id": "item2"},
			42, // This will also be filtered out.
			map[string]any{"id": "item3"},
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

	data := map[string]any{
		"siteId":   "site-123",
		"deviceId": "device-456",
		"notifications": []any{
			map[string]any{"id": "notif1"},
			map[string]any{"id": "notif2"},
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "value": 10},
			map[string]any{"id": "item2", "value": 20},
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
	var payload1 map[string]any
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &payload1); err != nil {
		t.Fatalf("Failed to parse action 0 raw payload: %v", err)
	}

	if payload1["id"] != "item1" {
		t.Errorf("Action 0 payload id = %v, want item1", payload1["id"])
	}
}

func TestProcessNATSActionWithForEach_IterationLimit(t *testing.T) {
	processor := newTestProcessor(WithMaxForEachIterations(5)) // low limit for testing

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "alerts.{id}",
		Payload: `{"id": "{id}"}`,
	}

	// Create 10 items (exceeds limit of 5)
	items := make([]any, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]any{"id": fmt.Sprintf("item%d", i)}
	}

	data := map[string]any{
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

	data := map[string]any{
		"notifications": []any{
			map[string]any{
				"event": map[string]any{
					"alarmId":   "alarm-001",
					"alarmName": "Motion Detected",
				},
			},
			map[string]any{
				"event": map[string]any{
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "priority": "high"},
			map[string]any{"id": "item2", "priority": "low"},
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "value": 10},
			map[string]any{"id": "item2", "value": 20},
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1"},
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1"},
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

	data := map[string]any{
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

	data := map[string]any{
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "status": "normal"},
			map[string]any{"id": "item2", "status": "normal"},
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

	data := map[string]any{
		"siteId": "site-123",
		"type":   "NOTIFICATION",
		"time":   "2019-10-29T17:02:18.528Z",
		"notification": []any{
			map[string]any{
				"id":       "evt-001",
				"type":     "DEVICE_MOTION_START",
				"cameraId": "cam-001",
				"event": map[string]any{
					"alarmId":   "alarm-abc",
					"alarmName": "Motion Detected",
				},
			},
			map[string]any{
				"id":       "evt-002",
				"type":     "DEVICE_DOOR_OPEN",
				"cameraId": "cam-002",
				"event": map[string]any{
					"alarmId":   "alarm-xyz",
					"alarmName": "Door Opened",
				},
			},
			map[string]any{
				"id":       "evt-003",
				"type":     "DEVICE_MOTION_START",
				"cameraId": "cam-003",
				"event": map[string]any{
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
	data := map[string]any{"device_id": "sensor001", "temperature": 25.5}
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
	data := map[string]any{"device_id": "sensor001"}
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
	kvData := make(map[string]map[string]any)
	kvData["devices"] = map[string]any{
		"sensor-001": map[string]any{"config_id": "cfg-alpha"},
	}
	kvData["configs"] = map[string]any{
		"cfg-alpha": map[string]any{"region": "us-west"},
	}
	kvData["regions"] = map[string]any{
		"us-west": map[string]any{"url": "api.west.internal"},
	}

	// 3 levels of nesting + 1 base variable
	template := `{"target": "https://{@kv.regions.{@kv.configs.{@kv.devices.{id}:config_id}:region}:url}/ingest"}`

	data := map[string]any{"id": "sensor-001"}
	context := newKVTemplateTestContext(data, kvData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// BenchmarkProcessor_Heavy_KV tests a realistic scenario with extensive KV usage
func BenchmarkProcessor_Heavy_KV(b *testing.B) {
	kvData := make(map[string]map[string]any)
	kvData["configs"] = make(map[string]any)
	kvData["limits"] = make(map[string]any)
	kvData["enrichment"] = make(map[string]any)

	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("dev-%d", i)
		kvData["configs"][id] = map[string]any{
			"type":   "sensor-type-x",
			"region": "us-east",
		}
		kvData["limits"]["sensor-type-x"] = map[string]any{
			"max_temp": 100,
		}
		kvData["enrichment"]["us-east"] = map[string]any{
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
	kvData := map[string]map[string]any{
		"configurations": {
			"cfg_alpha": map[string]any{
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

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "item1", "value": 10},
			map[string]any{"id": "item2", "value": 20},
			map[string]any{"id": "item3", "value": 30},
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

	items := make([]any, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]any{"id": fmt.Sprintf("item%d", i), "value": i * 10}
	}

	data := map[string]any{
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

	items := make([]any, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]any{"id": fmt.Sprintf("item%d", i), "value": i}
	}

	data := map[string]any{
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

	items := make([]any, 90)
	for i := 0; i < 90; i++ {
		if i%3 == 0 {
			items[i] = map[string]any{"id": fmt.Sprintf("item%d", i)}
		} else if i%3 == 1 {
			items[i] = "string"
		} else {
			items[i] = 42
		}
	}

	data := map[string]any{
		"items": items,
	}

	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSActionWithForEach(action, context)
	}
}

func BenchmarkExtractVariable(b *testing.B) {
	testCases := []string{
		"{notifications}",
		"{data.items}",
		"{nested.path.array}",
		"{@items}",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractVariable(testCases[i%len(testCases)])
	}
}

// --- Deep Merge Tests ---

func TestDeepMerge_BasicOverwrite(t *testing.T) {
	base := map[string]any{"a": "old", "b": 1}
	overlay := map[string]any{"a": "new"}
	result := deepMerge(base, overlay)

	if result["a"] != "new" {
		t.Errorf("expected overlay to overwrite base: got %v", result["a"])
	}
	if result["b"] != 1 {
		t.Errorf("expected base key preserved: got %v", result["b"])
	}
}

func TestDeepMerge_NewKeys(t *testing.T) {
	base := map[string]any{"a": 1}
	overlay := map[string]any{"b": 2, "c": 3}
	result := deepMerge(base, overlay)

	if result["a"] != 1 {
		t.Errorf("base key missing: got %v", result["a"])
	}
	if result["b"] != 2 {
		t.Errorf("overlay key b missing: got %v", result["b"])
	}
	if result["c"] != 3 {
		t.Errorf("overlay key c missing: got %v", result["c"])
	}
}

func TestDeepMerge_NestedObjectsRecursed(t *testing.T) {
	base := map[string]any{
		"nested": map[string]any{
			"keep":      "yes",
			"overwrite": "old",
		},
	}
	overlay := map[string]any{
		"nested": map[string]any{
			"overwrite": "new",
			"added":     "extra",
		},
	}
	result := deepMerge(base, overlay)

	nested, ok := result["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested should be map, got %T", result["nested"])
	}
	if nested["keep"] != "yes" {
		t.Errorf("nested.keep should be preserved: got %v", nested["keep"])
	}
	if nested["overwrite"] != "new" {
		t.Errorf("nested.overwrite should be updated: got %v", nested["overwrite"])
	}
	if nested["added"] != "extra" {
		t.Errorf("nested.added should be added: got %v", nested["added"])
	}
}

func TestDeepMerge_ArraysReplacedWholesale(t *testing.T) {
	base := map[string]any{"arr": []any{1, 2, 3}}
	overlay := map[string]any{"arr": []any{4, 5}}
	result := deepMerge(base, overlay)

	arr, ok := result["arr"].([]any)
	if !ok {
		t.Fatalf("arr should be slice, got %T", result["arr"])
	}
	if len(arr) != 2 || arr[0] != 4 || arr[1] != 5 {
		t.Errorf("overlay array should replace base: got %v", arr)
	}
}

func TestDeepMerge_EmptyOverlay(t *testing.T) {
	base := map[string]any{"a": 1, "b": 2}
	overlay := map[string]any{}
	result := deepMerge(base, overlay)

	if len(result) != 2 || result["a"] != 1 || result["b"] != 2 {
		t.Errorf("empty overlay should return copy of base: got %v", result)
	}
}

func TestDeepMerge_EmptyBase(t *testing.T) {
	base := map[string]any{}
	overlay := map[string]any{"a": 1}
	result := deepMerge(base, overlay)

	if len(result) != 1 || result["a"] != 1 {
		t.Errorf("empty base should return overlay: got %v", result)
	}
}

func TestDeepMerge_BaseNotMutated(t *testing.T) {
	base := map[string]any{"a": "original"}
	overlay := map[string]any{"a": "changed", "b": "new"}
	deepMerge(base, overlay)

	if base["a"] != "original" {
		t.Errorf("base was mutated: a = %v", base["a"])
	}
	if _, exists := base["b"]; exists {
		t.Error("base was mutated: key b should not exist")
	}
}

// --- Merge Benchmarks ---

func BenchmarkNATSAction_Templated(b *testing.B) {
	processor := newTestProcessor()
	action := &NATSAction{
		Subject: "output.{device_id}",
		Payload: `{"device_id": "{device_id}", "reading": {reading}, "status": "{status}"}`,
	}
	data := map[string]any{
		"device_id": "sensor-001",
		"reading":   98.6,
		"status":    "active",
		"extra1":    "preserved1",
		"extra2":    "preserved2",
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSAction(action, context)
	}
}

func BenchmarkNATSAction_Passthrough(b *testing.B) {
	processor := newTestProcessor()
	action := &NATSAction{
		Subject:     "output.passthrough",
		Passthrough: true,
	}
	data := map[string]any{
		"device_id": "sensor-001",
		"reading":   98.6,
		"status":    "active",
		"extra1":    "preserved1",
		"extra2":    "preserved2",
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSAction(action, context)
	}
}

func BenchmarkNATSAction_Merge(b *testing.B) {
	processor := newTestProcessor()
	action := &NATSAction{
		Subject: "output.{device_id}",
		Merge:   true,
		Payload: `{"processed": true, "tier": "premium"}`,
	}
	data := map[string]any{
		"device_id": "sensor-001",
		"reading":   98.6,
		"status":    "active",
		"extra1":    "preserved1",
		"extra2":    "preserved2",
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSAction(action, context)
	}
}

func BenchmarkNATSAction_Merge_LargePayload(b *testing.B) {
	processor := newTestProcessor()
	action := &NATSAction{
		Subject: "output.{id}",
		Merge:   true,
		Payload: `{"enriched": true}`,
	}

	// Build a message with 50 fields to simulate a wide schema
	data := map[string]any{"id": "msg-1"}
	for i := 0; i < 50; i++ {
		data[fmt.Sprintf("field_%d", i)] = fmt.Sprintf("value_%d", i)
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processNATSAction(action, context)
	}
}

// --- NATS Merge Action Tests ---

func TestProcessNATSAction_Merge_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		Subject: "output.merged",
		Merge:   true,
		Payload: `{"added_field": "new_value"}`,
	}

	data := map[string]any{
		"existing": "preserved",
		"count":    42,
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSAction(action, context)
	if err != nil {
		t.Fatalf("processNATSAction() error = %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	result := actions[0].NATS
	if len(result.RawPayload) == 0 {
		t.Fatal("Merge should produce RawPayload")
	}

	var merged map[string]any
	if err := json.Unmarshal(result.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse merged payload: %v", err)
	}

	if merged["existing"] != "preserved" {
		t.Errorf("Original field not preserved: got %v", merged["existing"])
	}
	if merged["added_field"] != "new_value" {
		t.Errorf("Overlay field not added: got %v", merged["added_field"])
	}
	// count comes back as float64 from JSON round-trip
	if merged["count"] != float64(42) {
		t.Errorf("Original numeric field not preserved: got %v", merged["count"])
	}
}

func TestProcessNATSAction_Merge_OverwritesExistingField(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		Subject: "output.merged",
		Merge:   true,
		Payload: `{"status": "enriched"}`,
	}

	data := map[string]any{
		"status": "raw",
		"id":     "msg-1",
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSAction(action, context)
	if err != nil {
		t.Fatalf("processNATSAction() error = %v", err)
	}

	var merged map[string]any
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if merged["status"] != "enriched" {
		t.Errorf("Overlay should overwrite: got %v", merged["status"])
	}
	if merged["id"] != "msg-1" {
		t.Errorf("Non-overlapping field should be preserved: got %v", merged["id"])
	}
}

func TestProcessNATSAction_Merge_WithTemplateVariables(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		Subject: "output.merged",
		Merge:   true,
		Payload: `{"device_name": "{name}", "source_subject": "{@subject}"}`,
	}

	data := map[string]any{
		"name":    "sensor-1",
		"reading": 98.6,
	}
	context := newTemplateTestContext(data, "sensors.temperature.room1", time.Now())

	actions, err := processor.processNATSAction(action, context)
	if err != nil {
		t.Fatalf("processNATSAction() error = %v", err)
	}

	var merged map[string]any
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if merged["device_name"] != "sensor-1" {
		t.Errorf("Template variable not resolved: got %v", merged["device_name"])
	}
	if merged["source_subject"] != "sensors.temperature.room1" {
		t.Errorf("System variable not resolved: got %v", merged["source_subject"])
	}
	if merged["reading"] != 98.6 {
		t.Errorf("Original field not preserved: got %v", merged["reading"])
	}
}

func TestProcessNATSAction_Merge_InvalidOverlayJSON(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		Subject: "output.merged",
		Merge:   true,
		Payload: `not valid json`,
	}

	data := map[string]any{"key": "value"}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	_, err := processor.processNATSAction(action, context)
	if err == nil {
		t.Fatal("Expected error for invalid overlay JSON")
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("Error should mention merge: got %v", err)
	}
}

func TestProcessNATSAction_Merge_NestedObject(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		Subject: "output.merged",
		Merge:   true,
		Payload: `{"metadata": {"processed": true, "tier": "premium"}}`,
	}

	data := map[string]any{
		"id": "order-1",
		"metadata": map[string]any{
			"source":  "api",
			"version": "2.0",
		},
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSAction(action, context)
	if err != nil {
		t.Fatalf("processNATSAction() error = %v", err)
	}

	var merged map[string]any
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	metadata, ok := merged["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata should be object, got %T", merged["metadata"])
	}

	// Original nested fields preserved
	if metadata["source"] != "api" {
		t.Errorf("Nested original field not preserved: source = %v", metadata["source"])
	}
	if metadata["version"] != "2.0" {
		t.Errorf("Nested original field not preserved: version = %v", metadata["version"])
	}
	// Overlay nested fields added
	if metadata["processed"] != true {
		t.Errorf("Nested overlay field not added: processed = %v", metadata["processed"])
	}
	if metadata["tier"] != "premium" {
		t.Errorf("Nested overlay field not added: tier = %v", metadata["tier"])
	}
}

// TestProcessHTTPAction_PublishResponse_SubjectTemplating verifies that the
// publishResponse.subject is templated against the trigger context (no
// response-side templating).
func TestProcessHTTPAction_PublishResponse_SubjectTemplating(t *testing.T) {
	processor := newTestProcessor()

	action := &HTTPAction{
		URL:    "https://api.example.com/devices/{deviceId}",
		Method: "GET",
		PublishResponse: &PublishResponseSpec{
			Subject: "poll.devices.{deviceId}.status",
		},
	}

	data := map[string]any{"deviceId": "abc123"}
	ctx := newTemplateTestContext(data, "trigger.poll", time.Now())

	actions, err := processor.processHTTPAction(action, ctx)
	if err != nil {
		t.Fatalf("processHTTPAction() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	got := actions[0].HTTP
	if got.PublishResponse == nil {
		t.Fatal("expected PublishResponse to be set on result")
	}
	if got.PublishResponse.Subject != "poll.devices.abc123.status" {
		t.Errorf("subject = %q, want %q", got.PublishResponse.Subject, "poll.devices.abc123.status")
	}
	if got.URL != "https://api.example.com/devices/abc123" {
		t.Errorf("URL not templated: %q", got.URL)
	}
}

// --- HTTP Merge Action Tests ---

func TestProcessHTTPAction_Merge_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &HTTPAction{
		URL:     "https://api.example.com/enrich",
		Method:  "POST",
		Merge:   true,
		Payload: `{"enriched": true}`,
	}

	data := map[string]any{
		"id":   "msg-1",
		"data": "original",
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processHTTPAction(action, context)
	if err != nil {
		t.Fatalf("processHTTPAction() error = %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	result := actions[0].HTTP
	if len(result.RawPayload) == 0 {
		t.Fatal("Merge should produce RawPayload")
	}

	var merged map[string]any
	if err := json.Unmarshal(result.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse merged payload: %v", err)
	}

	if merged["id"] != "msg-1" {
		t.Errorf("Original field not preserved: got %v", merged["id"])
	}
	if merged["enriched"] != true {
		t.Errorf("Overlay field not added: got %v", merged["enriched"])
	}
}

// --- ForEach + Merge Tests ---

func TestProcessNATSActionWithForEach_Merge_Basic(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "enriched.{id}",
		Merge:   true,
		Payload: `{"processed": true}`,
	}

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "a", "value": 10},
			map[string]any{"id": "b", "value": 20},
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

	for i, act := range actions {
		if len(act.NATS.RawPayload) == 0 {
			t.Fatalf("Action %d should have RawPayload", i)
		}

		var merged map[string]any
		if err := json.Unmarshal(act.NATS.RawPayload, &merged); err != nil {
			t.Fatalf("Action %d: failed to parse: %v", i, err)
		}

		// Element fields preserved
		if merged["value"] == nil {
			t.Errorf("Action %d: element field 'value' not preserved", i)
		}
		// Overlay field added
		if merged["processed"] != true {
			t.Errorf("Action %d: overlay field 'processed' not added", i)
		}
	}

	// Verify subjects resolved from element context
	if actions[0].NATS.Subject != "enriched.a" {
		t.Errorf("Action 0 subject = %s, want enriched.a", actions[0].NATS.Subject)
	}
	if actions[1].NATS.Subject != "enriched.b" {
		t.Errorf("Action 1 subject = %s, want enriched.b", actions[1].NATS.Subject)
	}
}

func TestProcessNATSActionWithForEach_Merge_ElementIsBase(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "output.{id}",
		Merge:   true,
		Payload: `{"rootField": "{@msg.globalKey}"}`,
	}

	data := map[string]any{
		"globalKey": "global-value",
		"items": []any{
			map[string]any{"id": "x", "localField": "local-value"},
		},
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("Expected 1 action, got %d", len(actions))
	}

	var merged map[string]any
	if err := json.Unmarshal(actions[0].NATS.RawPayload, &merged); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// Element field (base) should be preserved
	if merged["localField"] != "local-value" {
		t.Errorf("Element field not preserved: got %v", merged["localField"])
	}
	if merged["id"] != "x" {
		t.Errorf("Element id not preserved: got %v", merged["id"])
	}
	// Overlay should include root message access via @msg
	if merged["rootField"] != "global-value" {
		t.Errorf("Root message field not resolved: got %v", merged["rootField"])
	}
	// globalKey should NOT be in merged (it's not in the element or overlay)
	if _, exists := merged["globalKey"]; exists {
		t.Error("Root-level globalKey should not leak into element-based merge")
	}
}

func TestProcessNATSActionWithForEach_Merge_InvalidOverlay(t *testing.T) {
	processor := newTestProcessor()

	action := &NATSAction{
		ForEach: "{items}",
		Subject: "output.{id}",
		Merge:   true,
		Payload: `not valid json {id}`,
	}

	data := map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
	}
	context := newTemplateTestContext(data, "test.subject", time.Now())

	// forEach with invalid overlay should not return a top-level error,
	// but should track failures per element and return 0 successful actions
	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("forEach should not return top-level error: %v", err)
	}

	if len(actions) != 0 {
		t.Errorf("Expected 0 successful actions for invalid overlay, got %d", len(actions))
	}
}

// ========================================
// DEBOUNCE / THROTTLE TESTS
// ========================================

func TestProcessor_TriggerThrottle_NATS(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.temp",
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "alerts.temp",
					Payload: `{"alert": true}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{"temperature": 30}`)

	// First message should produce actions
	actions1, err := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions1) == 0 {
		t.Fatal("first message should produce actions")
	}

	// Second message within window should be suppressed
	actions2, err := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions2) != 0 {
		t.Fatalf("second message within window should be suppressed, got %d actions", len(actions2))
	}
}

func TestProcessor_TriggerThrottle_HTTP(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				HTTP: &HTTPTrigger{
					Path:     "/webhooks/github",
					Method:   "POST",
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "github.events",
					Payload: `{"event": true}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{"action": "push"}`)

	actions1, err := actionsOf(processor.ProcessHTTP("/webhooks/github", "POST", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions1) == 0 {
		t.Fatal("first message should produce actions")
	}

	actions2, err := actionsOf(processor.ProcessHTTP("/webhooks/github", "POST", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions2) != 0 {
		t.Fatalf("second message within window should be suppressed, got %d actions", len(actions2))
	}
}

func TestProcessor_ActionThrottle(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{Subject: "sensors.temp"},
			},
			Conditions: &Conditions{
				Operator: "and",
				Items: []Condition{
					{Field: "{temperature}", Operator: "gt", Value: 25},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "alerts.temp",
					Payload:  `{"alert": true}`,
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{"temperature": 30}`)

	// First message passes conditions and action throttle
	actions1, err := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions1) == 0 {
		t.Fatal("first message should produce actions")
	}

	// Second message passes conditions but action throttle suppresses
	actions2, err := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions2) != 0 {
		t.Fatalf("action throttle should suppress second message, got %d actions", len(actions2))
	}
}

func TestProcessor_Throttle_DefaultKey_PerSubject(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.>",
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "alerts",
					Payload: `{}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{}`)

	// First message on subject A
	actions1, _ := actionsOf(processor.ProcessNATS("sensors.room1", payload, nil))
	if len(actions1) == 0 {
		t.Fatal("first message on room1 should produce actions")
	}

	// First message on subject B (different default key)
	actions2, _ := actionsOf(processor.ProcessNATS("sensors.room2", payload, nil))
	if len(actions2) == 0 {
		t.Fatal("first message on room2 should produce actions (different key)")
	}

	// Second message on subject A (suppressed)
	actions3, _ := actionsOf(processor.ProcessNATS("sensors.room1", payload, nil))
	if len(actions3) != 0 {
		t.Fatal("second message on room1 should be suppressed")
	}
}

func TestProcessor_Throttle_TemplateKey(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.temp",
					Throttle: &ThrottleConfig{Window: "1s", Key: "{sensor_id}"},
				},
			},
			Action: Action{
				NATS: &NATSAction{
					Subject: "alerts.temp",
					Payload: `{}`,
				},
			},
		},
	}
	processor.LoadRules(rules)

	// Sensor A
	actions1, _ := actionsOf(processor.ProcessNATS("sensors.temp", []byte(`{"sensor_id": "A"}`), nil))
	if len(actions1) == 0 {
		t.Fatal("first message from sensor A should produce actions")
	}

	// Sensor B on same subject (different template key)
	actions2, _ := actionsOf(processor.ProcessNATS("sensors.temp", []byte(`{"sensor_id": "B"}`), nil))
	if len(actions2) == 0 {
		t.Fatal("first message from sensor B should produce actions")
	}

	// Sensor A again (suppressed)
	actions3, _ := actionsOf(processor.ProcessNATS("sensors.temp", []byte(`{"sensor_id": "A"}`), nil))
	if len(actions3) != 0 {
		t.Fatal("second message from sensor A should be suppressed")
	}
}

func TestProcessor_Throttle_IndependentRules(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.temp",
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
			Action: Action{
				NATS: &NATSAction{Subject: "alerts.ruleA", Payload: `{}`},
			},
		},
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.temp",
					Throttle: &ThrottleConfig{Window: "1s"},
				},
			},
			Action: Action{
				NATS: &NATSAction{Subject: "alerts.ruleB", Payload: `{}`},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{}`)

	// First message should fire both rules
	actions1, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if len(actions1) != 2 {
		t.Fatalf("expected 2 actions (one per rule), got %d", len(actions1))
	}

	// Second message should suppress both
	actions2, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if len(actions2) != 0 {
		t.Fatalf("expected 0 actions (both suppressed), got %d", len(actions2))
	}
}

func TestProcessor_Throttle_NoThrottleUnaffected(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{Subject: "sensors.temp"},
			},
			Action: Action{
				NATS: &NATSAction{Subject: "alerts.temp", Payload: `{}`},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{}`)

	// Both messages should produce actions (no throttle)
	actions1, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	actions2, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))

	if len(actions1) == 0 || len(actions2) == 0 {
		t.Fatal("rules without throttle should always produce actions")
	}
}

func TestProcessor_Throttle_WindowExpiry(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{
				NATS: &NATSTrigger{
					Subject:  "sensors.temp",
					Throttle: &ThrottleConfig{Window: "50ms"},
				},
			},
			Action: Action{
				NATS: &NATSAction{Subject: "alerts.temp", Payload: `{}`},
			},
		},
	}
	processor.LoadRules(rules)

	payload := []byte(`{}`)

	actions1, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if len(actions1) == 0 {
		t.Fatal("first message should produce actions")
	}

	time.Sleep(60 * time.Millisecond)

	actions2, _ := actionsOf(processor.ProcessNATS("sensors.temp", payload, nil))
	if len(actions2) == 0 {
		t.Fatal("message after window expiry should produce actions")
	}
}

// --- Trailing-mode throttle tests ---

// TestProcessor_TrailingThrottle_TagsWithoutSuppressing verifies trailing mode
// never drops an action inside the Processor: every match is evaluated and
// returned carrying a defer spec, and the executing layer does the coalescing.
func TestProcessor_TrailingThrottle_TagsWithoutSuppressing(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.setpoint"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "hvac.setpoint",
					Payload:  `{"v": {value}}`,
					Throttle: &ThrottleConfig{Window: "5s", Mode: ThrottleTrailing},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	var keys []string
	for i := 0; i < 3; i++ {
		out, err := processor.ProcessNATS("sensors.setpoint", []byte(`{"value": 21}`), nil)
		if err != nil {
			t.Fatalf("process %d failed: %v", i, err)
		}
		if len(out.Immediate) != 0 {
			t.Errorf("process %d: a trailing action must not be immediate, got %d", i, len(out.Immediate))
		}
		if len(out.Deferred) != 1 {
			t.Fatalf("process %d: trailing throttle must not suppress, expected 1 deferred batch, got %d",
				i, len(out.Deferred))
		}
		if out.Deferred[0].Window != 5*time.Second {
			t.Errorf("process %d: expected a 5s window, got %v", i, out.Deferred[0].Window)
		}
		keys = append(keys, out.Deferred[0].Key)
	}

	// Same rule and same resolved key across messages, so all three land in the
	// same coalescing window.
	if keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("expected a stable defer key across messages, got %q / %q / %q", keys[0], keys[1], keys[2])
	}
}

// TestProcessor_TrailingThrottle_KeySeparatesGroups verifies a templated key
// puts distinct values in distinct coalescing windows.
func TestProcessor_TrailingThrottle_KeySeparatesGroups(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.setpoint"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "hvac.setpoint",
					Payload:  `{}`,
					Throttle: &ThrottleConfig{Window: "5s", Key: "{room}", Mode: ThrottleTrailing},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	kitchen, _ := processor.ProcessNATS("sensors.setpoint", []byte(`{"room": "kitchen"}`), nil)
	attic, _ := processor.ProcessNATS("sensors.setpoint", []byte(`{"room": "attic"}`), nil)

	if len(kitchen.Deferred) != 1 || len(attic.Deferred) != 1 {
		t.Fatalf("expected one deferred batch each, got %d and %d", len(kitchen.Deferred), len(attic.Deferred))
	}
	if kitchen.Deferred[0].Key == attic.Deferred[0].Key {
		t.Errorf("expected different defer keys per room, both were %q", kitchen.Deferred[0].Key)
	}
}

// TestProcessor_TrailingThrottle_ForEachSharesOneKey verifies a fan-out is
// tagged as a single batch, so the coalescer replaces the batch as a unit
// instead of collapsing N actions into the last element.
func TestProcessor_TrailingThrottle_ForEachSharesOneKey(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "orders.batch"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "orders.item",
					ForEach:  "{items}",
					Payload:  `{"sku": "{sku}"}`,
					Throttle: &ThrottleConfig{Window: "5s", Mode: ThrottleTrailing},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	out, err := processor.ProcessNATS("orders.batch",
		[]byte(`{"items": [{"sku": "a"}, {"sku": "b"}, {"sku": "c"}]}`), nil)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// One batch, not three — a trailing window replaces the fan-out as a unit.
	if len(out.Deferred) != 1 {
		t.Fatalf("expected the fan-out to form 1 deferred batch, got %d", len(out.Deferred))
	}
	if len(out.Deferred[0].Actions) != 3 {
		t.Fatalf("expected all 3 actions in the batch, got %d", len(out.Deferred[0].Actions))
	}

	// Defer is also stamped per action so inspection surfaces can show it.
	for i, a := range out.Deferred[0].Actions {
		if a.Defer == nil {
			t.Fatalf("action %d missing defer spec", i)
		}
		if a.Defer.Key != out.Deferred[0].Key {
			t.Errorf("action %d has key %q, expected the batch key %q", i, a.Defer.Key, out.Deferred[0].Key)
		}
	}
}

// TestProcessor_LeadingThrottle_LeavesNoDeferSpec verifies leading mode still
// gates inline and never produces deferred work.
func TestProcessor_LeadingThrottle_LeavesNoDeferSpec(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.temp"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "alerts.temp",
					Payload:  `{}`,
					Throttle: &ThrottleConfig{Window: "5s"},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	first, _ := processor.ProcessNATS("sensors.temp", []byte(`{}`), nil)
	if len(first.Immediate) != 1 {
		t.Fatalf("expected the first message to fire immediately, got %d actions", len(first.Immediate))
	}
	if len(first.Deferred) != 0 {
		t.Errorf("leading mode must not produce deferred batches, got %d", len(first.Deferred))
	}
	if first.Immediate[0].Defer != nil {
		t.Error("leading mode must not tag actions for deferral")
	}

	second, _ := processor.ProcessNATS("sensors.temp", []byte(`{}`), nil)
	if !second.Empty() {
		t.Errorf("expected the second message to be suppressed, got %d immediate / %d deferred",
			len(second.Immediate), len(second.Deferred))
	}
}

// TestProcessor_ActionThrottle_ForEachGatesWholeBatch pins the documented
// semantics of an action throttle on a forEach action: the gate runs BEFORE
// expansion, so the whole fan-out passes or is suppressed together. It is a
// rate limit on "did this rule fire", not on individual emitted elements.
func TestProcessor_ActionThrottle_ForEachGatesWholeBatch(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "orders.batch"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "orders.item",
					ForEach:  "{items}",
					Payload:  `{"sku": "{sku}"}`,
					Throttle: &ThrottleConfig{Window: "5s"},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	payload := []byte(`{"items": [{"sku": "a"}, {"sku": "b"}, {"sku": "c"}]}`)

	first, _ := actionsOf(processor.ProcessNATS("orders.batch", payload, nil))
	if len(first) != 3 {
		t.Fatalf("expected the whole fan-out to pass the open window, got %d actions", len(first))
	}

	second, _ := actionsOf(processor.ProcessNATS("orders.batch", payload, nil))
	if len(second) != 0 {
		t.Errorf("expected the whole fan-out to be suppressed inside the window, got %d actions", len(second))
	}
}

// TestProcessor_ActionThrottle_ForEachKeyUsesTriggerContext pins the known
// limitation: because the gate precedes expansion, a key naming an array-element
// field is not per-element — it resolves against the trigger context and comes
// back empty, so every message shares one window. Documented in
// docs/01-core-concepts.md; this test exists so the behaviour cannot drift
// silently into something users might depend on.
func TestProcessor_ActionThrottle_ForEachKeyUsesTriggerContext(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "orders.batch"}},
			Action: Action{
				NATS: &NATSAction{
					Subject: "orders.item",
					ForEach: "{items}",
					Payload: `{"sku": "{sku}"}`,
					// "sku" only exists on array elements, never at the root.
					Throttle: &ThrottleConfig{Window: "5s", Key: "{sku}"},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	first, _ := actionsOf(processor.ProcessNATS("orders.batch", []byte(`{"items": [{"sku": "a"}]}`), nil))
	if len(first) != 1 {
		t.Fatalf("expected the first batch to fire, got %d actions", len(first))
	}

	// Entirely different element values — yet the same (empty) resolved key, so
	// this is still suppressed. This is the limitation, asserted deliberately.
	second, _ := actionsOf(processor.ProcessNATS("orders.batch", []byte(`{"items": [{"sku": "z"}]}`), nil))
	if len(second) != 0 {
		t.Errorf("expected suppression: a forEach throttle key resolves against the trigger context, "+
			"so distinct element values do not get distinct windows; got %d actions", len(second))
	}
}

// TestProcessor_MixedRules_SplitsOutcome verifies that when one message matches
// both a plain rule and a trailing-throttled one, the two land in different
// Outcome fields — the split callers depend on to not publish a held action.
func TestProcessor_MixedRules_SplitsOutcome(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.setpoint"}},
			Action:  Action{NATS: &NATSAction{Subject: "audit.setpoint", Payload: `{}`}},
		},
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.setpoint"}},
			Action: Action{
				NATS: &NATSAction{
					Subject:  "hvac.setpoint",
					Payload:  `{}`,
					Throttle: &ThrottleConfig{Window: "5s", Mode: ThrottleTrailing},
				},
			},
		},
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("failed to load rules: %v", err)
	}

	out, err := processor.ProcessNATS("sensors.setpoint", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if len(out.Immediate) != 1 {
		t.Fatalf("expected 1 immediate action, got %d", len(out.Immediate))
	}
	if out.Immediate[0].NATS.Subject != "audit.setpoint" {
		t.Errorf("wrong action ran immediately: %s", out.Immediate[0].NATS.Subject)
	}
	if len(out.Deferred) != 1 {
		t.Fatalf("expected 1 deferred batch, got %d", len(out.Deferred))
	}
	if out.Deferred[0].Actions[0].NATS.Subject != "hvac.setpoint" {
		t.Errorf("wrong action deferred: %s", out.Deferred[0].Actions[0].NATS.Subject)
	}

	// All() is the inspection view: everything, in evaluation order.
	if all := out.All(); len(all) != 2 {
		t.Errorf("expected All() to return both actions, got %d", len(all))
	}
	if out.Empty() {
		t.Error("Empty() must be false when either field is populated")
	}
}

func TestOutcome_EmptyAndAll(t *testing.T) {
	var zero Outcome
	if !zero.Empty() {
		t.Error("a zero Outcome should be empty")
	}
	if len(zero.All()) != 0 {
		t.Error("a zero Outcome should flatten to nothing")
	}

	deferredOnly := Outcome{Deferred: []DeferredBatch{{
		Key:     "k",
		Window:  time.Second,
		Actions: []*Action{{NATS: &NATSAction{Subject: "a"}}},
	}}}
	if deferredOnly.Empty() {
		t.Error("an Outcome holding only deferred work is not empty")
	}
	if len(deferredOnly.All()) != 1 {
		t.Error("All() must include deferred actions")
	}
}

// --- KV-Sourced forEach Tests ---

func TestProcessNATSForEach_KVSourcedArray(t *testing.T) {
	kvData := map[string]map[string]any{
		"config": {
			"devices": []any{
				map[string]any{"id": "dev-1", "name": "Front Door"},
				map[string]any{"id": "dev-2", "name": "Back Door"},
				map[string]any{"id": "dev-3", "name": "Garage"},
			},
		},
	}
	processor := setupTestProcessorWithKV(kvData)

	action := &NATSAction{
		ForEach: "{@kv.config.devices}",
		Subject: "commands.{id}",
		Payload: `{"device": "{id}", "name": "{name}", "command": "unlock"}`,
	}

	// Message payload doesn't need to contain the array — it comes from KV
	data := map[string]any{"source": "scheduler"}
	context := newKVTemplateTestContext(data, kvData)

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 3 {
		t.Fatalf("Expected 3 actions, got %d", len(actions))
	}

	if actions[0].NATS.Subject != "commands.dev-1" {
		t.Errorf("Action 0 subject = %s, want commands.dev-1", actions[0].NATS.Subject)
	}
	if !strings.Contains(actions[0].NATS.Payload, `"name": "Front Door"`) {
		t.Errorf("Action 0 payload missing expected content: %s", actions[0].NATS.Payload)
	}

	if actions[1].NATS.Subject != "commands.dev-2" {
		t.Errorf("Action 1 subject = %s, want commands.dev-2", actions[1].NATS.Subject)
	}

	if actions[2].NATS.Subject != "commands.dev-3" {
		t.Errorf("Action 2 subject = %s, want commands.dev-3", actions[2].NATS.Subject)
	}
}

func TestProcessScheduleForEach_KVSourcedArray(t *testing.T) {
	kvData := map[string]map[string]any{
		"config": {
			"doors": []any{
				map[string]any{"id": "front", "zone": "main"},
				map[string]any{"id": "back", "zone": "service"},
			},
		},
	}
	processor := setupTestProcessorWithKV(kvData)

	rule := Rule{
		Trigger: Trigger{
			Schedule: &ScheduleTrigger{Cron: "0 8 * * 1-5"},
		},
		Action: Action{
			NATS: &NATSAction{
				ForEach: "{@kv.config.doors}",
				Subject: "access.door.{id}.command",
				Payload: `{"command": "unlock", "zone": "{zone}", "source": "rule-scheduler"}`,
			},
		},
	}

	processor.LoadRules([]Rule{rule})

	actions, err := actionsOf(processor.ProcessSchedule(processor.scheduleRules[0]))
	if err != nil {
		t.Fatalf("ProcessSchedule() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	if actions[0].NATS.Subject != "access.door.front.command" {
		t.Errorf("Action 0 subject = %s, want access.door.front.command", actions[0].NATS.Subject)
	}
	if !strings.Contains(actions[0].NATS.Payload, `"zone": "main"`) {
		t.Errorf("Action 0 payload missing zone: %s", actions[0].NATS.Payload)
	}

	if actions[1].NATS.Subject != "access.door.back.command" {
		t.Errorf("Action 1 subject = %s, want access.door.back.command", actions[1].NATS.Subject)
	}
	if !strings.Contains(actions[1].NATS.Payload, `"zone": "service"`) {
		t.Errorf("Action 1 payload missing zone: %s", actions[1].NATS.Payload)
	}
}

func TestProcessForEach_KVSourcedArray_NotFound(t *testing.T) {
	// Empty KV — no data
	processor := setupTestProcessorWithKV(map[string]map[string]any{})

	action := &NATSAction{
		ForEach: "{@kv.config.missing_key}",
		Subject: "test.{id}",
		Payload: `{"id": "{id}"}`,
	}

	data := map[string]any{}
	kvData := map[string]map[string]any{}
	context := newKVTemplateTestContext(data, kvData)

	_, err := processor.processNATSActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for missing KV key, got nil")
	}
	if !strings.Contains(err.Error(), "forEach system field not found") {
		t.Errorf("Expected 'forEach system field not found' error, got: %v", err)
	}
}

func TestProcessForEach_KVSourcedArray_NotArray(t *testing.T) {
	kvData := map[string]map[string]any{
		"config": {
			"settings": map[string]any{"key": "value"}, // object, not array
		},
	}
	processor := setupTestProcessorWithKV(kvData)

	action := &NATSAction{
		ForEach: "{@kv.config.settings}",
		Subject: "test.{key}",
		Payload: `{"key": "{key}"}`,
	}

	context := newKVTemplateTestContext(map[string]any{}, kvData)

	_, err := processor.processNATSActionWithForEach(action, context)
	if err == nil {
		t.Fatal("Expected error for non-array KV value, got nil")
	}
	if !strings.Contains(err.Error(), "forEach field is not an array") {
		t.Errorf("Expected 'not an array' error, got: %v", err)
	}
}

func TestProcessForEach_KVSourcedArray_WithFilter(t *testing.T) {
	kvData := map[string]map[string]any{
		"config": {
			"doors": []any{
				map[string]any{"id": "front", "enabled": true},
				map[string]any{"id": "back", "enabled": false},
				map[string]any{"id": "garage", "enabled": true},
			},
		},
	}
	processor := setupTestProcessorWithKV(kvData)

	action := &NATSAction{
		ForEach: "{@kv.config.doors}",
		Filter: &Conditions{
			Operator: "and",
			Items: []Condition{
				{Field: "{enabled}", Operator: "eq", Value: true},
			},
		},
		Subject: "access.{id}.unlock",
		Payload: `{"door": "{id}"}`,
	}

	context := newKVTemplateTestContext(map[string]any{}, kvData)

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions (back filtered out), got %d", len(actions))
	}

	if actions[0].NATS.Subject != "access.front.unlock" {
		t.Errorf("Action 0 subject = %s, want access.front.unlock", actions[0].NATS.Subject)
	}
	if actions[1].NATS.Subject != "access.garage.unlock" {
		t.Errorf("Action 1 subject = %s, want access.garage.unlock", actions[1].NATS.Subject)
	}
}

func TestProcessForEach_KVSourcedArray_WithJsonPath(t *testing.T) {
	kvData := map[string]map[string]any{
		"config": {
			"building": map[string]any{
				"name": "HQ",
				"doors": []any{
					map[string]any{"id": "front"},
					map[string]any{"id": "back"},
				},
			},
		},
	}
	processor := setupTestProcessorWithKV(kvData)

	// Use KV JSON path to reach nested array: @kv.config.building:doors
	action := &NATSAction{
		ForEach: "{@kv.config.building:doors}",
		Subject: "access.{id}.command",
		Payload: `{"door": "{id}", "command": "unlock"}`,
	}

	context := newKVTemplateTestContext(map[string]any{}, kvData)

	actions, err := processor.processNATSActionWithForEach(action, context)
	if err != nil {
		t.Fatalf("processNATSActionWithForEach() error = %v", err)
	}

	if len(actions) != 2 {
		t.Fatalf("Expected 2 actions, got %d", len(actions))
	}

	if actions[0].NATS.Subject != "access.front.command" {
		t.Errorf("Action 0 subject = %s, want access.front.command", actions[0].NATS.Subject)
	}
	if actions[1].NATS.Subject != "access.back.command" {
		t.Errorf("Action 1 subject = %s, want access.back.command", actions[1].NATS.Subject)
	}
}

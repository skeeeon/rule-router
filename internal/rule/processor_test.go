package rule

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"rule-router/internal/logger"
)

// newMockLogger creates a no-op logger for testing that discards all log output
func newMockLogger() *logger.Logger {
	// Use zap's no-op logger which safely discards all logs
	// This is much better than a nil logger or mock implementation
	zapLogger := zap.NewNop()
	return &logger.Logger{Logger: zapLogger}
}

// TestProcessTemplate_JSONStructures tests that JSON braces don't interfere with variable substitution
func TestProcessTemplate_JSONStructures(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	tests := []struct {
		name     string
		template string
		data     map[string]interface{}
		want     string
	}{
		{
			name:     "variable in JSON object",
			template: `{"device": "{device_id}", "temp": {temperature}}`,
			data:     map[string]interface{}{"device_id": "sensor001", "temperature": 25.5},
			want:     `{"device": "sensor001", "temp": 25.5}`,
		},
		{
			name:     "multiple variables in JSON",
			template: `{"id": "{device_id}", "value": {reading}, "status": "{status}"}`,
			data:     map[string]interface{}{"device_id": "sensor001", "reading": 32, "status": "active"},
			want:     `{"id": "sensor001", "value": 32, "status": "active"}`,
		},
		{
			name:     "nested JSON with variables",
			template: `{"device": {"id": "{id}", "type": "{type}"}, "data": {value}}`,
			data:     map[string]interface{}{"id": "sensor001", "type": "temperature", "value": 25},
			want:     `{"device": {"id": "sensor001", "type": "temperature"}, "data": 25}`,
		},
	}

	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, tt.data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("processTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProcessTemplate_BasicVariables tests simple message field substitution
func TestProcessTemplate_BasicVariables(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

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
			name:     "multiple variables",
			template: "Device {device_id} reports {temperature}°C",
			data:     map[string]interface{}{"device_id": "sensor001", "temperature": 32},
			want:     "Device sensor001 reports 32°C",
		},
		{
			name:     "variable in JSON with quotes",
			template: `{"device": "{device_id}", "temp": {temperature}}`,
			data:     map[string]interface{}{"device_id": "sensor001", "temperature": 25.5},
			want:     `{"device": "sensor001", "temp": 25.5}`,
		},
		{
			name:     "missing variable returns empty string",
			template: "Value: {missing_field}",
			data:     map[string]interface{}{"temperature": 25},
			want:     "Value: ",
		},
		{
			name:     "no variables in template",
			template: "Static text only",
			data:     map[string]interface{}{"temperature": 25},
			want:     "Static text only",
		},
		{
			name:     "empty template",
			template: "",
			data:     map[string]interface{}{"temperature": 25},
			want:     "",
		},
		{
			name:     "boolean value",
			template: "Active: {active}",
			data:     map[string]interface{}{"active": true},
			want:     "Active: true",
		},
		{
			name:     "string value",
			template: "Status: {status}",
			data:     map[string]interface{}{"status": "online"},
			want:     "Status: online",
		},
		{
			name:     "nil value returns empty string",
			template: "Value: {null_field}",
			data:     map[string]interface{}{"null_field": nil},
			want:     "Value: ",
		},
	}

	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, tt.data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("processTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProcessTemplate_NestedFields tests dot-notation field access
func TestProcessTemplate_NestedFields(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	tests := []struct {
		name     string
		template string
		data     map[string]interface{}
		want     string
	}{
		{
			name:     "two level nesting",
			template: "User: {user.name}",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "John Doe",
				},
			},
			want: "User: John Doe",
		},
		{
			name:     "three level nesting",
			template: "Email: {user.profile.email}",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"profile": map[string]interface{}{
						"email": "john@example.com",
					},
				},
			},
			want: "Email: john@example.com",
		},
		{
			name:     "multiple nested fields",
			template: "{device.location.building} - {device.location.room}",
			data: map[string]interface{}{
				"device": map[string]interface{}{
					"location": map[string]interface{}{
						"building": "Main",
						"room":     "101",
					},
				},
			},
			want: "Main - 101",
		},
		{
			name:     "missing nested field returns empty",
			template: "Value: {device.missing.field}",
			data: map[string]interface{}{
				"device": map[string]interface{}{
					"id": "sensor001",
				},
			},
			want: "Value: ",
		},
		{
			name:     "nested numeric value",
			template: "Threshold: {config.thresholds.max}",
			data: map[string]interface{}{
				"config": map[string]interface{}{
					"thresholds": map[string]interface{}{
						"max": 100,
					},
				},
			},
			want: "Threshold: 100",
		},
	}

	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, tt.data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("processTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProcessTemplate_SystemFunctions tests system function calls
func TestProcessTemplate_SystemFunctions(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	tests := []struct {
		name         string
		template     string
		validateFunc func(string) bool
		description  string
	}{
		{
			name:     "uuid4 generates valid UUID",
			template: "{@uuid4()}",
			validateFunc: func(s string) bool {
				// Basic UUID v4 format check: xxxxxxxx-xxxx-4xxx-xxxx-xxxxxxxxxxxx
				return len(s) == 36 && s[14] == '4' && strings.Count(s, "-") == 4
			},
			description: "should generate valid UUID v4",
		},
		{
			name:     "uuid7 generates valid UUID",
			template: "{@uuid7()}",
			validateFunc: func(s string) bool {
				// Basic UUID v7 format check: similar structure to v4
				return len(s) == 36 && strings.Count(s, "-") == 4
			},
			description: "should generate valid UUID v7",
		},
		{
			name:     "timestamp generates ISO8601",
			template: "{@timestamp()}",
			validateFunc: func(s string) bool {
				// Check RFC3339 format: 2024-01-15T14:30:00Z
				_, err := time.Parse(time.RFC3339, s)
				return err == nil
			},
			description: "should generate valid RFC3339 timestamp",
		},
		{
			name:     "multiple functions in template",
			template: `{"id": "{@uuid7()}", "timestamp": "{@timestamp()}"}`,
			validateFunc: func(s string) bool {
				return strings.Contains(s, `"id":`) && strings.Contains(s, `"timestamp":`)
			},
			description: "should process multiple functions",
		},
	}

	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")
	data := map[string]interface{}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if !tt.validateFunc(got) {
				t.Errorf("processTemplate() = %q, %s", got, tt.description)
			}
		})
	}
}

// TestProcessTemplate_TimeFields tests time system fields
func TestProcessTemplate_TimeFields(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	// Use fixed time for predictable tests
	fixedTime := time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC) // Friday
	timeProvider := NewMockTimeProvider(fixedTime)
	timeCtx := timeProvider.GetCurrentContext()

	tests := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "time hour",
			template: "Hour: {@time.hour}",
			want:     "Hour: 14",
		},
		{
			name:     "time minute",
			template: "Minute: {@time.minute}",
			want:     "Minute: 30",
		},
		{
			name:     "day name",
			template: "Day: {@day.name}",
			want:     "Day: friday",
		},
		{
			name:     "day number",
			template: "Day#: {@day.number}",
			want:     "Day#: 5",
		},
		{
			name:     "date year",
			template: "Year: {@date.year}",
			want:     "Year: 2024",
		},
		{
			name:     "date month",
			template: "Month: {@date.month}",
			want:     "Month: 3",
		},
		{
			name:     "date day",
			template: "Day: {@date.day}",
			want:     "Day: 15",
		},
		{
			name:     "date iso",
			template: "Date: {@date.iso}",
			want:     "Date: 2024-03-15",
		},
		{
			name:     "multiple time fields",
			template: "{@date.iso} {@time.hour}:{@time.minute}",
			want:     "2024-03-15 14:30",
		},
	}

	subjectCtx := NewSubjectContext("test.subject")
	data := map[string]interface{}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("processTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProcessTemplate_SubjectFields tests subject token access
func TestProcessTemplate_SubjectFields(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	tests := []struct {
		name     string
		subject  string
		template string
		want     string
	}{
		{
			name:     "full subject",
			subject:  "sensors.temperature.room1",
			template: "Subject: {@subject}",
			want:     "Subject: sensors.temperature.room1",
		},
		{
			name:     "first token",
			subject:  "sensors.temperature.room1",
			template: "Type: {@subject.0}",
			want:     "Type: sensors",
		},
		{
			name:     "second token",
			subject:  "sensors.temperature.room1",
			template: "Metric: {@subject.1}",
			want:     "Metric: temperature",
		},
		{
			name:     "third token",
			subject:  "sensors.temperature.room1",
			template: "Location: {@subject.2}",
			want:     "Location: room1",
		},
		{
			name:     "first alias",
			subject:  "sensors.temperature.room1",
			template: "First: {@subject.first}",
			want:     "First: sensors",
		},
		{
			name:     "last alias",
			subject:  "sensors.temperature.room1",
			template: "Last: {@subject.last}",
			want:     "Last: room1",
		},
		{
			name:     "token count",
			subject:  "sensors.temperature.room1",
			template: "Tokens: {@subject.count}",
			want:     "Tokens: 3",
		},
		{
			name:     "multiple subject fields",
			subject:  "building.floor3.room101",
			template: "{@subject.0}/{@subject.1}/{@subject.2}",
			want:     "building/floor3/room101",
		},
		{
			name:     "out of bounds returns empty",
			subject:  "sensors.temperature",
			template: "Token: {@subject.5}",
			want:     "Token: ",
		},
	}

	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	data := map[string]interface{}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subjectCtx := NewSubjectContext(tt.subject)
			got, err := processor.processTemplate(tt.template, data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("processTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProcessTemplate_ComplexTemplates tests realistic complex templates
func TestProcessTemplate_ComplexTemplates(t *testing.T) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	timeProvider := NewMockTimeProvider(fixedTime)
	timeCtx := timeProvider.GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature.room101")

	tests := []struct {
		name     string
		template string
		data     map[string]interface{}
		contains []string // Strings that should be in result
	}{
		{
			name: "complex JSON alert",
			template: `{
				"alert": "Temperature exceeded",
				"device": {
					"id": "{device_id}",
					"type": "{@subject.1}",
					"location": "{location}"
				},
				"reading": {
					"value": {temperature},
					"threshold": {threshold}
				},
				"timestamp": "{@timestamp()}",
				"alert_id": "{@uuid7()}"
			}`,
			data: map[string]interface{}{
				"device_id":   "sensor001",
				"location":    "room101",
				"temperature": 35.5,
				"threshold":   30,
			},
			contains: []string{
				`"id": "sensor001"`,
				`"type": "temperature"`,
				`"location": "room101"`,
				`"value": 35.5`,
				`"threshold": 30`,
			},
		},
		{
			name: "mixed variable types",
			template: "Device {@subject.0}.{device_id} at {location} reported {temperature}°C at {@time.hour}:{@time.minute}",
			data: map[string]interface{}{
				"device_id":   "sensor001",
				"location":    "room101",
				"temperature": 25.5,
			},
			contains: []string{
				"Device sensors.sensor001",
				"at room101",
				"reported 25.5°C",
				"at 14:30",
			},
		},
		{
			name: "nested fields with system fields",
			template: `{
				"user": "{user.profile.name}",
				"timestamp": "{@timestamp()}",
				"day": "{@day.name}",
				"building": "{device.location.building}"
			}`,
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"profile": map[string]interface{}{
						"name": "John Doe",
					},
				},
				"device": map[string]interface{}{
					"location": map[string]interface{}{
						"building": "Main",
					},
				},
			},
			contains: []string{
				`"user": "John Doe"`,
				`"day": "friday"`,
				`"building": "Main"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := processor.processTemplate(tt.template, tt.data, timeCtx, subjectCtx, nil)
			if err != nil {
				t.Fatalf("processTemplate() error = %v", err)
			}
			for _, substr := range tt.contains {
				if !strings.Contains(got, substr) {
					t.Errorf("processTemplate() result doesn't contain %q\nGot: %s", substr, got)
				}
			}
		})
	}
}

// BenchmarkProcessTemplate_Simple benchmarks simple variable substitution
func BenchmarkProcessTemplate_Simple(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := "Device {device_id} reports {temperature}°C"
	data := map[string]interface{}{
		"device_id":   "sensor001",
		"temperature": 25.5,
	}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_Nested benchmarks nested field access
func BenchmarkProcessTemplate_Nested(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := "User {user.profile.name} from {user.location.city}"
	data := map[string]interface{}{
		"user": map[string]interface{}{
			"profile": map[string]interface{}{
				"name": "John Doe",
			},
			"location": map[string]interface{}{
				"city": "Seattle",
			},
		},
	}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_SystemFields benchmarks system field access
func BenchmarkProcessTemplate_SystemFields(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := "Time: {@time.hour}:{@time.minute} on {@day.name}"
	data := map[string]interface{}{}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_SystemFunctions benchmarks system function calls
func BenchmarkProcessTemplate_SystemFunctions(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := `{"id": "{@uuid7()}", "timestamp": "{@timestamp()}"}`
	data := map[string]interface{}{}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_Complex benchmarks realistic complex template
func BenchmarkProcessTemplate_Complex(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := `{
		"alert": "Temperature exceeded",
		"device": {
			"id": {device_id},
			"type": "{@subject.1}",
			"location": {device.location.building}
		},
		"reading": {
			"value": {temperature},
			"timestamp": "{@timestamp()}"
		},
		"metadata": {
			"alert_id": "{@uuid7()}",
			"hour": {@time.hour},
			"day": "{@day.name}"
		}
	}`

	data := map[string]interface{}{
		"device_id":   "sensor001",
		"temperature": 35.5,
		"device": map[string]interface{}{
			"location": map[string]interface{}{
				"building": "Main",
			},
		},
	}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("sensors.temperature.room101")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_NoVariables benchmarks template with no variables (fast path)
func BenchmarkProcessTemplate_NoVariables(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := "Static text with no variables at all"
	data := map[string]interface{}{"device_id": "sensor001"}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

// BenchmarkProcessTemplate_ManyVariables benchmarks template with many variable substitutions
func BenchmarkProcessTemplate_ManyVariables(b *testing.B) {
	processor := &Processor{
		logger: newMockLogger(),
	}

	template := "{field1} {field2} {field3} {field4} {field5} {field6} {field7} {field8} {field9} {field10}"
	data := map[string]interface{}{
		"field1":  "value1",
		"field2":  "value2",
		"field3":  "value3",
		"field4":  "value4",
		"field5":  "value5",
		"field6":  "value6",
		"field7":  "value7",
		"field8":  "value8",
		"field9":  "value9",
		"field10": "value10",
	}
	timeCtx := NewSystemTimeProvider().GetCurrentContext()
	subjectCtx := NewSubjectContext("test.subject")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.processTemplate(template, data, timeCtx, subjectCtx, nil)
	}
}

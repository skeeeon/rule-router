package rule

import (
	"testing"
)

// TestSubjectContext_BasicFields tests basic subject field access
func TestSubjectContext_BasicFields(t *testing.T) {
	subject := "sensors.temperature.room1"
	ctx := NewSubjectContext(subject)

	tests := []struct {
		field string
		want  interface{}
	}{
		{"@subject", "sensors.temperature.room1"},
		{"@subject.count", 3},
		{"@subject.0", "sensors"},
		{"@subject.1", "temperature"},
		{"@subject.2", "room1"},
		{"@subject.first", "sensors"},
		{"@subject.last", "room1"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, exists := ctx.GetField(tt.field)
			if !exists {
				t.Fatalf("GetField(%s) field not found", tt.field)
			}
			if got != tt.want {
				t.Errorf("GetField(%s) = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}

// TestSubjectContext_TokenIndexing tests numeric token access
func TestSubjectContext_TokenIndexing(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		index   int
		want    string
	}{
		{
			name:    "first token",
			subject: "building.floor1.room101",
			index:   0,
			want:    "building",
		},
		{
			name:    "middle token",
			subject: "building.floor1.room101",
			index:   1,
			want:    "floor1",
		},
		{
			name:    "last token",
			subject: "building.floor1.room101",
			index:   2,
			want:    "room101",
		},
		{
			name:    "single token",
			subject: "sensors",
			index:   0,
			want:    "sensors",
		},
		{
			name:    "out of bounds positive",
			subject: "a.b.c",
			index:   5,
			want:    "",
		},
		{
			name:    "negative index",
			subject: "a.b.c",
			index:   -1,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewSubjectContext(tt.subject)
			got := ctx.GetToken(tt.index)
			if got != tt.want {
				t.Errorf("GetToken(%d) = %q, want %q", tt.index, got, tt.want)
			}
		})
	}
}

// TestSubjectContext_SpecialAccessors tests first/last/count accessors
func TestSubjectContext_SpecialAccessors(t *testing.T) {
	tests := []struct {
		name      string
		subject   string
		wantFirst string
		wantLast  string
		wantCount int
	}{
		{
			name:      "three tokens",
			subject:   "sensors.temperature.room1",
			wantFirst: "sensors",
			wantLast:  "room1",
			wantCount: 3,
		},
		{
			name:      "single token",
			subject:   "sensors",
			wantFirst: "sensors",
			wantLast:  "sensors",
			wantCount: 1,
		},
		{
			name:      "two tokens",
			subject:   "building.main",
			wantFirst: "building",
			wantLast:  "main",
			wantCount: 2,
		},
		{
			name:      "many tokens",
			subject:   "a.b.c.d.e.f.g.h.i.j",
			wantFirst: "a",
			wantLast:  "j",
			wantCount: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewSubjectContext(tt.subject)

			first, _ := ctx.GetField("@subject.first")
			if first != tt.wantFirst {
				t.Errorf("first = %q, want %q", first, tt.wantFirst)
			}

			last, _ := ctx.GetField("@subject.last")
			if last != tt.wantLast {
				t.Errorf("last = %q, want %q", last, tt.wantLast)
			}

			count, _ := ctx.GetField("@subject.count")
			if count != tt.wantCount {
				t.Errorf("count = %v, want %v", count, tt.wantCount)
			}
		})
	}
}

// TestSubjectContext_EmptySubject tests empty subject handling
func TestSubjectContext_EmptySubject(t *testing.T) {
	ctx := NewSubjectContext("")

	if ctx.Full != "" {
		t.Errorf("Full = %q, want empty", ctx.Full)
	}

	if len(ctx.Tokens) != 0 {
		t.Errorf("Tokens length = %d, want 0", len(ctx.Tokens))
	}

	if ctx.Count != 0 {
		t.Errorf("Count = %d, want 0", ctx.Count)
	}

	// Test field access on empty subject
	first, exists := ctx.GetField("@subject.first")
	if !exists {
		t.Error("@subject.first should exist even for empty subject")
	}
	if first != "" {
		t.Errorf("first token = %q, want empty", first)
	}

	count, _ := ctx.GetField("@subject.count")
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
}

// TestSubjectContext_LongSubject tests subjects with many tokens
func TestSubjectContext_LongSubject(t *testing.T) {
	// Build a 15-token subject (near NATS 16-token recommendation)
	tokens := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o"}
	subject := "a.b.c.d.e.f.g.h.i.j.k.l.m.n.o"
	ctx := NewSubjectContext(subject)

	if ctx.Count != 15 {
		t.Errorf("Count = %d, want 15", ctx.Count)
	}

	// Test all indices work
	for i := 0; i < 15; i++ {
		got := ctx.GetToken(i)
		if got != tokens[i] {
			t.Errorf("Token[%d] = %q, want %q", i, got, tokens[i])
		}
	}

	// Test first/last work correctly
	first, _ := ctx.GetField("@subject.first")
	if first != "a" {
		t.Errorf("first = %q, want 'a'", first)
	}

	last, _ := ctx.GetField("@subject.last")
	if last != "o" {
		t.Errorf("last = %q, want 'o'", last)
	}
}

// TestSubjectContext_SpecialCharacters tests subjects with special characters
func TestSubjectContext_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		tokens  []string
	}{
		{
			name:    "hyphens",
			subject: "sensor-001.temp-reading.room-101",
			tokens:  []string{"sensor-001", "temp-reading", "room-101"},
		},
		{
			name:    "underscores",
			subject: "device_id.data_type.reading_value",
			tokens:  []string{"device_id", "data_type", "reading_value"},
		},
		{
			name:    "numbers",
			subject: "building.123.floor.456",
			tokens:  []string{"building", "123", "floor", "456"},
		},
		{
			name:    "mixed",
			subject: "sensor_001.temp-123.room_a1",
			tokens:  []string{"sensor_001", "temp-123", "room_a1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewSubjectContext(tt.subject)

			if ctx.Count != len(tt.tokens) {
				t.Errorf("Count = %d, want %d", ctx.Count, len(tt.tokens))
			}

			for i, want := range tt.tokens {
				got := ctx.GetToken(i)
				if got != want {
					t.Errorf("Token[%d] = %q, want %q", i, got, want)
				}
			}
		})
	}
}

// TestSubjectContext_InvalidFields tests handling of invalid field names
func TestSubjectContext_InvalidFields(t *testing.T) {
	ctx := NewSubjectContext("sensors.temperature.room1")

	invalidFields := []string{
		"@subject.invalid",
		"@subject.",
		"@subject.99",  // Beyond typical range
		"@subject.-1",  // Negative as string
		"notafield",
		"",
	}

	for _, field := range invalidFields {
		t.Run(field, func(t *testing.T) {
			_, exists := ctx.GetField(field)
			if exists {
				t.Errorf("GetField(%s) should not exist", field)
			}
		})
	}
}

// TestSubjectContext_GetAllFieldNames tests field enumeration
func TestSubjectContext_GetAllFieldNames(t *testing.T) {
	ctx := NewSubjectContext("sensors.temperature.room1")

	fieldNames := ctx.GetAllFieldNames()

	expectedFields := []string{
		"@subject",
		"@subject.count",
		"@subject.first",
		"@subject.last",
		"@subject.0",
		"@subject.1",
		"@subject.2",
	}

	// Check all expected fields are present
	fieldMap := make(map[string]bool)
	for _, name := range fieldNames {
		fieldMap[name] = true
	}

	for _, expected := range expectedFields {
		if !fieldMap[expected] {
			t.Errorf("Expected field %s not found in GetAllFieldNames()", expected)
		}
	}

	// Should have exactly the expected number of fields
	if len(fieldNames) != len(expectedFields) {
		t.Errorf("GetAllFieldNames() returned %d fields, expected %d",
			len(fieldNames), len(expectedFields))
	}
}

// TestSubjectContext_WildcardUseCase tests realistic wildcard scenarios
func TestSubjectContext_WildcardUseCase(t *testing.T) {
	tests := []struct {
		name         string
		pattern      string
		actualSubj   string
		extractToken int
		wantValue    string
	}{
		{
			name:         "single wildcard - sensor type",
			pattern:      "sensors.*",
			actualSubj:   "sensors.temperature",
			extractToken: 1,
			wantValue:    "temperature",
		},
		{
			name:         "building routing",
			pattern:      "building.*.floor.*",
			actualSubj:   "building.main.floor.3",
			extractToken: 1,
			wantValue:    "main",
		},
		{
			name:         "device id extraction",
			pattern:      "devices.*.data",
			actualSubj:   "devices.sensor001.data",
			extractToken: 1,
			wantValue:    "sensor001",
		},
		{
			name:         "greedy wildcard",
			pattern:      "company.>",
			actualSubj:   "company.engineering.backend.deploy",
			extractToken: 1,
			wantValue:    "engineering",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewSubjectContext(tt.actualSubj)
			got := ctx.GetToken(tt.extractToken)
			if got != tt.wantValue {
				t.Errorf("Token[%d] = %q, want %q (subject: %s)",
					tt.extractToken, got, tt.wantValue, tt.actualSubj)
			}
		})
	}
}

// TestSubjectContext_TemplateUsage tests subject usage in templates
func TestSubjectContext_TemplateUsage(t *testing.T) {
	// Simulates template variable extraction scenarios
	tests := []struct {
		name    string
		subject string
		field   string
		want    interface{}
	}{
		{
			name:    "route by device type",
			subject: "devices.camera.motion",
			field:   "@subject.1",
			want:    "camera",
		},
		{
			name:    "route by event type",
			subject: "events.temperature.alert",
			field:   "@subject.2",
			want:    "alert",
		},
		{
			name:    "full subject for logging",
			subject: "sensors.temp.room101",
			field:   "@subject",
			want:    "sensors.temp.room101",
		},
		{
			name:    "token count for validation",
			subject: "a.b.c.d",
			field:   "@subject.count",
			want:    4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewSubjectContext(tt.subject)
			got, exists := ctx.GetField(tt.field)
			if !exists {
				t.Fatalf("GetField(%s) not found", tt.field)
			}
			if got != tt.want {
				t.Errorf("GetField(%s) = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}

// BenchmarkSubjectContext_Creation benchmarks context creation
func BenchmarkSubjectContext_Creation(b *testing.B) {
	subject := "sensors.temperature.room1"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NewSubjectContext(subject)
	}
}

// BenchmarkSubjectContext_GetToken benchmarks token access
func BenchmarkSubjectContext_GetToken(b *testing.B) {
	ctx := NewSubjectContext("sensors.temperature.room1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.GetToken(1)
	}
}

// BenchmarkSubjectContext_GetField benchmarks field lookup
func BenchmarkSubjectContext_GetField(b *testing.B) {
	ctx := NewSubjectContext("sensors.temperature.room1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.GetField("@subject.1")
	}
}

// BenchmarkSubjectContext_MultipleFields benchmarks realistic usage
func BenchmarkSubjectContext_MultipleFields(b *testing.B) {
	ctx := NewSubjectContext("building.main.floor.3.room.101")

	fields := []string{
		"@subject.0",
		"@subject.1",
		"@subject.count",
		"@subject.last",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, field := range fields {
			ctx.GetField(field)
		}
	}
}

// BenchmarkSubjectContext_LongSubject benchmarks with many tokens
func BenchmarkSubjectContext_LongSubject(b *testing.B) {
	subject := "a.b.c.d.e.f.g.h.i.j.k.l.m.n.o"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := NewSubjectContext(subject)
		_ = ctx.GetToken(7)
	}
}

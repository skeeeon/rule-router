package rule

import (
	"strings"
	"testing"
)

// TestPatternMatcher_ExactMatch tests exact subject matching without wildcards
func TestPatternMatcher_ExactMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{
			name:    "exact match single token",
			pattern: "sensors",
			subject: "sensors",
			want:    true,
		},
		{
			name:    "exact match multiple tokens",
			pattern: "sensors.temperature.room1",
			subject: "sensors.temperature.room1",
			want:    true,
		},
		{
			name:    "no match different subject",
			pattern: "sensors.temperature",
			subject: "cameras.motion",
			want:    false,
		},
		{
			name:    "no match extra token in subject",
			pattern: "sensors.temperature",
			subject: "sensors.temperature.room1",
			want:    false,
		},
		{
			name:    "no match missing token in subject",
			pattern: "sensors.temperature.room1",
			subject: "sensors.temperature",
			want:    false,
		},
		{
			name:    "case sensitive match",
			pattern: "Sensors.Temperature",
			subject: "Sensors.Temperature",
			want:    true,
		},
		{
			name:    "case sensitive no match",
			pattern: "sensors.temperature",
			subject: "Sensors.Temperature",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.Match(tt.subject)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v (pattern: %s, subject: %s)",
					got, tt.want, tt.pattern, tt.subject)
			}
		})
	}
}

// TestPatternMatcher_SingleWildcard tests single-level wildcard (*) matching
func TestPatternMatcher_SingleWildcard(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{
			name:    "wildcard matches single token",
			pattern: "sensors.*",
			subject: "sensors.temperature",
			want:    true,
		},
		{
			name:    "wildcard matches different tokens",
			pattern: "sensors.*",
			subject: "sensors.humidity",
			want:    true,
		},
		{
			name:    "wildcard no match multiple levels",
			pattern: "sensors.*",
			subject: "sensors.temperature.room1",
			want:    false,
		},
		{
			name:    "wildcard in middle position",
			pattern: "sensors.*.room1",
			subject: "sensors.temperature.room1",
			want:    true,
		},
		{
			name:    "multiple wildcards",
			pattern: "building.*.floor.*",
			subject: "building.main.floor.3",
			want:    true,
		},
		{
			name:    "wildcard at start",
			pattern: "*.temperature",
			subject: "sensors.temperature",
			want:    true,
		},
		{
			name:    "wildcard matches empty-like token",
			pattern: "a.*.c",
			subject: "a.b.c",
			want:    true,
		},
		{
			name:    "wildcard requires exact token count",
			pattern: "a.*.c",
			subject: "a.b.d.c",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.Match(tt.subject)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v (pattern: %s, subject: %s)",
					got, tt.want, tt.pattern, tt.subject)
			}
		})
	}
}

// TestPatternMatcher_GreedyWildcard tests multi-level wildcard (>) matching
func TestPatternMatcher_GreedyWildcard(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{
			name:    "greedy wildcard matches zero tokens",
			pattern: "sensors.>",
			subject: "sensors",
			want:    true,
		},
		{
			name:    "greedy wildcard matches one token",
			pattern: "sensors.>",
			subject: "sensors.temperature",
			want:    true,
		},
		{
			name:    "greedy wildcard matches many tokens",
			pattern: "sensors.>",
			subject: "sensors.temperature.room1.floor2.building3",
			want:    true,
		},
		{
			name:    "greedy wildcard at root",
			pattern: ">",
			subject: "any.subject.at.all",
			want:    true,
		},
		{
			name:    "greedy wildcard empty subject",
			pattern: ">",
			subject: "",
			want:    true,
		},
		{
			name:    "greedy wildcard with prefix",
			pattern: "building.main.>",
			subject: "building.main.floor.1.room.101",
			want:    true,
		},
		{
			name:    "greedy wildcard no match wrong prefix",
			pattern: "building.main.>",
			subject: "building.west.floor.1",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.Match(tt.subject)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v (pattern: %s, subject: %s)",
					got, tt.want, tt.pattern, tt.subject)
			}
		})
	}
}

// TestPatternMatcher_MixedWildcards tests combinations of wildcard types
func TestPatternMatcher_MixedWildcards(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{
			name:    "single then greedy",
			pattern: "building.*.>",
			subject: "building.main.floor.1.room.101",
			want:    true,
		},
		{
			name:    "single then greedy with exact",
			pattern: "sensors.*.data.>",
			subject: "sensors.temp001.data.reading.celsius",
			want:    true,
		},
		{
			name:    "multiple singles then greedy",
			pattern: "building.*.floor.*.>",
			subject: "building.main.floor.1.room.101",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.Match(tt.subject)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v (pattern: %s, subject: %s)",
					got, tt.want, tt.pattern, tt.subject)
			}
		})
	}
}

// TestValidatePattern tests pattern validation logic
func TestValidatePattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid exact pattern",
			pattern: "sensors.temperature",
			wantErr: false,
		},
		{
			name:    "valid single wildcard",
			pattern: "sensors.*",
			wantErr: false,
		},
		{
			name:    "valid greedy wildcard",
			pattern: "sensors.>",
			wantErr: false,
		},
		{
			name:    "valid mixed wildcards",
			pattern: "building.*.floor.*.>",
			wantErr: false,
		},
		{
			name:    "invalid empty pattern",
			pattern: "",
			wantErr: true,
			errMsg:  "pattern cannot be empty",
		},
		{
			name:    "invalid greedy wildcard not last",
			pattern: "sensors.>.temperature",
			wantErr: true,
			errMsg:  "'>' wildcard must be the last token",
		},
		{
			name:    "invalid greedy wildcard in middle",
			pattern: "building.>.floor.1",
			wantErr: true,
			errMsg:  "'>' wildcard must be the last token",
		},
		{
			name:    "invalid empty token",
			pattern: "sensors..temperature",
			wantErr: true,
			errMsg:  "empty token",
		},
		{
			name:    "invalid wildcard mixed with text",
			pattern: "sensors.temp*",
			wantErr: true,
			errMsg:  "invalid wildcard usage",
		},
		{
			name:    "invalid greedy mixed with text",
			pattern: "sensors.>data",
			wantErr: true,
			errMsg:  "invalid wildcard usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePattern(tt.pattern)
			
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidatePattern() expected error containing '%s', got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidatePattern() error = %v, want error containing '%s'", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidatePattern() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestPatternMatcher_IsPattern tests pattern detection
func TestPatternMatcher_IsPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{
			name:    "exact match is not pattern",
			pattern: "sensors.temperature",
			want:    false,
		},
		{
			name:    "single wildcard is pattern",
			pattern: "sensors.*",
			want:    true,
		},
		{
			name:    "greedy wildcard is pattern",
			pattern: "sensors.>",
			want:    true,
		},
		{
			name:    "mixed wildcards is pattern",
			pattern: "building.*.>",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.IsPattern()
			if got != tt.want {
				t.Errorf("IsPattern() = %v, want %v (pattern: %s)", got, tt.want, tt.pattern)
			}
		})
	}
}

// TestPatternMatcher_EdgeCases tests boundary conditions and edge cases
func TestPatternMatcher_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{
			name:    "empty subject with non-empty pattern",
			pattern: "sensors",
			subject: "",
			want:    false,
		},
		{
			name:    "empty subject with greedy wildcard",
			pattern: ">",
			subject: "",
			want:    true,
		},
		{
			name:    "single character tokens",
			pattern: "a.b.c",
			subject: "a.b.c",
			want:    true,
		},
		{
			name:    "very long tokens",
			pattern: "verylongtoken.anotherverylongtoken",
			subject: "verylongtoken.anotherverylongtoken",
			want:    true,
		},
		{
			name:    "numeric tokens",
			pattern: "sensors.123.456",
			subject: "sensors.123.456",
			want:    true,
		},
		{
			name:    "tokens with hyphens",
			pattern: "sensors.temp-sensor-001",
			subject: "sensors.temp-sensor-001",
			want:    true,
		},
		{
			name:    "tokens with underscores",
			pattern: "sensors.temp_sensor_001",
			subject: "sensors.temp_sensor_001",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPatternMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPatternMatcher() error = %v", err)
			}

			got := matcher.Match(tt.subject)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v (pattern: %s, subject: %s)",
					got, tt.want, tt.pattern, tt.subject)
			}
		})
	}
}

// TestContainsWildcards tests the wildcard detection helper
func TestContainsWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"sensors.temperature", false},
		{"sensors.*", true},
		{"sensors.>", true},
		{"building.*.floor.>", true},
		{"a.b.c.d", false},
		{"*", true},
		{">", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := containsWildcards(tt.pattern)
			if got != tt.want {
				t.Errorf("containsWildcards(%s) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

// BenchmarkPatternMatcher_ExactMatch benchmarks exact pattern matching performance
func BenchmarkPatternMatcher_ExactMatch(b *testing.B) {
	matcher, _ := NewPatternMatcher("sensors.temperature.room1")
	subject := "sensors.temperature.room1"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.Match(subject)
	}
}

// BenchmarkPatternMatcher_SingleWildcard benchmarks single wildcard performance
func BenchmarkPatternMatcher_SingleWildcard(b *testing.B) {
	matcher, _ := NewPatternMatcher("sensors.*.room1")
	subject := "sensors.temperature.room1"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.Match(subject)
	}
}

// BenchmarkPatternMatcher_GreedyWildcard benchmarks greedy wildcard performance
func BenchmarkPatternMatcher_GreedyWildcard(b *testing.B) {
	matcher, _ := NewPatternMatcher("sensors.>")
	subject := "sensors.temperature.room1.floor2.building3"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.Match(subject)
	}
}

// BenchmarkPatternMatcher_MixedWildcards benchmarks mixed wildcard performance
func BenchmarkPatternMatcher_MixedWildcards(b *testing.B) {
	matcher, _ := NewPatternMatcher("building.*.floor.*.>")
	subject := "building.main.floor.3.room.101.sensor.temp"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.Match(subject)
	}
}

// BenchmarkNewPatternMatcher benchmarks pattern compilation overhead
func BenchmarkNewPatternMatcher(b *testing.B) {
	patterns := []string{
		"sensors.temperature.room1",
		"sensors.*",
		"sensors.>",
		"building.*.floor.*.>",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pattern := patterns[i%len(patterns)]
		NewPatternMatcher(pattern)
	}
}

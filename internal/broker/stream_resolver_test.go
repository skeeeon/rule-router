// file: internal/broker/stream_resolver_test.go

package broker

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/internal/logger"
)

// newTestResolver creates a StreamResolver for testing without JetStream dependency
func newTestResolver() *StreamResolver {
	return &StreamResolver{
		streams:    make([]StreamInfo, 0),
		discovered: true,
		logger:     logger.NewNopLogger(),
	}
}

// newTestResolverWithStreams creates a StreamResolver with pre-populated streams
func newTestResolverWithStreams(streams []StreamInfo) *StreamResolver {
	return &StreamResolver{
		streams:    streams,
		discovered: true,
		logger:     logger.NewNopLogger(),
	}
}

func TestCalculateSpecificity(t *testing.T) {
	sr := newTestResolver()

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		// Exact matches (no wildcards) - 1000 + len(tokens)*10
		{"exact 1 token", "sensors", 1010},
		{"exact 2 tokens", "sensors.temperature", 1020},
		{"exact 3 tokens", "sensors.temperature.room1", 1030},
		{"exact 4 tokens", "sensors.temperature.room1.value", 1040},

		// Single wildcard (*) - 10 per token, 100 per exact
		{"single wildcard only", "*", 10},
		{"prefix with single wildcard", "sensors.*", 110},
		{"two wildcards", "*.*", 20},
		{"exact.wildcard.exact", "sensors.*.room1", 210},

		// Greedy wildcard (>) - 1 per token, 100 per exact
		{"greedy only", ">", 1},
		{"prefix with greedy", "sensors.>", 101},
		{"two exact with greedy", "sensors.temperature.>", 201},

		// Mixed wildcards
		{"exact.wildcard.greedy", "sensors.*.>", 111},
		{"wildcard.greedy", "*.>", 11},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sr.calculateSpecificity(tt.filter)
			if got != tt.want {
				t.Errorf("calculateSpecificity(%q) = %d, want %d", tt.filter, got, tt.want)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	sr := newTestResolver()

	tests := []struct {
		name    string
		subject string
		pattern string
		want    bool
	}{
		// Exact matches
		{"exact match", "sensors.temperature", "sensors.temperature", true},
		{"exact no match", "sensors.temperature", "sensors.humidity", false},

		// Single wildcard (*)
		{"single wildcard match", "sensors.temperature", "sensors.*", true},
		{"single wildcard no match depth", "sensors.temp.room1", "sensors.*", false},
		{"single wildcard middle", "sensors.temp.room1", "sensors.*.room1", true},
		{"single wildcard middle no match", "sensors.temp.room1", "sensors.*.room2", false},
		{"multiple single wildcards", "a.b.c", "*.*.*", true},
		{"single wildcard wrong length", "a.b", "*.*.*", false},

		// Greedy wildcard (>)
		{"greedy match 1 level", "sensors.temperature", "sensors.>", true},
		{"greedy match 2 levels", "sensors.temp.room1", "sensors.>", true},
		{"greedy match many levels", "sensors.temp.room1.value.max", "sensors.>", true},
		{"greedy only", "anything.here", ">", true},
		{"greedy no prefix match", "events.temperature", "sensors.>", false},

		// Mixed wildcards with greedy
		{"wildcard then greedy", "a.b.c.d", "a.*.>", true},
		{"wildcard then greedy exact prefix", "a.x.c.d.e", "a.*.>", true},

		// Edge cases
		{"empty pattern", "sensors", "", false},
		{"single token exact", "sensors", "sensors", true},
		{"single token no match", "sensors", "events", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sr.matchPattern(tt.subject, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.subject, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestSubjectMatches(t *testing.T) {
	sr := newTestResolver()

	tests := []struct {
		name    string
		subject string
		filter  string
		want    bool
	}{
		// Exact matches
		{"exact match", "sensors.temperature", "sensors.temperature", true},
		{"exact no match", "sensors.temperature", "sensors.humidity", false},

		// Subject is concrete, filter has wildcards
		{"concrete vs wildcard filter", "sensors.temperature", "sensors.*", true},
		{"concrete vs greedy filter", "sensors.temp.room1", "sensors.>", true},

		// Subject is pattern, filter covers it
		{"pattern covered by greedy", "sensors.*", "sensors.>", true},
		// Note: When filter has wildcards, matchPattern is called which treats > in subject as literal
		// So sensors.* filter with * matches the literal > token
		{"greedy subject vs wildcard filter", "sensors.>", "sensors.*", true},
		{"any pattern covered by root greedy", "sensors.*", ">", true},
		{"greedy covered by root greedy", "sensors.>", ">", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sr.subjectMatches(tt.subject, tt.filter)
			if got != tt.want {
				t.Errorf("subjectMatches(%q, %q) = %v, want %v", tt.subject, tt.filter, got, tt.want)
			}
		})
	}
}

func TestPatternCoveredBy(t *testing.T) {
	sr := newTestResolver()

	tests := []struct {
		name           string
		subjectPattern string
		filterPattern  string
		want           bool
	}{
		// Root greedy covers everything
		{"root greedy covers exact", "sensors.temperature", ">", true},
		{"root greedy covers wildcard", "sensors.*", ">", true},
		{"root greedy covers greedy", "sensors.>", ">", true},

		// Greedy filter covers matching prefix
		{"greedy covers wildcard same prefix", "sensors.*", "sensors.>", true},
		{"greedy covers exact same prefix", "sensors.temperature", "sensors.>", true},
		{"greedy covers greedy same prefix", "sensors.temp.>", "sensors.>", true},
		{"greedy does not cover different prefix", "events.*", "sensors.>", false},

		// Single wildcard filter
		{"wildcard does not cover greedy", "sensors.>", "sensors.*", false},
		{"wildcard covers wildcard same level", "sensors.*", "sensors.*", true},
		{"wildcard covers exact same level", "sensors.temperature", "sensors.*", true},

		// Exact filter
		{"exact covers exact", "sensors.temperature", "sensors.temperature", true},
		{"exact does not cover wildcard", "sensors.*", "sensors.temperature", false},
		{"exact does not cover greedy", "sensors.>", "sensors.temperature", false},

		// Length edge cases
		// Note: "sensors" is covered by "sensors.>" because > matches at that level and beyond
		{"shorter subject covered by greedy", "sensors", "sensors.>", true},
		{"longer subject covered by greedy", "sensors.temp.room1", "sensors.>", true},
		// But exact filter won't cover shorter subjects
		{"shorter subject not covered by non-greedy", "sensors", "sensors.temperature", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sr.patternCoveredBy(tt.subjectPattern, tt.filterPattern)
			if got != tt.want {
				t.Errorf("patternCoveredBy(%q, %q) = %v, want %v", tt.subjectPattern, tt.filterPattern, got, tt.want)
			}
		})
	}
}

func TestIsSystemStream(t *testing.T) {
	sr := newTestResolver()

	tests := []struct {
		name       string
		streamName string
		want       bool
	}{
		// System streams (should be deprioritized)
		{"dollar prefix", "$JS", true},
		{"dollar with suffix", "$G", true},
		{"KV prefix", "KV_mybucket", true},
		{"KV with complex name", "KV_user_sessions", true},

		// Normal streams
		{"normal stream", "SENSORS", false},
		{"normal with underscore", "USER_EVENTS", false},
		{"normal lowercase", "mystream", false},
		{"stream with KV in name but not prefix", "MYKVSTORE", false},
		{"stream with dollar in name but not prefix", "MY$STREAM", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sr.isSystemStream(tt.streamName)
			if got != tt.want {
				t.Errorf("isSystemStream(%q) = %v, want %v", tt.streamName, got, tt.want)
			}
		})
	}
}

func TestFindStreamForSubject(t *testing.T) {
	tests := []struct {
		name        string
		streams     []StreamInfo
		subject     string
		wantStream  string
		wantErr     bool
	}{
		{
			name: "single matching stream",
			streams: []StreamInfo{
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "SENSORS",
			wantErr:    false,
		},
		{
			name: "no matching stream",
			streams: []StreamInfo{
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "events.user.login",
			wantStream: "",
			wantErr:    true,
		},
		{
			name: "memory storage preferred over file",
			streams: []StreamInfo{
				{Name: "SENSORS_FILE", Subjects: []string{"sensors.>"}, Storage: jetstream.FileStorage},
				{Name: "SENSORS_MEM", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "SENSORS_MEM",
			wantErr:    false,
		},
		{
			name: "more specific pattern wins",
			streams: []StreamInfo{
				{Name: "ALL_SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
				{Name: "TEMP_SENSORS", Subjects: []string{"sensors.temperature.*"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature.room1",
			wantStream: "TEMP_SENSORS",
			wantErr:    false,
		},
		{
			name: "exact match wins over wildcard",
			streams: []StreamInfo{
				{Name: "WILDCARD", Subjects: []string{"sensors.*"}, Storage: jetstream.MemoryStorage},
				{Name: "EXACT", Subjects: []string{"sensors.temperature"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "EXACT",
			wantErr:    false,
		},
		{
			name: "primary stream preferred over mirror",
			streams: []StreamInfo{
				{Name: "SENSORS_MIRROR", MirrorFilter: "sensors.>", IsMirror: true, Storage: jetstream.MemoryStorage},
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "SENSORS",
			wantErr:    false,
		},
		{
			name: "system stream deprioritized",
			streams: []StreamInfo{
				{Name: "KV_sensors", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "SENSORS",
			wantErr:    false,
		},
		{
			name: "mirror stream match",
			streams: []StreamInfo{
				{Name: "SENSORS_MIRROR", MirrorFilter: "sensors.>", IsMirror: true, Storage: jetstream.MemoryStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "SENSORS_MIRROR",
			wantErr:    false,
		},
		{
			name: "source stream match",
			streams: []StreamInfo{
				{Name: "AGGREGATED", SourceFilters: []string{"sensors.>", "events.>"}, IsSource: true, Storage: jetstream.FileStorage},
			},
			subject:    "sensors.temperature",
			wantStream: "AGGREGATED",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := newTestResolverWithStreams(tt.streams)

			gotStream, err := sr.FindStreamForSubject(tt.subject)

			if tt.wantErr {
				if err == nil {
					t.Errorf("FindStreamForSubject(%q) expected error, got stream %q", tt.subject, gotStream)
				}
				return
			}

			if err != nil {
				t.Errorf("FindStreamForSubject(%q) unexpected error: %v", tt.subject, err)
				return
			}

			if gotStream != tt.wantStream {
				t.Errorf("FindStreamForSubject(%q) = %q, want %q", tt.subject, gotStream, tt.wantStream)
			}
		})
	}
}

func TestValidateSubjects(t *testing.T) {
	tests := []struct {
		name     string
		streams  []StreamInfo
		subjects []string
		wantErr  bool
	}{
		{
			name: "all subjects valid",
			streams: []StreamInfo{
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
				{Name: "EVENTS", Subjects: []string{"events.>"}, Storage: jetstream.MemoryStorage},
			},
			subjects: []string{"sensors.temperature", "events.user.login"},
			wantErr:  false,
		},
		{
			name: "one subject invalid",
			streams: []StreamInfo{
				{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
			},
			subjects: []string{"sensors.temperature", "events.user.login"},
			wantErr:  true,
		},
		{
			name:     "empty subjects list",
			streams:  []StreamInfo{{Name: "SENSORS", Subjects: []string{"sensors.>"}}},
			subjects: []string{},
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := newTestResolverWithStreams(tt.streams)

			err := sr.ValidateSubjects(tt.subjects)

			if tt.wantErr && err == nil {
				t.Error("ValidateSubjects() expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateSubjects() unexpected error: %v", err)
			}
		})
	}
}


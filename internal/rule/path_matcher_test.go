// file: internal/rule/path_matcher_test.go

package rule

import (
	"strings"
	"testing"
)

func TestPathMatcher_Match(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		requestPath string
		want        bool
	}{
		{
			name:        "exact match",
			pattern:     "/webhooks/github",
			requestPath: "/webhooks/github",
			want:        true,
		},
		{
			name:        "exact non-match different segment",
			pattern:     "/webhooks/github",
			requestPath: "/webhooks/gitlab",
			want:        false,
		},
		{
			name:        "exact non-match different length",
			pattern:     "/webhooks/github",
			requestPath: "/webhooks/github/pr",
			want:        false,
		},
		{
			name:        "single wildcard middle",
			pattern:     "/webhooks/*/events",
			requestPath: "/webhooks/github/events",
			want:        true,
		},
		{
			name:        "single wildcard does not span segments",
			pattern:     "/webhooks/*/events",
			requestPath: "/webhooks/github/pr/events",
			want:        false,
		},
		{
			name:        "single wildcard at end",
			pattern:     "/webhooks/*",
			requestPath: "/webhooks/github",
			want:        true,
		},
		{
			name:        "single wildcard at end rejects extra segment",
			pattern:     "/webhooks/*",
			requestPath: "/webhooks/github/pr",
			want:        false,
		},
		{
			name:        "terminal greedy matches one segment",
			pattern:     "/webhooks/>",
			requestPath: "/webhooks/github",
			want:        true,
		},
		{
			name:        "terminal greedy matches many segments",
			pattern:     "/webhooks/>",
			requestPath: "/webhooks/github/pr/123",
			want:        true,
		},
		{
			// Matches existing matcher semantics: ">" is zero-or-more
			// trailing tokens (see compile() in pattern.go).
			name:        "terminal greedy matches zero trailing segments",
			pattern:     "/webhooks/github/>",
			requestPath: "/webhooks/github",
			want:        true,
		},
		{
			name:        "terminal greedy non-match different prefix",
			pattern:     "/webhooks/github/>",
			requestPath: "/webhooks/gitlab/pr",
			want:        false,
		},
		{
			name:        "root pattern",
			pattern:     "/",
			requestPath: "/",
			want:        true,
		},
		{
			name:        "trailing slash on request tolerated",
			pattern:     "/webhooks/github",
			requestPath: "/webhooks/github/",
			want:        true,
		},
		{
			name:        "trailing slash on pattern tolerated",
			pattern:     "/webhooks/github/",
			requestPath: "/webhooks/github",
			want:        true,
		},
		{
			name:        "wildcard + greedy combo",
			pattern:     "/api/*/>",
			requestPath: "/api/v1/users/42",
			want:        true,
		},
		{
			name:        "wildcard + greedy combo rejects missing wildcard segment",
			pattern:     "/api/*/>",
			requestPath: "/api",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewPathMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPathMatcher(%q) error = %v", tt.pattern, err)
			}
			if got := MatchPath(matcher, tt.requestPath); got != tt.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v",
					tt.pattern, tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestValidatePathPattern(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantError string // substring of error, empty for success
	}{
		{name: "empty rejected", path: "", wantError: "empty"},
		{name: "missing leading slash", path: "webhooks/github", wantError: "must start with"},
		{name: "double slash rejected", path: "/webhooks//github", wantError: "empty segment"},
		{name: "non-terminal greedy rejected", path: "/webhooks/>/events", wantError: "must be the last"},
		{name: "mixed wildcard in segment rejected", path: "/webhooks/*bar", wantError: "invalid wildcard"},
		{name: "exact accepted", path: "/webhooks/github"},
		{name: "single wildcard accepted", path: "/webhooks/*/events"},
		{name: "terminal greedy accepted", path: "/webhooks/>"},
		{name: "root accepted", path: "/"},
		{name: "wildcard and greedy accepted", path: "/api/*/>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePathPattern(tt.path)
			if tt.wantError == "" {
				if err != nil {
					t.Errorf("ValidatePathPattern(%q) unexpected error: %v", tt.path, err)
				}
				return
			}
			if err == nil {
				t.Errorf("ValidatePathPattern(%q) expected error containing %q, got nil",
					tt.path, tt.wantError)
				return
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("ValidatePathPattern(%q) error = %v, want substring %q",
					tt.path, err, tt.wantError)
			}
		})
	}
}

func TestPathContainsWildcardsFn(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/webhooks/github", false},
		{"/", false},
		{"/webhooks/*/events", true},
		{"/webhooks/>", true},
		{"/api/v1/users", false},
	}
	for _, tt := range tests {
		if got := PathContainsWildcards(tt.path); got != tt.want {
			t.Errorf("PathContainsWildcards(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

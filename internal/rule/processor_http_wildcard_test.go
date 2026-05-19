// file: internal/rule/processor_http_wildcard_test.go

package rule

import (
	"testing"
)

// BenchmarkProcessHTTP_ExactOnly measures the request-path overhead when
// only exact rules are loaded (the previous behavior). After the refactor,
// findHTTPRules still walks an httpPatternRules slice that is empty in this
// case — this benchmark confirms that empty-slice walk is free.
func BenchmarkProcessHTTP_ExactOnly(b *testing.B) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/github", "POST", "exact-only"),
	}
	if err := processor.LoadRules(rules); err != nil {
		b.Fatalf("LoadRules: %v", err)
	}
	body := []byte(`{"event":"push"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.ProcessHTTP("/webhooks/github", "POST", body, nil)
	}
}

// BenchmarkProcessHTTP_WildcardOnly measures request-path overhead when only
// wildcard rules are loaded (new capability).
func BenchmarkProcessHTTP_WildcardOnly(b *testing.B) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/*/events", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		b.Fatalf("LoadRules: %v", err)
	}
	body := []byte(`{"event":"push"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.ProcessHTTP("/webhooks/github/events", "POST", body, nil)
	}
}

// BenchmarkProcessHTTP_MixedExactAndWildcard measures request-path overhead
// when both exact and wildcard rules exist and both fire.
func BenchmarkProcessHTTP_MixedExactAndWildcard(b *testing.B) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/github", "POST", "exact"),
		httpRule("/webhooks/*", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		b.Fatalf("LoadRules: %v", err)
	}
	body := []byte(`{"event":"push"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processor.ProcessHTTP("/webhooks/github", "POST", body, nil)
	}
}

// helper: produce an HTTP-triggered rule that publishes a tagged NATS subject.
func httpRule(path, method, tag string) Rule {
	return Rule{
		Trigger: Trigger{
			HTTP: &HTTPTrigger{Path: path, Method: method},
		},
		Action: Action{
			NATS: &NATSAction{
				Subject: "events." + tag,
				Payload: `{"tag":"` + tag + `"}`,
			},
		},
	}
}

// helper: collect the unique trailing tag from action subjects for assertions.
func subjectTags(actions []*Action) []string {
	tags := make([]string, 0, len(actions))
	for _, a := range actions {
		if a.NATS == nil {
			continue
		}
		// "events.<tag>"
		s := a.NATS.Subject
		if len(s) > len("events.") {
			tags = append(tags, s[len("events."):])
		}
	}
	return tags
}

func TestProcessHTTP_WildcardMatch(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/*/events", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	actions, err := processor.ProcessHTTP("/webhooks/github/events", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
}

func TestProcessHTTP_GreedyWildcardMatch(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/api/>", "GET", "greedy"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	cases := []string{"/api/v1", "/api/v1/users", "/api/v1/users/42/posts"}
	for _, p := range cases {
		actions, err := processor.ProcessHTTP(p, "GET", []byte(`{}`), nil)
		if err != nil {
			t.Fatalf("ProcessHTTP(%q): %v", p, err)
		}
		if len(actions) != 1 {
			t.Errorf("path %q: expected 1 action, got %d", p, len(actions))
		}
	}
}

func TestProcessHTTP_ExactAndWildcardBothFire(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/github", "POST", "exact"),
		httpRule("/webhooks/*", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	actions, err := processor.ProcessHTTP("/webhooks/github", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected both exact + wildcard rules to fire, got %d actions", len(actions))
	}

	tags := subjectTags(actions)
	gotExact, gotWildcard := false, false
	for _, tag := range tags {
		switch tag {
		case "exact":
			gotExact = true
		case "wildcard":
			gotWildcard = true
		}
	}
	if !gotExact || !gotWildcard {
		t.Errorf("missing match: gotExact=%v gotWildcard=%v (tags=%v)", gotExact, gotWildcard, tags)
	}
}

func TestProcessHTTP_MethodFilterAppliesAcrossExactAndPattern(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/github", "POST", "exact-post"),
		httpRule("/webhooks/*", "GET", "wildcard-get"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	postActions, _ := processor.ProcessHTTP("/webhooks/github", "POST", []byte(`{}`), nil)
	if len(postActions) != 1 || subjectTags(postActions)[0] != "exact-post" {
		t.Errorf("POST: expected only exact-post, got %v", subjectTags(postActions))
	}

	getActions, _ := processor.ProcessHTTP("/webhooks/github", "GET", []byte(`{}`), nil)
	if len(getActions) != 1 || subjectTags(getActions)[0] != "wildcard-get" {
		t.Errorf("GET: expected only wildcard-get, got %v", subjectTags(getActions))
	}
}

func TestProcessHTTP_NoMatchOnWildcard(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/*/events", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	// Wrong tail segment.
	actions, err := processor.ProcessHTTP("/webhooks/github/issues", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("expected no match, got %d actions", len(actions))
	}
}

func TestProcessHTTP_HasHTTPPathHandlesPatterns(t *testing.T) {
	processor := newTestProcessor()
	rules := []Rule{
		httpRule("/webhooks/*/events", "POST", "wildcard"),
	}
	if err := processor.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	if !processor.HasHTTPPath("/webhooks/github/events") {
		t.Error("expected HasHTTPPath to match wildcard rule")
	}
	if processor.HasHTTPPath("/nope/bar") {
		t.Error("expected HasHTTPPath to reject unmatched path")
	}
}

func TestProcessHTTP_KVRulesetWithPatterns(t *testing.T) {
	processor := newTestProcessor()

	// File rules empty; load KV ruleset directly.
	kvRule := httpRule("/kv/*/ok", "POST", "kv-wildcard")
	matcher, err := NewPathMatcher(kvRule.Trigger.HTTP.Path)
	if err != nil {
		t.Fatalf("NewPathMatcher: %v", err)
	}
	set := &HTTPKVRuleSet{
		Exact:    map[string][]*Rule{},
		Patterns: []*HTTPPatternRule{{Rule: &kvRule, Matcher: matcher}},
	}
	processor.ReplaceHTTPRules(set)

	if !processor.HasHTTPPath("/kv/anything/ok") {
		t.Error("KV pattern should be matched by HasHTTPPath")
	}

	actions, err := processor.ProcessHTTP("/kv/anything/ok", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP: %v", err)
	}
	if len(actions) != 1 {
		t.Errorf("expected 1 KV pattern action, got %d", len(actions))
	}

	// Swap to empty set and confirm matches disappear (atomic swap).
	processor.ReplaceHTTPRules(&HTTPKVRuleSet{Exact: map[string][]*Rule{}})
	if processor.HasHTTPPath("/kv/anything/ok") {
		t.Error("after swap, KV pattern should no longer match")
	}
}

func TestProcessHTTP_FileAndKVExactAndPatternAllFire(t *testing.T) {
	processor := newTestProcessor()

	// File: one exact, one pattern.
	if err := processor.LoadRules([]Rule{
		httpRule("/webhooks/github", "POST", "file-exact"),
		httpRule("/webhooks/*", "POST", "file-pattern"),
	}); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	// KV: another exact + pattern for the same path.
	kvExact := httpRule("/webhooks/github", "POST", "kv-exact")
	kvPattern := httpRule("/webhooks/>", "POST", "kv-pattern")
	matcher, err := NewPathMatcher(kvPattern.Trigger.HTTP.Path)
	if err != nil {
		t.Fatalf("NewPathMatcher: %v", err)
	}
	processor.ReplaceHTTPRules(&HTTPKVRuleSet{
		Exact:    map[string][]*Rule{"/webhooks/github": {&kvExact}},
		Patterns: []*HTTPPatternRule{{Rule: &kvPattern, Matcher: matcher}},
	})

	actions, err := processor.ProcessHTTP("/webhooks/github", "POST", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP: %v", err)
	}
	tags := subjectTags(actions)
	want := map[string]bool{
		"file-exact":   false,
		"file-pattern": false,
		"kv-exact":     false,
		"kv-pattern":   false,
	}
	for _, tag := range tags {
		if _, ok := want[tag]; ok {
			want[tag] = true
		}
	}
	for tag, fired := range want {
		if !fired {
			t.Errorf("expected rule %q to fire (tags=%v)", tag, tags)
		}
	}
}

package tester

import (
	"os"
	"path/filepath"
	"testing"
)

// twoRulesOneSubject is two rules sharing a trigger with mutually exclusive
// conditions — the shape where loading the whole file makes a check of one rule
// report its sibling's match.
const twoRulesOneSubject = `
- trigger:
    nats:
      subject: things.*
  conditions:
    operator: and
    items:
      - field: "{tag}"
        operator: exists
  action:
    nats:
      subject: out.tagged
      passthrough: true
- trigger:
    nats:
      subject: things.*
  conditions:
    operator: and
    items:
      - field: "{tag}"
        operator: not_exists
  action:
    nats:
      subject: out.untagged
      passthrough: true
`

// TestSetupTestProcessor_RuleIndex pins that a rule index isolates that rule,
// which both "rule-cli check -n" and _rule_N/ test groups depend on.
func TestSetupTestProcessor_RuleIndex(t *testing.T) {
	rulePath := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(rulePath, []byte(twoRulesOneSubject), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		ruleIndex int
		msg       string
		want      []string // published subjects
	}{
		{"rule 0 matches tagged", 0, `{"tag":"a"}`, []string{"out.tagged"}},
		{"rule 0 ignores untagged", 0, `{}`, nil},
		{"rule 1 matches untagged", 1, `{}`, []string{"out.untagged"}},
		{"rule 1 ignores tagged", 1, `{"tag":"a"}`, nil},
		{"whole file, tagged", -1, `{"tag":"a"}`, []string{"out.tagged"}},
		{"whole file, untagged", -1, `{}`, []string{"out.untagged"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor, err := setupTestProcessor(rulePath, tt.ruleIndex, nil, &Config{}, false)
			if err != nil {
				t.Fatalf("setupTestProcessor: %v", err)
			}
			outcome, err := processor.ProcessNATS("things.x", []byte(tt.msg), nil)
			if err != nil {
				t.Fatalf("ProcessNATS: %v", err)
			}
			var got []string
			for _, a := range outcome.All() {
				if a.NATS != nil {
					got = append(got, a.NATS.Subject)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("published %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("published %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSetupTestProcessor_RuleIndexOutOfRange(t *testing.T) {
	rulePath := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(rulePath, []byte(twoRulesOneSubject), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTestProcessor(rulePath, 2, nil, &Config{}, false); err == nil {
		t.Fatal("expected an error for rule index 2 in a two-rule file")
	}
}

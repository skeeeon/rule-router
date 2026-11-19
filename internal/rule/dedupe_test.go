package rule

import (
	"testing"
	"time"
)

const (
	dedupeRuleA = `
- trigger: {nats: {subject: in.a}}
  action: {nats: {subject: out.a, payload: '{"a": 1}'}}
`
	dedupeRuleB = `
- trigger: {nats: {subject: in.b}}
  action: {nats: {subject: out.b, payload: '{"b": 1}'}}
`
	dedupeRuleC = `
- trigger: {nats: {subject: in.c}}
  action: {nats: {subject: out.c, payload: '{"c": 1}'}}
`
)

func parseDedupeRules(t *testing.T, yamlText string) []Rule {
	t.Helper()
	rules, err := newTestLoader().ParseAndValidateYAML([]byte(yamlText), "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return rules
}

// TestDedupeKey_FollowsTheRuleNotItsPosition pins why the key is a hash of the
// rule and not its index: when rules are added or reordered, a rule must keep
// its key, and must never take over another rule's. If B inherited A's key, a
// redelivery would publish B's output under A's Nats-Msg-Id and the stream
// would drop it as a duplicate.
func TestDedupeKey_FollowsTheRuleNotItsPosition(t *testing.T) {
	before := parseDedupeRules(t, dedupeRuleA+dedupeRuleB)
	after := parseDedupeRules(t, dedupeRuleC+dedupeRuleB+dedupeRuleA)

	keyA, keyB := before[0].Action.NATS.DedupeKey, before[1].Action.NATS.DedupeKey
	if keyA == "" || keyB == "" {
		t.Fatalf("expected keys on both rules, got A=%q B=%q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("different rules share key %q", keyA)
	}
	if got := after[2].Action.NATS.DedupeKey; got != keyA {
		t.Errorf("rule A moved and changed key: %q -> %q", keyA, got)
	}
	if got := after[1].Action.NATS.DedupeKey; got != keyB {
		t.Errorf("rule B moved and changed key: %q -> %q", keyB, got)
	}
	if got := after[0].Action.NATS.DedupeKey; got == keyA || got == keyB {
		t.Errorf("new rule C took an existing key %q", got)
	}
}

// TestDedupeKey_ChangesWhenTheRuleChanges: an edited rule is a different rule.
// A new key can at worst let one duplicate through; a reused key could drop
// the edited rule's output.
func TestDedupeKey_ChangesWhenTheRuleChanges(t *testing.T) {
	original := parseDedupeRules(t, dedupeRuleA)[0].Action.NATS.DedupeKey
	edited := parseDedupeRules(t, `
- trigger: {nats: {subject: in.a}}
  action: {nats: {subject: out.a, payload: '{"a": 2}'}}
`)[0].Action.NATS.DedupeKey
	if original == edited {
		t.Errorf("edited rule kept key %q", original)
	}
}

// TestDedupeKey_CarriedOntoEvaluatedActions: the publisher only sees the
// evaluated action, so the key must survive evaluation, and each forEach
// element needs its own. The element suffix is its array position, so it does
// not shift when the filter skips an earlier element.
func TestDedupeKey_CarriedOntoEvaluatedActions(t *testing.T) {
	p := newTestProcessor()
	ctx := newTemplateTestContext(map[string]any{
		"items": []any{
			map[string]any{"id": "x", "keep": true},
			map[string]any{"id": "y", "keep": false},
			map[string]any{"id": "z", "keep": true},
		},
	}, "in", time.Now())

	single, err := p.processNATSAction(&NATSAction{Subject: "out", Payload: "{}", DedupeKey: "k"}, ctx)
	if err != nil {
		t.Fatalf("processNATSAction: %v", err)
	}
	if got := single[0].NATS.DedupeKey; got != "k" {
		t.Errorf("single action key = %q, want %q", got, "k")
	}

	fanout, err := p.processNATSAction(&NATSAction{
		ForEach:   "{items}",
		Filter:    &Conditions{Operator: "and", Items: []Condition{{Field: "{keep}", Operator: "eq", Value: true}}},
		Subject:   "out.{id}",
		Payload:   "{}",
		DedupeKey: "k",
	}, ctx)
	if err != nil {
		t.Fatalf("forEach: %v", err)
	}
	var got []string
	for _, a := range fanout {
		got = append(got, a.NATS.DedupeKey)
	}
	if len(got) != 2 || got[0] != "k-0" || got[1] != "k-2" {
		t.Errorf("forEach keys = %v, want [k-0 k-2]", got)
	}

	// No key on the rule means none on its actions, never a bare "-<i>".
	unkeyed, err := p.processNATSAction(&NATSAction{ForEach: "{items}", Subject: "out.{id}", Payload: "{}"}, ctx)
	if err != nil {
		t.Fatalf("unkeyed forEach: %v", err)
	}
	for _, a := range unkeyed {
		if a.NATS.DedupeKey != "" {
			t.Errorf("unkeyed action got key %q", a.NATS.DedupeKey)
		}
	}
}

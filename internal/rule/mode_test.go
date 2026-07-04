// file: internal/rule/mode_test.go

package rule

import (
	"strings"
	"testing"

	"rule-router/internal/logger"
)

// TestNATSMode_Validation exercises loader validation for the trigger/action
// delivery mode fields.
func TestNATSMode_Validation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string // substring; "" means must succeed
	}{
		{
			name: "trigger mode core ok",
			yaml: `- trigger: { nats: { subject: "in.x", mode: core } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "trigger mode jetstream ok",
			yaml: `- trigger: { nats: { subject: "in.x", mode: jetstream } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "trigger mode invalid rejected",
			yaml: `- trigger: { nats: { subject: "in.x", mode: quantum } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "trigger mode must be 'jetstream' or 'core'",
		},
		{
			name: "reply with jetstream mode rejected",
			yaml: `- trigger: { nats: { subject: "svc.x", reply: true, mode: jetstream } }
  action: { respond: { payload: "{}" } }`,
			wantErr: "cannot set 'mode: jetstream'",
		},
		{
			name: "reply with explicit core mode ok",
			yaml: `- trigger: { nats: { subject: "svc.x", reply: true, mode: core } }
  action: { respond: { payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "queue on core trigger ok",
			yaml: `- trigger: { nats: { subject: "in.x", mode: core, queue: "workers" } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "queue on jetstream trigger rejected",
			yaml: `- trigger: { nats: { subject: "in.x", queue: "workers" } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "'queue' requires a core subscription",
		},
		{
			name: "action mode core ok",
			yaml: `- trigger: { nats: { subject: "in.x" } }
  action: { nats: { subject: "out.x", mode: core, payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "action mode jetstream ok",
			yaml: `- trigger: { nats: { subject: "in.x" } }
  action: { nats: { subject: "out.x", mode: jetstream, payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "action mode invalid rejected",
			yaml: `- trigger: { nats: { subject: "in.x" } }
  action: { nats: { subject: "out.x", mode: telepathy, payload: "{}" } }`,
			wantErr: "action mode must be 'jetstream' or 'core'",
		},
	}

	loader := NewRulesLoader(logger.NewNopLogger(), nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loader.ParseAndValidateYAML([]byte(c.yaml), "test")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", c.wantErr, err)
			}
		})
	}
}

// TestNATSTrigger_IsCore verifies the core-transport classification used by
// the subscription routing (JetStream consumer vs core subscription).
func TestNATSTrigger_IsCore(t *testing.T) {
	cases := []struct {
		name    string
		trigger NATSTrigger
		want    bool
	}{
		{"default is jetstream", NATSTrigger{Subject: "x"}, false},
		{"explicit jetstream", NATSTrigger{Subject: "x", Mode: ModeJetStream}, false},
		{"explicit core", NATSTrigger{Subject: "x", Mode: ModeCore}, true},
		{"reply implies core", NATSTrigger{Subject: "x", Reply: true}, true},
		{"reply plus explicit core", NATSTrigger{Subject: "x", Reply: true, Mode: ModeCore}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.trigger.IsCore(); got != c.want {
				t.Errorf("IsCore() = %v, want %v", got, c.want)
			}
		})
	}
}

// modeFilterRules returns one core-mode and one jetstream-mode rule sharing
// the same trigger subject, with distinct action subjects for attribution.
func modeFilterRules() []Rule {
	return []Rule{
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.temp", Mode: ModeCore}},
			Action:  Action{NATS: &NATSAction{Subject: "out.core", Payload: "{}"}},
		},
		{
			Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.temp"}},
			Action:  Action{NATS: &NATSAction{Subject: "out.jetstream", Payload: "{}"}},
		},
	}
}

// assertActionSubjects fails unless the actions' NATS subjects are exactly want.
func assertActionSubjects(t *testing.T, actions []*Action, want ...string) {
	t.Helper()
	var got []string
	for _, a := range actions {
		if a.NATS != nil {
			got = append(got, a.NATS.Subject)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("action subjects: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("action subjects: got %v, want %v", got, want)
		}
	}
}

// TestProcessForSubscription_TransportFilter_Index verifies that when core and
// JetStream rules share a subject (file-loaded, index path), each transport's
// filter evaluates only its own rules — the double-fire guard.
func TestProcessForSubscription_TransportFilter_Index(t *testing.T) {
	p := newTestProcessor()
	if err := p.LoadRules(modeFilterRules()); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	payload := []byte(`{"v":1}`)

	jsActions, err := p.ProcessForSubscription("sensors.temp", "sensors.temp", payload, nil, JetStreamRuleFilter)
	if err != nil {
		t.Fatalf("jetstream ProcessForSubscription failed: %v", err)
	}
	assertActionSubjects(t, jsActions, "out.jetstream")

	coreActions, err := p.ProcessForSubscription("sensors.temp", "sensors.temp", payload, nil, CoreRuleFilter)
	if err != nil {
		t.Fatalf("core ProcessForSubscription failed: %v", err)
	}
	assertActionSubjects(t, coreActions, "out.core")

	allActions, err := p.ProcessForSubscription("sensors.temp", "sensors.temp", payload, nil, nil)
	if err != nil {
		t.Fatalf("unfiltered ProcessForSubscription failed: %v", err)
	}
	if len(allActions) != 2 {
		t.Fatalf("unfiltered: expected 2 actions, got %d", len(allActions))
	}
}

// TestProcessForSubscription_TransportFilter_KV verifies the same double-fire
// guard on the KV rule-set path, including that a subject whose KV rules are
// all filtered out returns nothing rather than falling back to pattern matching.
func TestProcessForSubscription_TransportFilter_KV(t *testing.T) {
	p := newTestProcessor()

	rules := modeFilterRules()
	coreRule, jsRule := &rules[0], &rules[1]
	p.ReplaceKVRuleSet(&KVRuleSet{
		NATS: map[string][]*Rule{
			"sensors.temp": {coreRule, jsRule},
			"sensors.hum":  {{Trigger: Trigger{NATS: &NATSTrigger{Subject: "sensors.hum", Mode: ModeCore}}, Action: Action{NATS: &NATSAction{Subject: "out.hum", Payload: "{}"}}}},
		},
		HTTP: &HTTPKVRuleSet{Exact: map[string][]*Rule{}},
	})

	payload := []byte(`{"v":1}`)

	jsActions, err := p.ProcessForSubscription("sensors.temp", "sensors.temp", payload, nil, JetStreamRuleFilter)
	if err != nil {
		t.Fatalf("jetstream ProcessForSubscription failed: %v", err)
	}
	assertActionSubjects(t, jsActions, "out.jetstream")

	coreActions, err := p.ProcessForSubscription("sensors.temp", "sensors.temp", payload, nil, CoreRuleFilter)
	if err != nil {
		t.Fatalf("core ProcessForSubscription failed: %v", err)
	}
	assertActionSubjects(t, coreActions, "out.core")

	// A core-only subject seen through the JetStream filter yields nothing.
	empty, err := p.ProcessForSubscription("sensors.hum", "sensors.hum", payload, nil, JetStreamRuleFilter)
	if err != nil {
		t.Fatalf("filtered-to-empty ProcessForSubscription failed: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no actions for core-only subject under jetstream filter, got %d", len(empty))
	}
}

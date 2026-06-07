// file: internal/rule/respond_test.go

package rule

import (
	"strings"
	"testing"

	"rule-router/internal/logger"
)

// TestProcessHTTP_RespondAction verifies an HTTP-triggered respond rule produces
// a Respond action with the templated payload, status code, and headers.
func TestProcessHTTP_RespondAction(t *testing.T) {
	p := newTestProcessor()
	rule := Rule{
		Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/api/quote", Method: "POST"}},
		Action: Action{Respond: &RespondAction{
			StatusCode: 201,
			Payload:    `{"symbol":"{symbol}"}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		}},
	}
	if err := p.LoadRules([]Rule{rule}); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	actions, err := p.ProcessHTTP("/api/quote", "POST", []byte(`{"symbol":"ACME"}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP failed: %v", err)
	}
	if len(actions) != 1 || actions[0].Respond == nil {
		t.Fatalf("expected 1 respond action, got %#v", actions)
	}
	got := actions[0].Respond
	if got.StatusCode != 201 {
		t.Errorf("status code: got %d, want 201", got.StatusCode)
	}
	if got.Payload != `{"symbol":"ACME"}` {
		t.Errorf("payload: got %q, want %q", got.Payload, `{"symbol":"ACME"}`)
	}
	if got.Headers["Content-Type"] != "application/json" {
		t.Errorf("header Content-Type: got %q", got.Headers["Content-Type"])
	}
}

// TestProcessNATS_RespondAction verifies a NATS reply rule produces a Respond action.
func TestProcessNATS_RespondAction(t *testing.T) {
	p := newTestProcessor()
	rule := Rule{
		Trigger: Trigger{NATS: &NATSTrigger{Subject: "services.echo", Reply: true}},
		Action:  Action{Respond: &RespondAction{Payload: `{"echo":"{ping}"}`}},
	}
	if err := p.LoadRules([]Rule{rule}); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	actions, err := p.ProcessNATS("services.echo", []byte(`{"ping":"hello"}`), nil)
	if err != nil {
		t.Fatalf("ProcessNATS failed: %v", err)
	}
	if len(actions) != 1 || actions[0].Respond == nil {
		t.Fatalf("expected 1 respond action, got %#v", actions)
	}
	if actions[0].Respond.Payload != `{"echo":"hello"}` {
		t.Errorf("payload: got %q, want %q", actions[0].Respond.Payload, `{"echo":"hello"}`)
	}
}

// TestProcessHTTP_BridgePreservesRequest verifies the HTTP↔NATS bridge survives
// rule processing: the evaluated NATS action must keep Request/Timeout, or the
// gateway misroutes it to a fire-and-forget publish and returns 404.
func TestProcessHTTP_BridgePreservesRequest(t *testing.T) {
	p := newTestProcessor()
	rule := Rule{
		Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/api/geocode", Method: "POST"}},
		Action:  Action{NATS: &NATSAction{Subject: "services.geocode", Request: true, Timeout: "3s"}},
	}
	if err := p.LoadRules([]Rule{rule}); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	actions, err := p.ProcessHTTP("/api/geocode", "POST", []byte(`{"address":"x"}`), nil)
	if err != nil {
		t.Fatalf("ProcessHTTP failed: %v", err)
	}
	if len(actions) != 1 || actions[0].NATS == nil {
		t.Fatalf("expected 1 NATS action, got %#v", actions)
	}
	if !actions[0].NATS.Request {
		t.Error("processed NATS action lost Request=true — bridge would be misrouted to a plain publish")
	}
	if actions[0].NATS.Timeout != "3s" {
		t.Errorf("processed NATS action lost Timeout: got %q, want %q", actions[0].NATS.Timeout, "3s")
	}
	if actions[0].NATS.Subject != "services.geocode" {
		t.Errorf("subject: got %q", actions[0].NATS.Subject)
	}
}

// TestHasSyncHTTPPath classifies which HTTP paths require synchronous handling.
func TestHasSyncHTTPPath(t *testing.T) {
	p := newTestProcessor()
	rules := []Rule{
		{
			Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/respond", Method: "POST"}},
			Action:  Action{Respond: &RespondAction{Payload: `{}`}},
		},
		{
			Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/bridge", Method: "POST"}},
			Action:  Action{NATS: &NATSAction{Subject: "svc.req", Request: true, Timeout: "2s"}},
		},
		{
			Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/async", Method: "POST"}},
			Action:  Action{NATS: &NATSAction{Subject: "events.in", Payload: `{}`}},
		},
	}
	if err := p.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"/respond", true},
		{"/bridge", true},
		{"/async", false},
		{"/unknown", false},
	}
	for _, c := range cases {
		if got := p.HasSyncHTTPPath(c.path, "POST"); got != c.want {
			t.Errorf("HasSyncHTTPPath(%q): got %v, want %v", c.path, got, c.want)
		}
	}
}

// TestValidateTriggerActionCompatibility covers the request/reply pairing rules.
func TestValidateTriggerActionCompatibility(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string // substring; "" means must succeed
	}{
		{
			name: "respond on http trigger ok",
			yaml: `- trigger: { http: { path: "/x", method: "POST" } }
  action: { respond: { payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "respond on nats reply trigger ok",
			yaml: `- trigger: { nats: { subject: "svc.x", reply: true } }
  action: { respond: { payload: "{}" } }`,
			wantErr: "",
		},
		{
			name: "respond on schedule trigger rejected",
			yaml: `- trigger: { schedule: { cron: "* * * * *" } }
  action: { respond: { payload: "{}" } }`,
			wantErr: "respond action requires an HTTP trigger",
		},
		{
			name: "nats reply without respond rejected",
			yaml: `- trigger: { nats: { subject: "svc.x", reply: true } }
  action: { nats: { subject: "out.x", payload: "{}" } }`,
			wantErr: "requires a respond action",
		},
		{
			name: "nats request on non-http trigger rejected",
			yaml: `- trigger: { nats: { subject: "in.x" } }
  action: { nats: { subject: "svc.x", request: true } }`,
			wantErr: "only supported on HTTP triggers",
		},
		{
			name: "nats request on http trigger ok",
			yaml: `- trigger: { http: { path: "/g", method: "POST" } }
  action: { nats: { subject: "svc.x", request: true, timeout: "3s" } }`,
			wantErr: "",
		},
		{
			name: "timeout without request rejected",
			yaml: `- trigger: { http: { path: "/g", method: "POST" } }
  action: { nats: { subject: "svc.x", timeout: "3s" } }`,
			wantErr: "only valid with 'request: true'",
		},
		{
			name: "respond statusCode out of range rejected",
			yaml: `- trigger: { http: { path: "/x", method: "POST" } }
  action: { respond: { statusCode: 999, payload: "{}" } }`,
			wantErr: "statusCode must be between 100 and 599",
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

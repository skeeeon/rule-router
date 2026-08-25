package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// ghSign computes a GitHub-style "sha256=<hex>" signature over body.
func ghSign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newHMACTestServer builds an InboundServer backed by a Processor holding a
// single HMAC-gated rule with a respond action (so the happy path needs no
// NATS connection). js/nc are nil — the fail-closed and respond paths never
// touch them.
func newHMACTestServer(t *testing.T, secret string) *InboundServer {
	t.Helper()
	log := logger.NewNop()
	proc := rule.NewProcessor(log)
	r := rule.Rule{
		Trigger: rule.Trigger{HTTP: &rule.HTTPTrigger{
			Path:   "/webhooks/github/push",
			Method: "POST",
			HMAC:   &rule.HMACConfig{Header: "X-Hub-Signature-256", Secret: secret, Prefix: "sha256="},
		}},
		Action: rule.Action{Respond: &rule.RespondAction{StatusCode: 200, Payload: `{"ok":true}`}},
	}
	if err := proc.LoadRules([]rule.Rule{r}); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return NewInboundServer(log, nil, proc, nil, nil, &Config{})
}

func TestWebhookHandler_HMACGate(t *testing.T) {
	const secret = "topsecret"
	const path = "/webhooks/github/push"
	body := `{"ref":"refs/heads/main"}`

	tests := []struct {
		name      string
		signature string // value for X-Hub-Signature-256; "" means omit the header
		wantCode  int
	}{
		{name: "valid signature passes gate", signature: ghSign(secret, body), wantCode: http.StatusOK},
		{name: "tampered body rejected", signature: ghSign(secret, "different"), wantCode: http.StatusUnauthorized},
		{name: "wrong secret rejected", signature: ghSign("nope", body), wantCode: http.StatusUnauthorized},
		{name: "missing signature rejected", signature: "", wantCode: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newHMACTestServer(t, secret)
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}
			w := httptest.NewRecorder()

			s.webhookHandler(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tt.wantCode, w.Body.String())
			}
		})
	}
}

// newQueryTestServer builds an InboundServer with a single respond rule that
// echoes query parameters, so a request through webhookHandler proves the
// gateway extracts them and hands them to the Processor.
func newQueryTestServer(t *testing.T) *InboundServer {
	t.Helper()
	log := logger.NewNop()
	proc := rule.NewProcessor(log)
	r := rule.Rule{
		Trigger: rule.Trigger{HTTP: &rule.HTTPTrigger{Path: "/echo", Method: "GET"}},
		Action: rule.Action{Respond: &rule.RespondAction{
			StatusCode: 200,
			Payload:    `{"tenant":"{@query.tenant}","page":"{@query.page}","missing":"{@query.nope}"}`,
		}},
	}
	if err := proc.LoadRules([]rule.Rule{r}); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return NewInboundServer(log, nil, proc, nil, nil, &Config{})
}

func TestWebhookHandler_QueryParams(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "parameters reach the rule",
			url:  "/echo?tenant=acme&page=2",
			want: `{"tenant":"acme","page":"2","missing":""}`,
		},
		{
			name: "percent-encoding and plus are decoded",
			url:  "/echo?tenant=a%26b&page=x+y",
			want: `{"tenant":"a&b","page":"x y","missing":""}`,
		},
		{
			name: "first value of a repeated name wins",
			url:  "/echo?tenant=first&tenant=second&page=1",
			want: `{"tenant":"first","page":"1","missing":""}`,
		},
		{
			name: "no query string still matches the path",
			url:  "/echo",
			want: `{"tenant":"","page":"","missing":""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newQueryTestServer(t)
			w := httptest.NewRecorder()
			s.webhookHandler(w, httptest.NewRequest(http.MethodGet, tt.url, nil))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
			}
			if got := w.Body.String(); got != tt.want {
				t.Errorf("body = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestWebhookHandler_QueryDoesNotAffectRouting pins that rule matching and the
// metrics label key off the path alone: a query string must not turn a known
// path into a 404, nor make an unknown one match.
func TestWebhookHandler_QueryDoesNotAffectRouting(t *testing.T) {
	s := newQueryTestServer(t)

	w := httptest.NewRecorder()
	s.webhookHandler(w, httptest.NewRequest(http.MethodGet, "/echo?anything=goes", nil))
	if w.Code != http.StatusOK {
		t.Errorf("known path with query: status = %d, want 200", w.Code)
	}

	w = httptest.NewRecorder()
	s.webhookHandler(w, httptest.NewRequest(http.MethodGet, "/nope?tenant=acme", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown path with query: status = %d, want 404", w.Code)
	}
}

// file: internal/gateway/server_test.go

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
	log := logger.NewNopLogger()
	proc := rule.NewProcessor(log, nil, nil, nil)
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
	return NewInboundServer(log, nil, proc, nil, nil, &ServerConfig{}, &PublishConfig{})
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

// file: internal/rule/hmac_test.go

package rule

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/textproto"
	"testing"
)

// sign computes the reference signature for a body, matching verifyHMAC's scheme.
func sign(algorithm, encoding string, secret, body []byte) string {
	var newHash func() hash.Hash
	switch algorithm {
	case "sha1":
		newHash = sha1.New
	default:
		newHash = sha256.New
	}
	mac := hmac.New(newHash, secret)
	mac.Write(body)
	sum := mac.Sum(nil)
	if encoding == "base64" {
		return base64.StdEncoding.EncodeToString(sum)
	}
	return hex.EncodeToString(sum)
}

func headersWith(header, value string) map[string]string {
	if value == "" {
		return map[string]string{}
	}
	return map[string]string{textproto.CanonicalMIMEHeaderKey(header): value}
}

func TestVerifyHMAC(t *testing.T) {
	secret := []byte("topsecret")
	body := []byte(`{"hello":"world"}`)

	tests := []struct {
		name    string
		cfg     *HMACConfig
		secret  []byte
		body    []byte
		headers map[string]string
		want    string
	}{
		{
			name:    "github sha256 hex with prefix",
			cfg:     &HMACConfig{Header: "X-Hub-Signature-256", Prefix: "sha256="},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Hub-Signature-256", "sha256="+sign("sha256", "hex", secret, body)),
			want:    hmacValid,
		},
		{
			name:    "sha1 hex",
			cfg:     &HMACConfig{Header: "X-Sig", Algorithm: "sha1"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", sign("sha1", "hex", secret, body)),
			want:    hmacValid,
		},
		{
			name:    "shopify sha256 base64",
			cfg:     &HMACConfig{Header: "X-Shopify-Hmac-Sha256", Encoding: "base64"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Shopify-Hmac-Sha256", sign("sha256", "base64", secret, body)),
			want:    hmacValid,
		},
		{
			name:    "default algorithm and encoding are sha256/hex",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacValid,
		},
		{
			name:    "prefix absent in header still validates (best-effort strip)",
			cfg:     &HMACConfig{Header: "X-Sig", Prefix: "sha256="},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacValid,
		},
		{
			name:    "missing header",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  secret,
			body:    body,
			headers: map[string]string{},
			want:    hmacMissing,
		},
		{
			name:    "tampered body",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  secret,
			body:    []byte(`{"hello":"tampered"}`),
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacInvalid,
		},
		{
			name:    "wrong secret",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  []byte("different"),
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacInvalid,
		},
		{
			name:    "undecodable hex",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", "nothex!!"),
			want:    hmacInvalid,
		},
		{
			name:    "empty secret",
			cfg:     &HMACConfig{Header: "X-Sig"},
			secret:  []byte{},
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacError,
		},
		{
			name:    "unknown algorithm",
			cfg:     &HMACConfig{Header: "X-Sig", Algorithm: "md5"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacError,
		},
		{
			name:    "unknown encoding",
			cfg:     &HMACConfig{Header: "X-Sig", Encoding: "ascii85"},
			secret:  secret,
			body:    body,
			headers: headersWith("X-Sig", sign("sha256", "hex", secret, body)),
			want:    hmacError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyHMAC(tt.cfg, tt.secret, tt.body, tt.headers)
			if got != tt.want {
				t.Errorf("verifyHMAC() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckHTTPHMAC exercises the exact decision the gateway gate makes:
// match rules for a path, resolve the secret, verify, and report (required, ok).
func TestCheckHTTPHMAC(t *testing.T) {
	p := newTestProcessor()
	secret := "topsecret"
	rules := []Rule{
		{
			Trigger: Trigger{HTTP: &HTTPTrigger{
				Path:   "/webhooks/gh",
				Method: "POST",
				HMAC:   &HMACConfig{Header: "X-Hub-Signature-256", Secret: secret, Prefix: "sha256="},
			}},
			Action: Action{NATS: &NATSAction{Subject: "gh.events", Payload: "{}"}},
		},
		{
			// A path with no hmac block — the gate must not require verification.
			Trigger: Trigger{HTTP: &HTTPTrigger{Path: "/plain", Method: "POST"}},
			Action:  Action{NATS: &NATSAction{Subject: "plain.events", Payload: "{}"}},
		},
	}
	if err := p.LoadRules(rules); err != nil {
		t.Fatalf("LoadRules failed: %v", err)
	}

	body := []byte(`{"event":"push"}`)
	validHeader := func() map[string]string {
		return headersWith("X-Hub-Signature-256", "sha256="+sign("sha256", "hex", []byte(secret), body))
	}

	tests := []struct {
		name         string
		path         string
		headers      map[string]string
		wantRequired bool
		wantOK       bool
	}{
		{name: "valid signature", path: "/webhooks/gh", headers: validHeader(), wantRequired: true, wantOK: true},
		{name: "tampered body", path: "/webhooks/gh", headers: headersWith("X-Hub-Signature-256", "sha256="+sign("sha256", "hex", []byte(secret), []byte("other"))), wantRequired: true, wantOK: false},
		{name: "missing header", path: "/webhooks/gh", headers: map[string]string{}, wantRequired: true, wantOK: false},
		{name: "path without hmac rule", path: "/plain", headers: map[string]string{}, wantRequired: false, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			required, ok := p.CheckHTTPHMAC(tt.path, "POST", body, tt.headers)
			if required != tt.wantRequired || ok != tt.wantOK {
				t.Errorf("CheckHTTPHMAC() = (required=%v, ok=%v), want (required=%v, ok=%v)",
					required, ok, tt.wantRequired, tt.wantOK)
			}
		})
	}
}

func TestValidateHMACConfig(t *testing.T) {
	l := newTestLoader()

	tests := []struct {
		name    string
		cfg     *HMACConfig
		wantErr bool
	}{
		{name: "nil is valid (no hmac block)", cfg: nil, wantErr: false},
		{name: "minimal valid", cfg: &HMACConfig{Header: "X-Sig", Secret: "s"}, wantErr: false},
		{name: "explicit valid algorithm/encoding", cfg: &HMACConfig{Header: "X-Sig", Secret: "s", Algorithm: "sha1", Encoding: "base64"}, wantErr: false},
		{name: "env-ref secret", cfg: &HMACConfig{Header: "X-Sig", Secret: "${WEBHOOK_SECRET}"}, wantErr: false},
		{name: "kv-ref secret", cfg: &HMACConfig{Header: "X-Sig", Secret: "{@kv.device_config.github}"}, wantErr: false},
		{name: "missing header", cfg: &HMACConfig{Secret: "s"}, wantErr: true},
		// Empty/unset secret is tolerated at load (env-refs resolve to "" when the
		// var is unset, incl. the browser tester); it fails closed at runtime.
		{name: "empty secret tolerated at load", cfg: &HMACConfig{Header: "X-Sig"}, wantErr: false},
		{name: "bad algorithm", cfg: &HMACConfig{Header: "X-Sig", Secret: "s", Algorithm: "md5"}, wantErr: true},
		{name: "bad encoding", cfg: &HMACConfig{Header: "X-Sig", Secret: "s", Encoding: "ascii85"}, wantErr: true},
		{name: "malformed kv-ref secret", cfg: &HMACConfig{Header: "X-Sig", Secret: "{@kv.nokey}"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := l.validateHMACConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHMACConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

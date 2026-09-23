package rule

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
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
			got := verifyHMAC(tt.cfg, tt.secret, tt.body, tt.headers, time.Now())
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
			Trigger: Trigger{HTTP: &HTTPTrigger{
				Path:   "/webhooks/stripe",
				Method: "POST",
				HMAC:   &HMACConfig{Scheme: schemeStripe, Secret: secret},
			}},
			Action: Action{NATS: &NATSAction{Subject: "stripe.events", Payload: "{}"}},
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
	// stripeHeader signs with a timestamp offset from the real clock, proving
	// CheckHTTPHMAC passes time.Now() and the scheme through to verifyHMAC.
	stripeHeader := func(offset time.Duration) map[string]string {
		ts := strconv.FormatInt(time.Now().Add(offset).Unix(), 10)
		sig := hex.EncodeToString(hmacSHA256([]byte(secret), ts+"."+string(body)))
		return map[string]string{"Stripe-Signature": "t=" + ts + ",v1=" + sig}
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
		{name: "stripe scheme valid", path: "/webhooks/stripe", headers: stripeHeader(0), wantRequired: true, wantOK: true},
		{name: "stripe scheme expired", path: "/webhooks/stripe", headers: stripeHeader(-10 * time.Minute), wantRequired: true, wantOK: false},
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
		{name: "stripe scheme with only a secret", cfg: &HMACConfig{Scheme: "stripe", Secret: "${STRIPE_WEBHOOK_SECRET}"}, wantErr: false},
		{name: "slack scheme", cfg: &HMACConfig{Scheme: "slack", Secret: "s"}, wantErr: false},
		{name: "standardwebhooks scheme with kv secret", cfg: &HMACConfig{Scheme: "standardwebhooks", Secret: "{@kv.secrets.svix}"}, wantErr: false},
		{name: "unknown scheme", cfg: &HMACConfig{Scheme: "github", Secret: "s"}, wantErr: true},
		{name: "scheme is case-sensitive", cfg: &HMACConfig{Scheme: "Stripe", Secret: "s"}, wantErr: true},
		{name: "scheme with header", cfg: &HMACConfig{Scheme: "stripe", Secret: "s", Header: "Stripe-Signature"}, wantErr: true},
		{name: "scheme with algorithm", cfg: &HMACConfig{Scheme: "slack", Secret: "s", Algorithm: "sha256"}, wantErr: true},
		{name: "scheme with encoding", cfg: &HMACConfig{Scheme: "slack", Secret: "s", Encoding: "hex"}, wantErr: true},
		{name: "scheme with prefix", cfg: &HMACConfig{Scheme: "stripe", Secret: "s", Prefix: "v1="}, wantErr: true},
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

// hmacSHA256 is the reference HMAC-SHA256 over a message, for building
// provider signatures in the timestamped-scheme tests.
func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func TestVerifyStripe(t *testing.T) {
	cfg := &HMACConfig{Scheme: schemeStripe}
	secret := []byte("whsec_test_secret")
	other := []byte("whsec_old_secret")
	body := []byte(`{"id":"evt_1","type":"payment_intent.succeeded"}`)
	now := time.Unix(1700000000, 0)

	// sig returns the hex v1 signature Stripe would send for timestamp ts.
	sig := func(key []byte, ts int64, body []byte) string {
		return hex.EncodeToString(hmacSHA256(key, strconv.FormatInt(ts, 10)+"."+string(body)))
	}
	header := func(v string) map[string]string {
		return map[string]string{"Stripe-Signature": v}
	}
	ts := now.Unix()
	tsStr := strconv.FormatInt(ts, 10)
	old := ts - 6*60
	future := ts + 6*60

	tests := []struct {
		name    string
		body    []byte
		headers map[string]string
		want    string
	}{
		{name: "valid", body: body, headers: header("t=" + tsStr + ",v1=" + sig(secret, ts, body)), want: hmacValid},
		{name: "valid with v0 test signature present", body: body, headers: header("t=" + tsStr + ",v1=" + sig(secret, ts, body) + ",v0=" + sig(other, ts, body)), want: hmacValid},
		{name: "secret rotation: second v1 matches", body: body, headers: header("t=" + tsStr + ",v1=" + sig(other, ts, body) + ",v1=" + sig(secret, ts, body)), want: hmacValid},
		{name: "only v0 present", body: body, headers: header("t=" + tsStr + ",v0=" + sig(secret, ts, body)), want: hmacInvalid},
		{name: "missing header", body: body, headers: map[string]string{}, want: hmacMissing},
		{name: "missing t", body: body, headers: header("v1=" + sig(secret, ts, body)), want: hmacInvalid},
		{name: "non-numeric t", body: body, headers: header("t=abc,v1=" + hex.EncodeToString(hmacSHA256(secret, "abc."+string(body)))), want: hmacInvalid},
		{name: "tampered body", body: []byte(`{"id":"evt_2"}`), headers: header("t=" + tsStr + ",v1=" + sig(secret, ts, body)), want: hmacInvalid},
		{name: "timestamp 6 min old", body: body, headers: header("t=" + strconv.FormatInt(old, 10) + ",v1=" + sig(secret, old, body)), want: hmacExpired},
		{name: "timestamp 6 min in future", body: body, headers: header("t=" + strconv.FormatInt(future, 10) + ",v1=" + sig(secret, future, body)), want: hmacExpired},
		{name: "old timestamp with bad signature is invalid, not expired", body: body, headers: header("t=" + strconv.FormatInt(old, 10) + ",v1=" + sig(other, old, body)), want: hmacInvalid},
		{name: "timestamp changed after signing", body: body, headers: header("t=" + strconv.FormatInt(ts+1, 10) + ",v1=" + sig(secret, ts, body)), want: hmacInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyHMAC(cfg, secret, tt.body, tt.headers, now); got != tt.want {
				t.Errorf("verifyHMAC() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifySlack(t *testing.T) {
	cfg := &HMACConfig{Scheme: schemeSlack}
	secret := []byte("slack_signing_secret")
	body := []byte("token=abc&command=%2Fdeploy&text=prod")
	now := time.Unix(1700000000, 0)

	headers := func(ts int64, sig string) map[string]string {
		return map[string]string{
			"X-Slack-Request-Timestamp": strconv.FormatInt(ts, 10),
			"X-Slack-Signature":         sig,
		}
	}
	sig := func(ts int64, body []byte) string {
		return "v0=" + hex.EncodeToString(hmacSHA256(secret, "v0:"+strconv.FormatInt(ts, 10)+":"+string(body)))
	}
	ts := now.Unix()
	old := ts - 6*60

	tests := []struct {
		name    string
		body    []byte
		headers map[string]string
		want    string
	}{
		{name: "valid", body: body, headers: headers(ts, sig(ts, body)), want: hmacValid},
		{name: "missing timestamp header", body: body, headers: map[string]string{"X-Slack-Signature": sig(ts, body)}, want: hmacMissing},
		{name: "missing signature header", body: body, headers: map[string]string{"X-Slack-Request-Timestamp": strconv.FormatInt(ts, 10)}, want: hmacMissing},
		{name: "wrong version prefix", body: body, headers: headers(ts, "v1="+strings.TrimPrefix(sig(ts, body), "v0=")), want: hmacInvalid},
		{name: "undecodable hex", body: body, headers: headers(ts, "v0=nothex!!"), want: hmacInvalid},
		{name: "old timestamp", body: body, headers: headers(old, sig(old, body)), want: hmacExpired},
		{name: "tampered body", body: []byte("token=abc&command=%2Fdeploy&text=staging"), headers: headers(ts, sig(ts, body)), want: hmacInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyHMAC(cfg, secret, tt.body, tt.headers, now); got != tt.want {
				t.Errorf("verifyHMAC() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVerifySlack_DocsExample pins the worked example from Slack's
// "Verifying requests from Slack" docs, copied verbatim.
func TestVerifySlack_DocsExample(t *testing.T) {
	secret := []byte("8f742231b10e8888abcd99yyyzzz85a5")
	body := []byte("token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c")
	headers := map[string]string{
		"X-Slack-Request-Timestamp": "1531420618",
		"X-Slack-Signature":         "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503",
	}
	now := time.Unix(1531420618, 0)

	if got := verifyHMAC(&HMACConfig{Scheme: schemeSlack}, secret, body, headers, now); got != hmacValid {
		t.Errorf("verifyHMAC() = %q, want %q", got, hmacValid)
	}
}

// TestVerifyStandardWebhooks_DocsExample pins the example from Svix's manual
// verification docs (Svix is the reference Standard Webhooks implementation).
func TestVerifyStandardWebhooks_DocsExample(t *testing.T) {
	secret := []byte("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	body := []byte(`{"test": 2432232314}`)
	headers := map[string]string{
		"Webhook-Id":        "msg_p5jXN8AQM9LWM0D4loKWxJek",
		"Webhook-Timestamp": "1614265330",
		"Webhook-Signature": "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=",
	}
	now := time.Unix(1614265330, 0)

	if got := verifyHMAC(&HMACConfig{Scheme: schemeStandardWebhooks}, secret, body, headers, now); got != hmacValid {
		t.Errorf("verifyHMAC() = %q, want %q", got, hmacValid)
	}
}

func TestVerifyStandardWebhooks(t *testing.T) {
	cfg := &HMACConfig{Scheme: schemeStandardWebhooks}
	key := []byte("0123456789abcdef0123456789abcdef")
	otherKey := []byte("fedcba9876543210fedcba9876543210")
	secret := []byte("whsec_" + base64.StdEncoding.EncodeToString(key))
	body := []byte(`{"type":"contact.created"}`)
	now := time.Unix(1700000000, 0)
	const id = "msg_2KWPBgLlAfxdpx2AI54pPJ85f4W"

	sig := func(k []byte, ts int64) string {
		return "v1," + base64.StdEncoding.EncodeToString(hmacSHA256(k, id+"."+strconv.FormatInt(ts, 10)+"."+string(body)))
	}
	headers := func(ts int64, sigs string) map[string]string {
		return map[string]string{
			"Webhook-Id":        id,
			"Webhook-Timestamp": strconv.FormatInt(ts, 10),
			"Webhook-Signature": sigs,
		}
	}
	ts := now.Unix()
	old := ts - 6*60

	tests := []struct {
		name    string
		secret  []byte
		headers map[string]string
		want    string
	}{
		{name: "valid", secret: secret, headers: headers(ts, sig(key, ts)), want: hmacValid},
		{name: "secret without whsec_ prefix", secret: []byte(base64.StdEncoding.EncodeToString(key)), headers: headers(ts, sig(key, ts)), want: hmacValid},
		{name: "multiple signatures, second matches", secret: secret, headers: headers(ts, sig(otherKey, ts)+" "+sig(key, ts)), want: hmacValid},
		{name: "non-v1 entries ignored", secret: secret, headers: headers(ts, "v1a,AAAA "+sig(key, ts)), want: hmacValid},
		{name: "only non-v1 entries", secret: secret, headers: headers(ts, "v1a,"+strings.TrimPrefix(sig(key, ts), "v1,")), want: hmacInvalid},
		{name: "no signature matches", secret: secret, headers: headers(ts, sig(otherKey, ts)), want: hmacInvalid},
		{name: "missing webhook-id", secret: secret, headers: map[string]string{"Webhook-Timestamp": strconv.FormatInt(ts, 10), "Webhook-Signature": sig(key, ts)}, want: hmacMissing},
		{name: "secret not base64", secret: []byte("whsec_not base64!"), headers: headers(ts, sig(key, ts)), want: hmacError},
		{name: "old timestamp", secret: secret, headers: headers(old, sig(key, old)), want: hmacExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyHMAC(cfg, tt.secret, body, tt.headers, now); got != tt.want {
				t.Errorf("verifyHMAC() = %q, want %q", got, tt.want)
			}
		})
	}
}

// file: internal/rule/signature_verify_test.go
// Pure-crypto unit tests for NKey signature verification — no NATS server required.

package rule

import (
	"encoding/base64"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

const (
	defaultPubHeader = "Nats-Public-Key"
	defaultSigHeader = "Nats-Signature"
)

// signPayload creates a fresh NKey user and returns its public key plus a
// base64-encoded signature over payload.
func signPayload(t *testing.T, payload []byte) (pubKey, sigB64 string) {
	t.Helper()
	user, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("nkeys.CreateUser: %v", err)
	}
	pubKey, err = user.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	sig, err := user.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return pubKey, base64.StdEncoding.EncodeToString(sig)
}

// newSigContext builds an EvaluationContext wired for signature verification.
func newSigContext(t *testing.T, payload []byte, headers map[string]string, sv *SignatureVerification, m *metrics.Metrics) *EvaluationContext {
	t.Helper()
	ctx, err := NewEvaluationContext(
		payload,
		headers,
		NewSubjectContext("test.subject"),
		nil, // httpCtx
		NewSystemTimeProvider().GetCurrentContext(),
		nil, // kvCtx
		sv,
		logger.NewNopLogger(),
	)
	if err != nil {
		t.Fatalf("NewEvaluationContext: %v", err)
	}
	ctx.Metrics = m
	return ctx
}

func TestVerifySignature(t *testing.T) {
	payload := []byte(`{"device":"sensor-1","reading":42}`)
	validPub, validSig := signPayload(t, payload)

	// A different valid key, used to prove a good signature under the wrong signer fails.
	wrongPub, _ := signPayload(t, payload)

	tests := []struct {
		name          string
		payload       []byte
		pubKeyHeader  string // config header name for the public key
		sigHeader     string // config header name for the signature
		headers       map[string]string
		enabled       bool
		wantValid     bool
		wantSigner    string // expected signerPublicKey ("" = not set)
		wantAttempted bool   // whether a verification outcome should be recorded in metrics
	}{
		{
			name:          "valid signature",
			payload:       payload,
			headers:       map[string]string{defaultPubHeader: validPub, defaultSigHeader: validSig},
			enabled:       true,
			wantValid:     true,
			wantSigner:    validPub,
			wantAttempted: true,
		},
		{
			name:          "tampered payload",
			payload:       []byte(`{"device":"sensor-1","reading":999}`), // differs from what was signed
			headers:       map[string]string{defaultPubHeader: validPub, defaultSigHeader: validSig},
			enabled:       true,
			wantValid:     false,
			wantSigner:    validPub,
			wantAttempted: true,
		},
		{
			name:          "valid signature under wrong signer",
			payload:       payload,
			headers:       map[string]string{defaultPubHeader: wrongPub, defaultSigHeader: validSig},
			enabled:       true,
			wantValid:     false,
			wantSigner:    wrongPub,
			wantAttempted: true,
		},
		{
			name:          "malformed base64 signature",
			payload:       payload,
			headers:       map[string]string{defaultPubHeader: validPub, defaultSigHeader: "not!valid!base64!"},
			enabled:       true,
			wantValid:     false,
			wantSigner:    validPub,
			wantAttempted: true,
		},
		{
			// Regression: a public key shorter than 16 chars previously panicked
			// on the truncated-key log line (pubKeyStr[:16]).
			name:          "short invalid public key does not panic",
			payload:       payload,
			headers:       map[string]string{defaultPubHeader: "abc", defaultSigHeader: validSig},
			enabled:       true,
			wantValid:     false,
			wantSigner:    "abc",
			wantAttempted: true,
		},
		{
			name:          "missing headers are skipped, not counted",
			payload:       payload,
			headers:       map[string]string{}, // neither header present
			enabled:       true,
			wantValid:     false,
			wantSigner:    "",
			wantAttempted: false,
		},
		{
			name:          "verification disabled",
			payload:       payload,
			headers:       map[string]string{defaultPubHeader: validPub, defaultSigHeader: validSig},
			enabled:       false,
			wantValid:     false,
			wantSigner:    "",
			wantAttempted: false,
		},
		{
			// NewEvaluationContext canonicalizes header keys, so lowercase keys still resolve.
			name:          "lowercase header keys still verify",
			payload:       payload,
			headers:       map[string]string{"nats-public-key": validPub, "nats-signature": validSig},
			enabled:       true,
			wantValid:     true,
			wantSigner:    validPub,
			wantAttempted: true,
		},
		{
			// Config-driven header names must be honored (the point where the CLI
			// tester's hardcoded defaults diverge from production).
			name:          "custom configured header names",
			payload:       payload,
			pubKeyHeader:  "X-Signer-Key",
			sigHeader:     "X-Signature",
			headers:       map[string]string{"X-Signer-Key": validPub, "X-Signature": validSig},
			enabled:       true,
			wantValid:     true,
			wantSigner:    validPub,
			wantAttempted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pubHeader := tt.pubKeyHeader
			if pubHeader == "" {
				pubHeader = defaultPubHeader
			}
			sigHeader := tt.sigHeader
			if sigHeader == "" {
				sigHeader = defaultSigHeader
			}

			m, err := metrics.NewMetrics(prometheus.NewRegistry())
			if err != nil {
				t.Fatalf("NewMetrics: %v", err)
			}

			sv := NewSignatureVerification(tt.enabled, pubHeader, sigHeader)
			ctx := newSigContext(t, tt.payload, tt.headers, sv, m)

			// Must not panic (see "short invalid public key" case).
			ctx.verifySignature()

			if ctx.sigValid != tt.wantValid {
				t.Errorf("sigValid = %v, want %v", ctx.sigValid, tt.wantValid)
			}
			if ctx.signerPublicKey != tt.wantSigner {
				t.Errorf("signerPublicKey = %q, want %q", ctx.signerPublicKey, tt.wantSigner)
			}

			// Metrics: an outcome is recorded only when both headers are present
			// AND verification is enabled (an attempt was actually made).
			valid := sigVerifyCount(t, m, "valid")
			invalid := sigVerifyCount(t, m, "invalid")
			total := valid + invalid
			if tt.wantAttempted {
				if total != 1 {
					t.Errorf("expected exactly one recorded verification, got valid=%v invalid=%v", valid, invalid)
				}
				wantResult := "invalid"
				if tt.wantValid {
					wantResult = "valid"
				}
				if got := sigVerifyCount(t, m, wantResult); got != 1 {
					t.Errorf("expected signature_verifications_total{result=%q}=1, got %v", wantResult, got)
				}
			} else if total != 0 {
				t.Errorf("expected no recorded verification for skipped path, got valid=%v invalid=%v", valid, invalid)
			}
		})
	}
}

// TestVerifySignatureIdempotent confirms lazy verification runs at most once.
func TestVerifySignatureIdempotent(t *testing.T) {
	payload := []byte(`{"x":1}`)
	pub, sig := signPayload(t, payload)

	m, err := metrics.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	sv := NewSignatureVerification(true, defaultPubHeader, defaultSigHeader)
	ctx := newSigContext(t, payload, map[string]string{defaultPubHeader: pub, defaultSigHeader: sig}, sv, m)

	ctx.verifySignature()
	ctx.verifySignature()
	ctx.verifySignature()

	if got := sigVerifyCount(t, m, "valid"); got != 1 {
		t.Errorf("verification recorded %v times, want 1 (should be memoized)", got)
	}
}

// sigVerifyCount reads signature_verifications_total{result=<result>} from the
// registry without reaching into the metrics package's unexported fields.
func sigVerifyCount(t *testing.T, m *metrics.Metrics, result string) float64 {
	t.Helper()
	mfs, err := m.GetRegistry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "signature_verifications_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == result {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

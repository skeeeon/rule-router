// file: internal/rule/signature.go

package rule

// SignatureVerification holds configuration for signature verification.
// This is a lightweight struct passed to EvaluationContext instead of the full config.
type SignatureVerification struct {
	Enabled         bool
	PublicKeyHeader string
	SignatureHeader string
}

// NewSignatureVerification creates a SignatureVerification from config values
func NewSignatureVerification(enabled bool, pubKeyHeader, sigHeader string) *SignatureVerification {
	return &SignatureVerification{
		Enabled:         enabled,
		PublicKeyHeader: pubKeyHeader,
		SignatureHeader: sigHeader,
	}
}

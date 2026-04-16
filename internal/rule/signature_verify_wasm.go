// file: internal/rule/signature_verify_wasm.go
// No-op signature verification for WASM builds (nkeys not available).

//go:build js && wasm

package rule

// verifySignature is a no-op in WASM builds.
// Signature verification requires nkeys which is excluded to reduce binary size.
func (c *EvaluationContext) verifySignature() {
	c.sigMu.Lock()
	defer c.sigMu.Unlock()
	c.sigChecked = true
}

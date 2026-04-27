// file: internal/rule/signature_verify.go
// NKey signature verification — requires nats-io/nkeys (excluded from WASM builds).

//go:build !js

package rule

import (
	"encoding/base64"
	"net/textproto"

	"github.com/nats-io/nkeys"
)

// verifySignature performs NKey signature verification with lazy evaluation.
// This method is thread-safe and only runs once per message, only when requested.
func (c *EvaluationContext) verifySignature() {
	c.sigMu.Lock()
	defer c.sigMu.Unlock()

	if c.sigChecked {
		return
	}
	c.sigChecked = true

	if c.sigVerification == nil || !c.sigVerification.Enabled {
		return
	}

	if c.Headers == nil {
		return
	}

	pubKeyStr := c.Headers[textproto.CanonicalMIMEHeaderKey(c.sigVerification.PublicKeyHeader)]
	sigBase64 := c.Headers[textproto.CanonicalMIMEHeaderKey(c.sigVerification.SignatureHeader)]

	if pubKeyStr == "" || sigBase64 == "" {
		if c.logger != nil {
			c.logger.Debug("signature verification skipped: missing headers",
				"pubKeyHeader", c.sigVerification.PublicKeyHeader,
				"sigHeader", c.sigVerification.SignatureHeader)
		}
		return
	}

	c.signerPublicKey = pubKeyStr

	sig, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		if c.logger != nil {
			c.logger.Debug("signature verification failed: invalid base64", "error", err)
		}
		return
	}

	user, err := nkeys.FromPublicKey(pubKeyStr)
	if err != nil {
		if c.logger != nil {
			c.logger.Debug("signature verification failed: invalid public key",
				"pubkey", pubKeyStr[:16]+"...", "error", err)
		}
		return
	}

	if err := user.Verify(c.RawPayload, sig); err == nil {
		c.sigValid = true
		if c.logger != nil {
			var contextType string
			if c.Subject != nil {
				contextType = "nats"
			} else if c.HTTP != nil {
				contextType = "http"
			}
			c.logger.Info("signature verified successfully",
				"pubkey", pubKeyStr[:16]+"...", "contextType", contextType)
		}
	} else {
		if c.logger != nil {
			c.logger.Warn("signature verification failed",
				"pubkey", pubKeyStr[:16]+"...", "error", err.Error())
		}
	}
}

// file: internal/rule/context.go

package rule

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	json "github.com/goccy/go-json"
	"github.com/nats-io/nkeys"
	"rule-router/internal/logger"
)

// EvaluationContext holds all data for a single message evaluation.
// It is created once per message and is treated as immutable.
type EvaluationContext struct {
	Msg        map[string]interface{}
	RawPayload []byte
	Headers    map[string]string
	Subject    *SubjectContext
	Time       *TimeContext
	KV         *KVContext
	traverser  *JSONPathTraverser

	// Signature verification (lazy evaluation)
	sigVerification *SignatureVerification
	logger          *logger.Logger
	sigMu           sync.Mutex
	sigChecked      bool
	sigValid        bool
	signerPublicKey string
}

// NewEvaluationContext creates a new context for a message.
func NewEvaluationContext(
	payload []byte,
	headers map[string]string,
	subject *SubjectContext,
	time *TimeContext,
	kv *KVContext,
	sigVerification *SignatureVerification,
	logger *logger.Logger,
) (*EvaluationContext, error) {
	var msg map[string]interface{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}
	}

	return &EvaluationContext{
		Msg:             msg,
		RawPayload:      payload,
		Headers:         headers,
		Subject:         subject,
		Time:            time,
		KV:              kv,
		traverser:       NewJSONPathTraverser(),
		sigVerification: sigVerification,
		logger:          logger,
	}, nil
}

// ResolveValue is the central point for variable lookup.
// It understands all prefixes like {@subject.}, {@kv.}, {@header.}, {@signature.}, and {msg.field}.
func (c *EvaluationContext) ResolveValue(path string) (interface{}, bool) {
	if strings.HasPrefix(path, "@") {
		// System variable
		if strings.HasPrefix(path, "@subject") {
			return c.Subject.GetField(path)
		}
		if strings.HasPrefix(path, "@time") || strings.HasPrefix(path, "@date") || strings.HasPrefix(path, "@timestamp") {
			return c.Time.GetField(path)
		}
		if strings.HasPrefix(path, "@kv") && c.KV != nil {
			return c.KV.GetFieldWithContext(path, c.Msg, c.Time, c.Subject)
		}
		if strings.HasPrefix(path, "@header.") {
			if c.Headers == nil {
				return nil, false
			}
			headerName := path[8:] // remove "@header."
			val, exists := c.Headers[headerName]
			return val, exists
		}
		// NEW: Signature fields
		if strings.HasPrefix(path, "@signature.") {
			c.verifySignature() // Lazy verification
			switch path {
			case "@signature.valid":
				return c.sigValid, true
			case "@signature.pubkey":
				if c.signerPublicKey != "" {
					return c.signerPublicKey, true
				}
				return nil, false
			}
			return nil, false
		}
		return nil, false
	}

	// Message field
	value, err := c.traverser.TraversePathString(c.Msg, path)
	if err != nil {
		return nil, false
	}
	return value, true
}

// verifySignature performs NKey signature verification with lazy evaluation.
// This method is thread-safe and only runs once per message, only when requested.
func (c *EvaluationContext) verifySignature() {
	c.sigMu.Lock()
	defer c.sigMu.Unlock()

	// Already checked - return immediately
	if c.sigChecked {
		return
	}
	c.sigChecked = true

	// Feature not enabled
	if c.sigVerification == nil || !c.sigVerification.Enabled {
		return
	}

	// No headers present
	if c.Headers == nil {
		return
	}

	// Extract public key and signature from headers
	pubKeyStr := c.Headers[c.sigVerification.PublicKeyHeader]
	sigBase64 := c.Headers[c.sigVerification.SignatureHeader]

	if pubKeyStr == "" || sigBase64 == "" {
		if c.logger != nil {
			c.logger.Debug("signature verification skipped: missing headers",
				"pubKeyHeader", c.sigVerification.PublicKeyHeader,
				"sigHeader", c.sigVerification.SignatureHeader)
		}
		return
	}

	// Store the public key for @signature.pubkey access
	c.signerPublicKey = pubKeyStr

	// Decode the Base64 signature
	sig, err := base64.StdEncoding.DecodeString(sigBase64)
	if err != nil {
		if c.logger != nil {
			c.logger.Debug("signature verification failed: invalid base64",
				"error", err)
		}
		return
	}

	// Create a public-key-only user from the header
	user, err := nkeys.FromPublicKey(pubKeyStr)
	if err != nil {
		if c.logger != nil {
			c.logger.Debug("signature verification failed: invalid public key",
				"pubkey", pubKeyStr[:16]+"...",
				"error", err)
		}
		return
	}

	// Verify the signature against the raw message payload
	if err := user.Verify(c.RawPayload, sig); err == nil {
		c.sigValid = true
		if c.logger != nil {
			c.logger.Info("signature verified successfully",
				"pubkey", pubKeyStr[:16]+"...",
				"subject", c.Subject.Full)
		}
	} else {
		if c.logger != nil {
			c.logger.Warn("signature verification failed",
				"pubkey", pubKeyStr[:16]+"...",
				"subject", c.Subject.Full,
				"error", err.Error())
		}
	}
}

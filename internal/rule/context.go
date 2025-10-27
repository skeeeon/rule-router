// file: internal/rule/context.go

package rule

import (
	json "github.com/goccy/go-json"
	"encoding/base64"
	"strings"
	"sync"

	"github.com/nats-io/nkeys"
	"rule-router/internal/logger"
)

// EvaluationContext provides all data needed for condition evaluation and template processing
// Supports both NATS and HTTP contexts, and now includes support for forEach array iteration
type EvaluationContext struct {
	// Message data
	Msg        map[string]interface{} // CURRENT context (root message OR array element during forEach)
	RawPayload []byte
	Headers    map[string]string

	// NEW: Original message reference for @msg prefix
	// ALWAYS points to root message, even when Msg points to array element
	OriginalMsg map[string]interface{}

	// Context (NATS or HTTP, one will be nil)
	Subject *SubjectContext
	HTTP    *HTTPRequestContext

	// Shared contexts
	Time      *TimeContext
	KV        *KVContext
	traverser *JSONPathTraverser

	// Signature verification (lazy evaluation)
	sigVerification *SignatureVerification
	sigMu           sync.Mutex
	sigChecked      bool
	sigValid        bool
	signerPublicKey string
	logger          *logger.Logger
}

// NewEvaluationContext creates a new evaluation context
// Either subjectCtx OR httpCtx should be provided (not both)
func NewEvaluationContext(
	payload []byte,
	headers map[string]string,
	subjectCtx *SubjectContext,
	httpCtx *HTTPRequestContext,
	timeCtx *TimeContext,
	kvCtx *KVContext,
	sigVerification *SignatureVerification,
	logger *logger.Logger,
) (*EvaluationContext, error) {
	var msgData map[string]interface{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &msgData); err != nil {
			return nil, err
		}
	} else {
		msgData = make(map[string]interface{})
	}

	ctx := &EvaluationContext{
		Msg:             msgData,
		RawPayload:      payload,
		Headers:         headers,
		Subject:         subjectCtx,
		HTTP:            httpCtx,
		Time:            timeCtx,
		KV:              kvCtx,
		traverser:       NewJSONPathTraverser(),
		sigVerification: sigVerification,
		logger:          logger,
	}
	
	// NEW: Initialize OriginalMsg to point to the same message data
	// This will remain constant even if Msg is changed during forEach iteration
	ctx.OriginalMsg = msgData
	
	return ctx, nil
}

// ResolveValue resolves a field value from the context
// Supports message fields, system fields (@subject, @path, @header, @time, @kv, @signature)
// NEW: Also supports @msg prefix for explicit root message access during forEach
func (c *EvaluationContext) ResolveValue(path string) (interface{}, bool) {
	// System fields start with @
	if strings.HasPrefix(path, "@") {
		return c.resolveSystemField(path)
	}

	// Message field - traverse JSON using current context (Msg)
	// During forEach, this will be the array element
	// Outside forEach, this is the same as OriginalMsg
	value, err := c.traverser.TraversePathString(c.Msg, path)
	if err != nil {
		return nil, false
	}
	return value, true
}

// resolveSystemField handles all @ prefixed system fields
// NEW: Includes @msg.* prefix for explicit root message access
func (c *EvaluationContext) resolveSystemField(path string) (interface{}, bool) {
	// NEW: @msg prefix - explicitly access root message
	// This is critical during forEach to access fields outside the current array element
	if strings.HasPrefix(path, "@msg.") {
		fieldPath := path[5:] // Remove "@msg."
		value, err := c.traverser.TraversePathString(c.OriginalMsg, fieldPath)
		if err != nil {
			return nil, false
		}
		return value, true
	}

	// Subject fields (NATS context)
	if strings.HasPrefix(path, "@subject") {
		if c.Subject != nil {
			return c.Subject.GetField(path)
		}
		return nil, false
	}

	// HTTP path fields (HTTP context)
	if strings.HasPrefix(path, "@path") {
		if c.HTTP != nil {
			return c.HTTP.GetField(path)
		}
		return nil, false
	}

	// HTTP method field (HTTP context)
	if path == "@method" {
		if c.HTTP != nil {
			return c.HTTP.Method, true
		}
		return nil, false
	}

	// Header fields (both contexts)
	if strings.HasPrefix(path, "@header.") {
		headerName := path[8:] // Remove "@header."
		if c.Headers != nil {
			if value, ok := c.Headers[headerName]; ok {
				return value, true
			}
		}
		return nil, false
	}

	// Time fields (both contexts)
	if strings.HasPrefix(path, "@time") || strings.HasPrefix(path, "@day") || strings.HasPrefix(path, "@date") {
		if c.Time != nil {
			return c.Time.GetField(path)
		}
		return nil, false
	}

	// KV fields (both contexts)
	if strings.HasPrefix(path, "@kv.") {
		if c.KV != nil {
			return c.KV.GetFieldWithContext(path, c.Msg, c.Time, c.Subject)
		}
		return nil, false
	}

	// Signature fields (both contexts)
	if strings.HasPrefix(path, "@signature.") {
		if c.sigVerification != nil && c.sigVerification.Enabled {
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

	return nil, false
}

// verifySignature performs NKey signature verification with lazy evaluation
// This method is thread-safe and only runs once per message, only when requested
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
			var contextType string
			if c.Subject != nil {
				contextType = "nats"
			} else if c.HTTP != nil {
				contextType = "http"
			}
			c.logger.Info("signature verified successfully",
				"pubkey", pubKeyStr[:16]+"...",
				"contextType", contextType)
		}
	} else {
		if c.logger != nil {
			c.logger.Warn("signature verification failed",
				"pubkey", pubKeyStr[:16]+"...",
				"error", err.Error())
		}
	}
}

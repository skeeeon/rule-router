// file: internal/rule/context.go

package rule

import (
	"encoding/base64"
	"strings"
	"sync"
	"unicode/utf8"

	json "github.com/goccy/go-json"
	"github.com/nats-io/nkeys"

	"rule-router/internal/logger"
)

// System field prefixes for @ variables
const (
	prefixMsg       = "@msg."
	prefixHeader    = "@header."
	prefixKV        = "@kv."
	prefixSignature = "@signature."
)

// wrapIfNeeded wraps primitives and arrays to ensure root message is always an object.
// Objects are passed through unchanged for backward compatibility.
//
// Wrapping rules:
//   - Objects: {"field": ...} → pass through unchanged
//   - Arrays: [...] → {"@items": [...]}
//   - Primitives: "text", 42, true, null → {"@value": <primitive>}
//
// This enables rules to work with:
//   - SenML arrays at root
//   - Simple string/number messages
//   - Primitive array elements
func wrapIfNeeded(raw interface{}) map[string]interface{} {
	switch v := raw.(type) {
	case map[string]interface{}:
		// Already an object - pass through unchanged
		return v

	case []interface{}:
		// Array at root - wrap in @items
		return map[string]interface{}{"@items": v}

	case nil:
		// null value - wrap in @value
		return map[string]interface{}{"@value": nil}

	default:
		// Primitives: string, float64, bool
		// Wrap in @value for consistent access
		return map[string]interface{}{"@value": v}
	}
}

// EvaluationContext provides all data needed for condition evaluation and template processing
// Supports both NATS and HTTP contexts, and now includes support for forEach array iteration
type EvaluationContext struct {
	// Message data
	Msg        map[string]interface{} // CURRENT context (root message OR array element during forEach)
	RawPayload []byte
	Headers    map[string]string

	// Original message reference for @msg prefix
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
	// Parse payload as generic interface to handle all JSON types
	var raw interface{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &raw); err != nil {
			// JSON parsing failed - check if it's valid UTF-8 text
			if utf8.Valid(payload) {
				// Treat entire payload as a raw string
				raw = string(payload)
				logger.Debug("non-JSON payload detected, treating as raw string",
					"payloadSize", len(payload),
					"preview", truncateString(string(payload), 50))
			} else {
				// Not valid UTF-8 - cannot process as text
				return nil, err
			}
		}
	}

	// Wrap if needed to ensure msgData is always an object
	msgData := wrapIfNeeded(raw)

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
	
	// IMPORTANT: OriginalMsg should point to wrapped version too
	ctx.OriginalMsg = msgData
	
	return ctx, nil
}

// truncateString truncates a string to maxLen characters for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// WithElement creates a child context for processing an array element.
// The new context has Msg set to the element while preserving OriginalMsg
// for @msg access to the root message. All other fields are inherited.
func (c *EvaluationContext) WithElement(element map[string]interface{}) *EvaluationContext {
	return &EvaluationContext{
		Msg:             element,
		OriginalMsg:     c.OriginalMsg, // Preserve root for @msg access
		RawPayload:      c.RawPayload,
		Headers:         c.Headers,
		Subject:         c.Subject,
		HTTP:            c.HTTP,
		Time:            c.Time,
		KV:              c.KV,
		traverser:       c.traverser,
		sigVerification: c.sigVerification,
		sigChecked:      c.sigChecked,
		sigValid:        c.sigValid,
		signerPublicKey: c.signerPublicKey,
		logger:          c.logger,
	}
}

// ResolveValue resolves a field value from the context
// Supports message fields, system fields (@subject, @path, @header, @time, @kv, @signature)
// Also supports @msg prefix for explicit root message access during forEach
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
// Includes @msg.* prefix for explicit root message access
// Includes fallback for wrapped fields (@value, @items)
func (c *EvaluationContext) resolveSystemField(path string) (interface{}, bool) {
	// @msg prefix - explicitly access root message
	// This is critical during forEach to access fields outside the current array element
	if strings.HasPrefix(path, prefixMsg) {
		fieldPath := path[len(prefixMsg):]
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
	if strings.HasPrefix(path, prefixHeader) {
		headerName := path[len(prefixHeader):]
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
	if strings.HasPrefix(path, prefixKV) {
		if c.KV != nil {
			return c.KV.GetFieldWithContext(path, c.Msg, c.Time, c.Subject)
		}
		return nil, false
	}

	// Signature fields (both contexts)
	if strings.HasPrefix(path, prefixSignature) {
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

	// Fallback for wrapped field names (@value, @items)
	// These exist in the message itself after wrapIfNeeded()
	// This enables templates like {@value} and {@items.0} to work
	value, err := c.traverser.TraversePathString(c.Msg, path)
	if err != nil {
		c.logger.Debug("system field not recognized and not found in message",
			"field", path)
		return nil, false
	}
	
	c.logger.Debug("resolved wrapped system field from message",
		"field", path,
		"valueType", valueType(value))
	
	return value, true
}

// valueType returns a human-readable type description for logging
func valueType(v interface{}) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "unknown"
	}
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


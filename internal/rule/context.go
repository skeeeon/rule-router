package rule

import (
	"bytes"
	"fmt"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	json "github.com/goccy/go-json"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// System field prefixes for @ variables
const (
	prefixMsg       = "@msg."
	prefixHeader    = "@header."
	prefixKV        = "@kv."
	prefixSignature = "@signature."
)

// contentTypeForm is the media type of a URL-encoded form body.
//
// This is the only body format besides JSON that the engine decodes, and the
// bar for adding a third is deliberately high: a decoder belongs here only if
// it needs no configuration and yields the map[string]any shape the engine
// already uses. Form encoding clears that bar with url.ParseQuery and nothing
// else. XML (element/attribute/namespace ambiguity), multipart (files are not
// fields), CSV (header and delimiter policy), and Protobuf/Avro (schema
// registry) all fail it — each would drag configuration or a subsystem into
// the evaluation path. Decode formats that outlive any one vendor; never
// decode a vendor's schema.
const contentTypeForm = "application/x-www-form-urlencoded"

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
func wrapIfNeeded(raw any) map[string]any {
	switch v := raw.(type) {
	case map[string]any:
		// Already an object - pass through unchanged
		return v

	case []any:
		// Array at root - wrap in @items
		return map[string]any{"@items": v}

	case nil:
		// null value - wrap in @value
		return map[string]any{"@value": nil}

	default:
		// Primitives: string, float64, bool
		// Wrap in @value for consistent access
		return map[string]any{"@value": v}
	}
}

// EvaluationContext provides all data needed for condition evaluation and template processing
// Supports both NATS and HTTP contexts, and now includes support for forEach array iteration
type EvaluationContext struct {
	// Message data
	Msg        map[string]any // CURRENT context (root message OR array element during forEach)
	RawPayload []byte
	Headers    map[string]string

	// Original message reference for @msg prefix
	// ALWAYS points to root message, even when Msg points to array element
	OriginalMsg map[string]any

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

	// Metrics sink for signature-verification instrumentation. Optional:
	// nil in tests/CLI (no-op). Set by the Processor at evaluation time.
	Metrics *metrics.Metrics
}

// isFormContentType reports whether ct names a URL-encoded form body, ignoring
// any media type parameters such as "; charset=UTF-8".
func isFormContentType(ct string) bool {
	mediaType, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), contentTypeForm)
}

// decodeForm parses an application/x-www-form-urlencoded body into a message
// object.
//
// Every value stays a string. Form encoding carries no types, and inferring
// them corrupts data — a PIN of "007" would become 7. Nothing downstream needs
// the inference: Evaluator.toFloat parses strings for the numeric operators and
// compareValues stringifies the other side for eq, so conditions written
// against a form field behave the same as against a JSON one.
//
// A key repeated in the body becomes an array, so {tag.0} and forEach traverse
// it exactly like a JSON array. This differs from a flat @-namespace such as
// @header, which has no traversal behind it and keeps only the first value.
func decodeForm(payload []byte) (map[string]any, error) {
	values, err := url.ParseQuery(string(payload))
	if err != nil {
		return nil, fmt.Errorf("parsing form body: %w", err)
	}

	msg := make(map[string]any, len(values))
	for k, v := range values {
		switch len(v) {
		case 0:
			msg[k] = ""
		case 1:
			msg[k] = v[0]
		default:
			items := make([]any, len(v))
			for i, s := range v {
				items[i] = s
			}
			msg[k] = items
		}
	}
	return msg, nil
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
	// Canonicalize header keys at the rule-engine boundary so lookups are case-insensitive
	// regardless of how the caller constructed the map (extraction sites, tests, WASM).
	// This runs before the payload decode, which reads Content-Type — a caller
	// spelling it "content-type" must select the same decoder.
	if len(headers) > 0 {
		canonical := make(map[string]string, len(headers))
		for k, v := range headers {
			canonical[textproto.CanonicalMIMEHeaderKey(k)] = v
		}
		headers = canonical
	}

	var msgData map[string]any
	switch {
	case len(payload) == 0:
		msgData = wrapIfNeeded(nil)

	case isFormContentType(headers["Content-Type"]):
		// Fail closed. url.ParseQuery returns the pairs it managed to read
		// alongside its error, and evaluating a rule against a silently
		// truncated field set is worse than rejecting the message: a dropped
		// field reads as absent, which can flip a condition rather than raise
		// one. A body without the header still takes the JSON path below, so
		// this decoder is opt-in by the sender.
		decoded, err := decodeForm(payload)
		if err != nil {
			logger.Error("failed to decode form payload",
				"error", err,
				"payloadSize", len(payload))
			return nil, err
		}
		msgData = decoded

	default:
		// Parse payload as generic interface to handle all JSON types.
		// UseNumber() preserves numeric precision by decoding numbers as json.Number
		// instead of float64, preventing silent data corruption on large integers.
		var raw any
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
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

		// Wrap if needed to ensure msgData is always an object
		msgData = wrapIfNeeded(raw)
	}

	ctx := &EvaluationContext{
		Msg:             msgData,
		RawPayload:      payload,
		Headers:         headers,
		Subject:         subjectCtx,
		HTTP:            httpCtx,
		Time:            timeCtx,
		KV:              kvCtx,
		traverser:       defaultTraverser,
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
func (c *EvaluationContext) WithElement(element map[string]any) *EvaluationContext {
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
		Metrics:         c.Metrics,
	}
}

// ResolveValue resolves a field value from the context
// Supports message fields, system fields (@subject, @path, @header, @time, @kv, @signature)
// Also supports @msg prefix for explicit root message access during forEach
func (c *EvaluationContext) ResolveValue(path string) (any, bool) {
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
func (c *EvaluationContext) resolveSystemField(path string) (any, bool) {
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
			return c.Subject.Field(path)
		}
		return nil, false
	}

	// HTTP path fields (HTTP context)
	if strings.HasPrefix(path, "@path") {
		if c.HTTP != nil {
			return c.HTTP.Field(path)
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

	// Header fields (both contexts) — case-insensitive per HTTP/MIME conventions.
	// Headers are stored canonicalized at the extraction site; canonicalize the
	// requested name here so templates work regardless of how authors spell them.
	if strings.HasPrefix(path, prefixHeader) {
		headerName := textproto.CanonicalMIMEHeaderKey(path[len(prefixHeader):])
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
			return c.Time.Field(path)
		}
		return nil, false
	}

	// KV fields (both contexts)
	if strings.HasPrefix(path, prefixKV) {
		if c.KV != nil {
			return c.KV.FieldWithContext(path, c.Msg, c.Time, c.Subject)
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
func valueType(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case string:
		return "string"
	case json.Number, float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

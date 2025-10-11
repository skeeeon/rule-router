// file: internal/rule/context.go

package rule

import (
	"fmt"
	"strings"

	json "github.com/goccy/go-json"
)

// EvaluationContext holds all data for a single message evaluation.
// It is created once per message and is treated as immutable.
type EvaluationContext struct {
	Msg        map[string]interface{}
	RawPayload []byte
	Headers    map[string]string // For the new header feature
	Subject    *SubjectContext
	Time       *TimeContext
	KV         *KVContext
	traverser  *JSONPathTraverser
}

// NewEvaluationContext creates a new context for a message.
func NewEvaluationContext(
	payload []byte,
	headers map[string]string,
	subject *SubjectContext,
	time *TimeContext,
	kv *KVContext,
) (*EvaluationContext, error) {
	var msg map[string]interface{}
	// Only unmarshal if there's a payload; passthrough actions might have an empty one.
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}
	}

	return &EvaluationContext{
		Msg:        msg,
		RawPayload: payload,
		Headers:    headers,
		Subject:    subject,
		Time:       time,
		KV:         kv,
		traverser:  NewJSONPathTraverser(),
	}, nil
}

// ResolveValue is the new central point for variable lookup.
// It understands all prefixes like {@subject.}, {@kv.}, {@header.}, and {msg.field}.
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
			// Pass the necessary contexts for nested variable resolution within the KV field.
			return c.KV.GetFieldWithContext(path, c.Msg, c.Time, c.Subject)
		}
		// NEW: Header support
		if strings.HasPrefix(path, "@header.") {
			if c.Headers == nil {
				return nil, false
			}
			headerName := path[8:] // remove "@header."
			val, exists := c.Headers[headerName]
			return val, exists
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

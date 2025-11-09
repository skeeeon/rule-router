// file: internal/rule/types.go

package rule

import (
	"strconv"
	"strings"
)

// Rule represents a generic rule with trigger and action
type Rule struct {
	Trigger    Trigger     `json:"trigger" yaml:"trigger"`
	Conditions *Conditions `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	Action     Action      `json:"action" yaml:"action"`
}

// Trigger defines what initiates rule evaluation (NATS or HTTP)
type Trigger struct {
	NATS *NATSTrigger `json:"nats,omitempty" yaml:"nats,omitempty"`
	HTTP *HTTPTrigger `json:"http,omitempty" yaml:"http,omitempty"`
}

// NATSTrigger represents a NATS subject-based trigger
type NATSTrigger struct {
	Subject string `json:"subject" yaml:"subject"`
}

// HTTPTrigger represents an HTTP endpoint-based trigger
type HTTPTrigger struct {
	Path   string `json:"path" yaml:"path"`
	Method string `json:"method,omitempty" yaml:"method,omitempty"` // Optional, defaults to all methods
}

// Action defines what happens when rule matches (NATS or HTTP)
type Action struct {
	NATS *NATSAction `json:"nats,omitempty" yaml:"nats,omitempty"`
	HTTP *HTTPAction `json:"http,omitempty" yaml:"http,omitempty"`
}

// NATSAction represents publishing to a NATS subject
type NATSAction struct {
	Subject     string            `json:"subject" yaml:"subject"`
	Payload     string            `json:"payload,omitempty" yaml:"payload,omitempty"`
	Passthrough bool              `json:"passthrough,omitempty" yaml:"passthrough,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	RawPayload  []byte            `json:"-" yaml:"-"` // Populated during processing
	
	// Array iteration fields for forEach functionality
	// ForEach must use template syntax: "{arrayField}" or "{nested.array}" or "{@items}"
	ForEach string      `json:"forEach,omitempty" yaml:"forEach,omitempty"`
	Filter  *Conditions `json:"filter,omitempty" yaml:"filter,omitempty"`
}

// HTTPAction represents making an HTTP request
type HTTPAction struct {
	URL         string            `json:"url" yaml:"url"`
	Method      string            `json:"method" yaml:"method"`
	Payload     string            `json:"payload,omitempty" yaml:"payload,omitempty"`
	Passthrough bool              `json:"passthrough,omitempty" yaml:"passthrough,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	RawPayload  []byte            `json:"-" yaml:"-"` // Populated during processing
	Retry       *RetryConfig      `json:"retry,omitempty" yaml:"retry,omitempty"`
	
	// Array iteration fields for forEach functionality
	// ForEach must use template syntax: "{arrayField}" or "{nested.array}" or "{@items}"
	ForEach string      `json:"forEach,omitempty" yaml:"forEach,omitempty"`
	Filter  *Conditions `json:"filter,omitempty" yaml:"filter,omitempty"`
}

// RetryConfig defines retry behavior for HTTP actions
type RetryConfig struct {
	MaxAttempts  int    `json:"maxAttempts" yaml:"maxAttempts"`
	InitialDelay string `json:"initialDelay" yaml:"initialDelay"` // e.g., "1s"
	MaxDelay     string `json:"maxDelay" yaml:"maxDelay"`         // e.g., "30s"
}

// Conditions represents a group of conditions with a logical operator
type Conditions struct {
	Operator string       `json:"operator" yaml:"operator"` // "and" or "or"
	Items    []Condition  `json:"items" yaml:"items"`
	Groups   []Conditions `json:"groups,omitempty" yaml:"groups,omitempty"` // For nested condition groups
}

// Condition represents a single evaluation criterion with template-based variable resolution
//
// Both Field and Value now support template syntax for dynamic comparisons:
//
// Field Examples:
//   - Message field: "{temperature}"
//   - Nested field: "{sensor.reading.value}"
//   - System time: "{@time.hour}"
//   - Subject token: "{@subject.1}"
//   - KV lookup: "{@kv.sensor_config.{sensor_id}:max_temp}"
//
// Value Examples:
//   - Literal: 30
//   - Literal string: "active"
//   - Message field: "{threshold}"
//   - KV lookup: "{@kv.config.global:max_temperature}"
//   - System variable: "{@time.hour}"
//
// This enables powerful variable-to-variable comparisons:
//   field: "{temperature}"
//   operator: gt
//   value: "{@kv.sensor_config.{sensor_id}:max_temp}"
//
// Type preservation:
//   - Numbers remain numbers for accurate numeric comparison
//   - Strings remain strings
//   - Booleans remain booleans
//   - Type coercion is performed automatically when needed
type Condition struct {
	Field    string      `json:"field" yaml:"field"`       // Template: "{temperature}" or "{@time.hour}"
	Operator string      `json:"operator" yaml:"operator"` // eq, gt, contains, etc.
	Value    interface{} `json:"value,omitempty" yaml:"value,omitempty"` // Literal or template: 30 or "{@kv.config:max_temp}"
	
	// For array operators (any/all/none) - nested conditions to evaluate against array elements
	Conditions *Conditions `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// SubjectContext provides access to NATS subject information for templates and conditions
type SubjectContext struct {
	Full   string   `json:"full"`   // Full subject: "sensors.temperature.room1"
	Tokens []string `json:"tokens"` // Split tokens: ["sensors", "temperature", "room1"]
	Count  int      `json:"count"`  // Token count: 3
}

// NewSubjectContext creates a SubjectContext from a NATS subject string
func NewSubjectContext(subject string) *SubjectContext {
	if subject == "" {
		return &SubjectContext{
			Full:   "",
			Tokens: []string{},
			Count:  0,
		}
	}

	tokens := strings.Split(subject, ".")
	return &SubjectContext{
		Full:   subject,
		Tokens: tokens,
		Count:  len(tokens),
	}
}

// GetToken safely retrieves a token by index, returns empty string if out of bounds
func (sc *SubjectContext) GetToken(index int) string {
	if index < 0 || index >= len(sc.Tokens) {
		return ""
	}
	return sc.Tokens[index]
}

// GetField retrieves a subject field for template/condition processing
// Supports: "@subject", "@subject.0", "@subject.1", "@subject.count"
func (sc *SubjectContext) GetField(fieldName string) (interface{}, bool) {
	switch fieldName {
	case "@subject":
		return sc.Full, true
	case "@subject.count":
		return sc.Count, true
	default:
		// Check for indexed access: @subject.0, @subject.1, etc.
		if strings.HasPrefix(fieldName, "@subject.") {
			indexStr := fieldName[9:] // Remove "@subject."
			if index, err := strconv.Atoi(indexStr); err == nil {
				if token := sc.GetToken(index); token != "" {
					return token, true
				}
			}
		}
	}
	return nil, false
}

// HTTPRequestContext provides HTTP-specific context for rule evaluation
type HTTPRequestContext struct {
	Path       string   // Full path: "/webhooks/github/pr"
	PathTokens []string // Tokens: ["webhooks", "github", "pr"]
	Method     string   // HTTP method: "POST", "GET", etc.
	Count      int      // Token count
}

// NewHTTPRequestContext creates an HTTPRequestContext from path and method
func NewHTTPRequestContext(path, method string) *HTTPRequestContext {
	tokens := strings.Split(strings.Trim(path, "/"), "/")
	
	// Handle root path
	if len(tokens) == 1 && tokens[0] == "" {
		tokens = []string{}
	}
	
	return &HTTPRequestContext{
		Path:       path,
		PathTokens: tokens,
		Method:     method,
		Count:      len(tokens),
	}
}

// GetToken safely retrieves a path token by index
func (hc *HTTPRequestContext) GetToken(index int) string {
	if index < 0 || index >= len(hc.PathTokens) {
		return ""
	}
	return hc.PathTokens[index]
}

// GetField retrieves an HTTP field for template/condition processing
// Supports: "@path", "@path.0", "@path.1", "@path.count", "@method"
func (hc *HTTPRequestContext) GetField(fieldName string) (interface{}, bool) {
	switch fieldName {
	case "@path":
		return hc.Path, true
	case "@path.count":
		return hc.Count, true
	case "@method":
		return hc.Method, true
	default:
		// Check for indexed access: @path.0, @path.1, etc.
		if strings.HasPrefix(fieldName, "@path.") {
			indexStr := fieldName[6:] // Remove "@path."
			if index, err := strconv.Atoi(indexStr); err == nil {
				if token := hc.GetToken(index); token != "" {
					return token, true
				}
			}
		}
	}
	return nil, false
}

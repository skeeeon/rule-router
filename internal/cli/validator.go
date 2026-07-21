// file: internal/cli/validator.go
package cli

import "rule-router/internal/rule"

// IsValidOperator checks if a string is a valid rule condition operator.
// Delegates to rule.IsValidOperator so the operator set has a single source of truth.
func IsValidOperator(op string) bool {
	return rule.IsValidOperator(op)
}

// ContextHelp provides a formatted string with help about available variables and operators.
const ContextHelp = `
--- Context Help ---
Available Fields (always use {braces}):
  - Payload Fields:   {temperature}, {sensor.reading.value}, {items.0.name}
  - NATS Context:     {@subject}, {@subject.0}, {@subject.count}
  - HTTP Context:     {@path}, {@path.0}, {@method}, {@header.X-Custom}
  - Time Context:     {@time.hour}, {@day.name}, {@date.iso}, {@timestamp.unix}
  - KV Context:       {@kv.bucket.key}, {@kv.bucket.key:json.path}
  - Signature:        {@signature.valid}, {@signature.pubkey}
  - Root Message:     {@msg.field} (in forEach, access original message)
  - Array Wrapper:    {@items} (when message is a root array)
  - Value Wrapper:    {@value} (when message is a primitive)

  Note: Schedule rules have no incoming message — only Time, KV,
  {@timestamp()}, {@uuid4()}, and {@uuid7()} are available.

Available Operators:
  Equality:    eq, neq
  Comparison:  gt, lt, gte, lte
  String:      contains, not_contains
  Membership:  in, not_in
  Special:     exists, recent
  Array:       any, all, none (require nested conditions)
--------------------`

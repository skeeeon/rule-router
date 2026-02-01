// file: internal/cli/validator.go
package cli

// IsValidOperator checks if a string is a valid rule condition operator.
func IsValidOperator(op string) bool {
	validOps := map[string]bool{
		"eq":           true,
		"neq":          true,
		"gt":           true,
		"lt":           true,
		"gte":          true,
		"lte":          true,
		"contains":     true,
		"not_contains": true,
		"in":           true,
		"not_in":       true,
		"exists":       true,
		"recent":       true,
		"any":          true,
		"all":          true,
		"none":         true,
	}
	return validOps[op]
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

Available Operators:
  Equality:    eq, neq
  Comparison:  gt, lt, gte, lte
  String:      contains, not_contains
  Membership:  in, not_in
  Special:     exists, recent
  Array:       any, all, none (require nested conditions)
--------------------`

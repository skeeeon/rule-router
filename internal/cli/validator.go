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
Available Fields:
  - Payload Fields: your.field, nested.object.field
  - NATS Context:   @subject, @subject.0, @subject.count
  - HTTP Context:   @path, @path.0, @method, @path.count
  - Time Context:   @time.hour, @day.name, @date.iso, etc.
  - KV Context:     @kv.bucket.key:json.path
  - Signature:      @signature.valid, @signature.pubkey

Available Operators:
  eq neq gt lt gte lte contains not_contains in not_in exists recent
  any all none (for arrays in conditions)
--------------------`

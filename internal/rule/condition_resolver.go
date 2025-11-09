// file: internal/rule/condition_resolver.go

package rule

import (
	"fmt"
	"strings"
)

// IsTemplate checks if a string looks like a template variable: "{something}"
// Simple check - if it contains braces, we treat it as a template
// Exported for use in validation (loader.go)
func IsTemplate(s string) bool {
	return strings.Contains(s, "{") && strings.Contains(s, "}")
}

// ExtractVariable extracts the variable name from a template string
// Examples:
//   "{temperature}" -> "temperature"
//   "{@time.hour}" -> "@time.hour"
//   "{@kv.config.sensor:max}" -> "@kv.config.sensor:max"
//   "temperature" -> "" (not a template)
//   "{}" -> "" (malformed)
// Exported for use in validation (loader.go)
func ExtractVariable(template string) string {
	template = strings.TrimSpace(template)
	
	// Must start with { and end with }
	if !strings.HasPrefix(template, "{") || !strings.HasSuffix(template, "}") {
		return ""
	}
	
	// Extract content between braces
	varName := template[1 : len(template)-1]
	
	// Variable name cannot be empty
	if strings.TrimSpace(varName) == "" {
		return ""
	}
	
	return varName
}

// resolveConditionValue resolves a condition value (field or value side of comparison)
// If the value is a template string "{...}", it extracts the variable name and
// resolves it using the context. Otherwise, returns the value as-is.
//
// This enables variable-to-variable comparisons:
//   field: "{temperature}"
//   value: "{@kv.sensor_config.{sensor_id}:max_temp}"
//
// Type preservation:
//   - Numbers stay numbers for accurate numeric comparison
//   - Strings stay strings
//   - Booleans stay booleans
//   - Arrays and objects are preserved
//
// Error handling:
//   - Returns error if template variable cannot be resolved
//   - Non-template values (literals) always succeed
func resolveConditionValue(value interface{}, context *EvaluationContext) (interface{}, error) {
	// Only strings can be templates
	strValue, isString := value.(string)
	if !isString {
		// Numbers, bools, arrays, objects - return as-is
		return value, nil
	}
	
	// Fast path: if no braces, it's a literal string
	if !IsTemplate(strValue) {
		return value, nil
	}
	
	// Extract variable name from template
	varName := ExtractVariable(strValue)
	if varName == "" {
		// Malformed template like "{}" - treat as literal for robustness
		return value, nil
	}
	
	// Resolve using existing context resolution (handles all variable types)
	resolved, found := context.ResolveValue(varName)
	if !found {
		return nil, fmt.Errorf("variable not found: %s", varName)
	}
	
	// Return resolved value with original type preserved
	return resolved, nil
}

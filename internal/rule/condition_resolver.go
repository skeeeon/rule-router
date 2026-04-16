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
		return value, nil
	}

	// Quick check: must start with { and end with } to be a template
	if len(strValue) < 3 || strValue[0] != '{' || strValue[len(strValue)-1] != '}' {
		return value, nil
	}

	// Extract variable name directly (skip redundant IsTemplate check)
	varName := strValue[1 : len(strValue)-1]
	if varName == "" {
		return value, nil
	}

	resolved, found := context.ResolveValue(varName)
	if !found {
		return nil, fmt.Errorf("variable not found: %s", varName)
	}

	return resolved, nil
}

// resolveConditionValueFast uses pre-computed path data when available.
// Falls back to resolveConditionValue only for non-templates and nested braces.
func resolveConditionValueFast(value interface{}, varName string, path []string, context *EvaluationContext) (interface{}, error) {
	if path != nil {
		result, err := context.traverser.TraversePath(context.Msg, path)
		if err != nil {
			return nil, fmt.Errorf("variable not found: %s", varName)
		}
		return result, nil
	}
	// varName was pre-extracted at load time (system fields like @time.hour)
	// Skip the full re-parse of the template string
	if varName != "" {
		resolved, found := context.ResolveValue(varName)
		if !found {
			return nil, fmt.Errorf("variable not found: %s", varName)
		}
		return resolved, nil
	}
	return resolveConditionValue(value, context)
}

// PrepareConditions walks the condition tree and pre-computes field/value
// paths for simple message field templates. Called once at rule load time.
func PrepareConditions(conditions *Conditions) {
	if conditions == nil {
		return
	}
	for i := range conditions.Items {
		prepareCondition(&conditions.Items[i])
	}
	for i := range conditions.Groups {
		PrepareConditions(&conditions.Groups[i])
	}
}

// prepareCondition pre-computes paths for a single condition's Field and Value.
func prepareCondition(cond *Condition) {
	cond.fieldVarName, cond.fieldPath = precomputeTemplatePath(cond.Field)

	if strVal, ok := cond.Value.(string); ok {
		cond.valueVarName, cond.valuePath = precomputeTemplatePath(strVal)
	}

	// Recurse into nested conditions (array operators: any/all/none)
	if cond.Conditions != nil {
		PrepareConditions(cond.Conditions)
	}
}

// precomputeTemplatePath checks if a string is a simple message field template
// and returns the extracted variable name and pre-split path.
// Returns ("", nil) for anything that needs runtime resolution:
// non-templates, system fields (@), and nested braces.
func precomputeTemplatePath(s string) (string, []string) {
	if !IsTemplate(s) {
		return "", nil
	}
	varName := ExtractVariable(s)
	if varName == "" {
		return "", nil
	}
	// System fields require runtime dispatch -- don't pre-compute path,
	// but keep varName so resolveConditionValueFast can skip re-parsing
	if strings.HasPrefix(varName, "@") {
		return varName, nil
	}
	// Nested braces require runtime template resolution
	if strings.Contains(varName, "{") {
		return "", nil
	}
	path := strings.Split(varName, ".")
	return varName, path
}

// prepareForEachPath pre-computes the split path for a forEach template.
// Returns nil for system fields (@) or dynamic paths (nested braces).
func prepareForEachPath(forEachTemplate string) []string {
	fieldPath := ExtractVariable(forEachTemplate)
	if fieldPath == "" {
		return nil
	}
	// System fields require runtime ResolveValue dispatch
	if strings.HasPrefix(fieldPath, "@") {
		return nil
	}
	// Nested braces require runtime template resolution
	if strings.Contains(fieldPath, "{") {
		return nil
	}
	path, err := SplitPathRespectingBraces(fieldPath)
	if err != nil {
		return nil
	}
	return path
}

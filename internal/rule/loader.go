//file: internal/rule/loader.go

package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
	"gopkg.in/yaml.v3"
	"rule-router/internal/logger"
)

// Enhanced regex patterns for KV field validation with colon delimiter
var (
    // Matches variable placeholders: {variable_name}
    variablePattern = regexp.MustCompile(`\{([^}]+)\}`)
)

// RulesLoader handles loading and validation of rules from the filesystem
type RulesLoader struct {
    logger            *logger.Logger
    configuredBuckets map[string]bool  // KV buckets configured in config.yaml
}

// NewRulesLoader creates a new rules loader instance
func NewRulesLoader(log *logger.Logger, kvBuckets []string) *RulesLoader {
    if log == nil {
        return nil
    }
    
    // Build bucket lookup map for validation
    buckets := make(map[string]bool)
    for _, bucket := range kvBuckets {
        buckets[bucket] = true
    }
    
    log.Info("rules loader initialized with colon delimiter syntax", 
        "kvBucketsConfigured", len(buckets), 
        "buckets", kvBuckets,
        "syntax", "@kv.bucket.key:path")
    
    return &RulesLoader{
        logger:            log,
        configuredBuckets: buckets,
    }
}

// LoadFromDirectory loads all rule files from the specified directory
func (l *RulesLoader) LoadFromDirectory(path string) ([]Rule, error) {
    l.logger.Debug("loading rules from directory", "path", path)

    var validRules []Rule
    var errorCount int

    err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
        if err != nil {
            return fmt.Errorf("error accessing path %s: %w", filePath, err)
        }

        // If the entry is a directory and ends with "_test", skip it entirely.
        if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
            l.logger.Debug("skipping test directory", "path", filePath)
            return filepath.SkipDir
        }

        if info.IsDir() {
            return nil
        }

        ext := strings.ToLower(filepath.Ext(filePath))
        if ext != ".json" && ext != ".yaml" && ext != ".yml" {
            return nil
        }

        l.logger.Debug("loading rule file", "path", filePath)

        data, err := os.ReadFile(filePath)
        if err != nil {
            l.logger.Error("failed to read rule file", "path", filePath, "error", err)
            return fmt.Errorf("failed to read rule file %s: %w", filePath, err)
        }

        var ruleSet []Rule
        var parseErr error

        // Determine parser based on file extension
        switch ext {
        case ".json":
            parseErr = json.Unmarshal(data, &ruleSet)
        case ".yaml", ".yml":
            parseErr = yaml.Unmarshal(data, &ruleSet)
        }

        if parseErr != nil {
            l.logger.Error("failed to parse rule file", "path", filePath, "error", parseErr)
            return fmt.Errorf("failed to parse rule file %s: %w", filePath, parseErr)
        }

        l.logger.Debug("parsed rule file successfully", "path", filePath, "rulesFound", len(ruleSet))

        // Validate and filter rules
        for i, rule := range ruleSet {
            if err := l.validateRule(&rule, filePath, i); err != nil {
                l.logger.Error("skipping invalid rule", "file", filePath, "index", i, "subject", rule.Subject, "error", err)
                errorCount++
                continue
            }
            
            validRules = append(validRules, rule)
            l.logger.Debug("validated rule successfully", "file", filePath, "index", i, "subject", rule.Subject, "isPattern", containsWildcards(rule.Subject))
        }

        return nil
    })

    if err != nil {
        return nil, fmt.Errorf("failed to load rules: %w", err)
    }

    // Log summary
    if errorCount > 0 {
        l.logger.Info("rule loading completed with some validation errors", "validRules", len(validRules), "errorCount", errorCount)
    }

    l.logger.Info("rules loaded successfully", "count", len(validRules))
    return validRules, nil
}

// validateRule performs comprehensive validation of rule configuration including wildcard patterns and KV fields
func (l *RulesLoader) validateRule(rule *Rule, filePath string, ruleIndex int) error {
    if rule == nil {
        return fmt.Errorf("rule cannot be nil")
    }

    if rule.Subject == "" {
        return fmt.Errorf("rule subject cannot be empty")
    }

    // Validate wildcard patterns if present
    if containsWildcards(rule.Subject) {
        if err := l.validateWildcardPattern(rule.Subject); err != nil {
            return fmt.Errorf("invalid wildcard pattern '%s': %w", rule.Subject, err)
        }
        
        l.logger.Debug("validated wildcard pattern", "subject", rule.Subject, "file", filePath, "index", ruleIndex)
    }

    if rule.Action == nil {
        return fmt.Errorf("rule action cannot be nil")
    }

    if rule.Action.Subject == "" {
        return fmt.Errorf("action subject cannot be empty")
    }

    // NEW: Validate passthrough vs payload mutual exclusivity
    if err := l.validateActionPayload(rule.Action); err != nil {
        return fmt.Errorf("invalid action configuration: %w", err)
    }

    // Validate action subject for wildcard patterns too
    if containsWildcards(rule.Action.Subject) {
        l.logger.Info("action subject contains wildcards - ensure this is intentional", "actionSubject", rule.Action.Subject, "ruleSubject", rule.Subject, "file", filePath, "index", ruleIndex)
    }

    // Validate KV field references in action payload (with colon delimiter)
    if err := l.validateKVFieldsInTemplate(rule.Action.Payload); err != nil {
        return fmt.Errorf("invalid KV field in action payload: %w", err)
    }

    // Validate KV field references in action subject template
    if err := l.validateKVFieldsInTemplate(rule.Action.Subject); err != nil {
        return fmt.Errorf("invalid KV field in action subject: %w", err)
    }

    if rule.Conditions != nil {
        if err := l.validateConditions(rule.Conditions); err != nil {
            return fmt.Errorf("invalid conditions: %w", err)
        }
    }

    return nil
}

// CORRECTED: validateActionPayload ensures action payload configuration is valid
func (l *RulesLoader) validateActionPayload(action *Action) error {
    // The only truly invalid state is when passthrough is true AND a non-empty payload is also defined.
    // An empty payload ("") is a valid configuration for a templated action.
    if action.Passthrough && action.Payload != "" {
        return fmt.Errorf("cannot specify both 'payload' and 'passthrough: true' - choose one")
    }
    
    // The check for "neither specified" from the original plan was flawed because it couldn't
    // distinguish between an omitted payload and an explicitly empty one (`payload: ""`),
    // causing it to reject valid rules. An action that produces an empty message is a valid use case.
    return nil
}

// validateWildcardPattern validates NATS wildcard pattern syntax
func (l *RulesLoader) validateWildcardPattern(subject string) error {
    // Use the existing ValidatePattern function from pattern.go
    if err := ValidatePattern(subject); err != nil {
        return err
    }
    
    // Additional validation for common mistakes
    if strings.Contains(subject, "**") {
        return fmt.Errorf("double wildcards '**' are not valid, use '>' for multi-level wildcards")
    }
    
    // Check for empty tokens which are common mistakes
    if strings.Contains(subject, "..") {
        return fmt.Errorf("empty tokens ('..') are not allowed in patterns")
    }
    
    return nil
}

// validateConditions recursively validates condition groups
func (l *RulesLoader) validateConditions(conditions *Conditions) error {
    if conditions == nil {
        return fmt.Errorf("conditions cannot be nil")
    }

    if conditions.Operator != "and" && conditions.Operator != "or" {
        return fmt.Errorf("invalid operator: %s", conditions.Operator)
    }

    // Validate individual conditions
    for i, condition := range conditions.Items {
        if condition.Field == "" {
            return fmt.Errorf("condition field cannot be empty at index %d", i)
        }
        if !l.isValidOperator(condition.Operator) {
            return fmt.Errorf("invalid condition operator '%s' at index %d", condition.Operator, i)
        }
        
        // Validate subject field references if present
        if strings.HasPrefix(condition.Field, "@subject") {
            if err := l.validateSubjectField(condition.Field); err != nil {
                return fmt.Errorf("invalid subject field '%s' at index %d: %w", condition.Field, i, err)
            }
        }
        
        // Validate KV field references with colon delimiter
        if strings.HasPrefix(condition.Field, "@kv") {
            if err := l.validateKVFieldWithVariables(condition.Field); err != nil {
                return fmt.Errorf("invalid KV field '%s' at index %d: %w", condition.Field, i, err)
            }
        }
    }

    // Recursively validate nested condition groups
    for i, group := range conditions.Groups {
        if err := l.validateConditions(&group); err != nil {
            return fmt.Errorf("invalid nested condition group at index %d: %w", i, err)
        }
    }

    return nil
}

// **FIX START**
// validateSubjectField validates subject field references like @subject.1, @subject.count
// This version correctly parses any non-negative integer for token indices.
func (l *RulesLoader) validateSubjectField(field string) error {
	// Allow the full subject itself, e.g., "@subject"
	if field == "@subject" {
		return nil
	}

	// For token access, the field must start with the correct prefix.
	if !strings.HasPrefix(field, "@subject.") {
		return fmt.Errorf("invalid subject field format: %s", field)
	}

	// Extract the accessor part (e.g., "0", "10", "count", "first").
	suffix := field[9:] // Remove "@subject."

	// Check for special keyword accessors.
	switch suffix {
	case "count", "first", "last":
		return nil
	}

	// If it's not a keyword, it must be a numeric index.
	if index, err := strconv.Atoi(suffix); err == nil {
		// Ensure the index is not negative.
		if index < 0 {
			return fmt.Errorf("subject token index cannot be negative, got: %d", index)
		}
		return nil // The format is valid.
	}

	// If it's not a recognized keyword and not a valid non-negative integer, it's an error.
	return fmt.Errorf("invalid subject field token accessor '%s'; must be a non-negative integer, 'first', 'last', or 'count'", suffix)
}
// **FIX END**

// isValidOperator checks if the operator is supported
func (l *RulesLoader) isValidOperator(op string) bool {
    validOperators := map[string]bool{
        // Comparison operators
        "eq":       true,
        "neq":      true,
        "gt":       true,
        "lt":       true,
        "gte":      true,
        "lte":      true,
        "exists":   true,
        
        // String/Array operators
        "contains":     true,
        "not_contains": true,
        
        // Array membership operators
        "in":     true,
        "not_in": true,
    }
    return validOperators[op]
}

// validateKVFieldsInTemplate validates all KV field references in a template string
func (l *RulesLoader) validateKVFieldsInTemplate(template string) error {
    // Extract all {@kv...} fields using brace-aware parsing
    kvFields := l.extractKVFieldsFromTemplate(template)
    
    for _, field := range kvFields {
        // Check for colon in the field
        if !strings.Contains(field, ":") {
            return fmt.Errorf("KV field in template must use ':' delimiter (format: @kv.bucket.key:path), got: {%s}", field)
        }
        
        if err := l.validateKVFieldWithVariables(field); err != nil {
            return fmt.Errorf("invalid KV field '%s' in template: %w", field, err)
        }
    }
    
    return nil
}

// extractKVFieldsFromTemplate extracts all {@kv...} field references from a template
// Uses brace-counting to handle nested variables like {@kv.bucket.{key}:{path}}
func (l *RulesLoader) extractKVFieldsFromTemplate(template string) []string {
    var fields []string
    
    i := 0
    for i < len(template) {
        // Look for "{@kv."
        if i+5 <= len(template) && template[i:i+5] == "{@kv." {
            // Found start of KV field - extract the complete field with brace counting
            field, endPos := l.extractBracedContent(template, i)
            if field != "" {
                // Remove outer braces and the leading @
                // field is like "{@kv.bucket.key:path}" - we want "@kv.bucket.key:path"
                if len(field) > 2 {
                    fields = append(fields, field[1:len(field)-1]) // Remove { and }
                }
            }
            i = endPos
        } else {
            i++
        }
    }
    
    return fields
}

// extractBracedContent extracts content within braces starting at startPos
// Returns the complete braced content and the position after the closing brace
// Handles nested braces correctly
func (l *RulesLoader) extractBracedContent(template string, startPos int) (string, int) {
    if startPos >= len(template) || template[startPos] != '{' {
        return "", startPos
    }
    
    braceDepth := 0
    i := startPos
    
    for i < len(template) {
        switch template[i] {
        case '{':
            braceDepth++
        case '}':
            braceDepth--
            if braceDepth == 0 {
                // Found matching closing brace
                return template[startPos : i+1], i + 1
            }
        }
        i++
    }
    
    // Unclosed brace - return empty
    return "", startPos
}

// validateKVFieldWithVariables validates KV fields with colon delimiter syntax
// Supports variable substitution in keys and paths
// Format: @kv.bucket.key:json.path where key and path can contain {variables}
//
// FIXED: Now uses SplitPathRespectingBraces to handle variables with dots like {sensor.choice}
func (l *RulesLoader) validateKVFieldWithVariables(field string) error {
    l.logger.Debug("validating KV field with colon delimiter", "field", field)
    
    // Parse the field with variable awareness
    if !strings.HasPrefix(field, "@kv.") {
        return fmt.Errorf("KV field must start with '@kv.', got: %s", field)
    }
    
    remainder := field[4:] // Remove "@kv."
    
    // Check for colon delimiter (REQUIRED)
    if !strings.Contains(remainder, ":") {
        return fmt.Errorf("KV field must use ':' to separate key from JSON path (format: @kv.bucket.key:path), got: %s", field)
    }
    
    // Check for multiple colons
    if strings.Count(remainder, ":") > 1 {
        return fmt.Errorf("KV field must contain exactly one ':' delimiter, got: %s", field)
    }
    
    // Split on colon
    colonIndex := strings.Index(remainder, ":")
    bucketKeyPart := remainder[:colonIndex]
    jsonPathPart := remainder[colonIndex+1:]
    
    // Validate JSON path is not empty
    if jsonPathPart == "" {
        return fmt.Errorf("JSON path after ':' cannot be empty (format: @kv.bucket.key:path), got: %s", field)
    }
    
    // Parse bucket.key
    bucketKeyParts := strings.SplitN(bucketKeyPart, ".", 2)
    if len(bucketKeyParts) != 2 {
        return fmt.Errorf("KV field must have 'bucket.key' before ':', got: %s", bucketKeyPart)
    }
    
    bucket := bucketKeyParts[0]
    key := bucketKeyParts[1]
    
    // Validate bucket name format (must be literal, not a variable)
    if bucket == "" {
        return fmt.Errorf("KV bucket name cannot be empty in field: %s", field)
    }
    
    // Check for variables in bucket name (not allowed)
    if strings.Contains(bucket, "{") {
        return fmt.Errorf("variables in bucket names are not supported, got: %s", field)
    }
    
    // Validate bucket name characters and existence
    if err := l.validateBucketNameFormat(bucket); err != nil {
        return fmt.Errorf("invalid bucket name '%s' in field '%s': %w", bucket, field, err)
    }
    
    // CRITICAL: Check that the bucket is configured
    if !l.configuredBuckets[bucket] {
        availableBuckets := make([]string, 0, len(l.configuredBuckets))
        for b := range l.configuredBuckets {
            availableBuckets = append(availableBuckets, b)
        }
        return fmt.Errorf("KV bucket '%s' not configured (available: %v)", bucket, availableBuckets)
    }
    
    // Validate key (can be a variable or literal)
    if key == "" {
        return fmt.Errorf("KV key name cannot be empty in field: %s", field)
    }
    
    // If key contains variables, validate variable syntax
    if strings.Contains(key, "{") {
        if err := l.validateVariableSyntax(key); err != nil {
            return fmt.Errorf("invalid variable syntax in key '%s': %w", key, err)
        }
        l.logger.Debug("validated KV field with variables in key", "field", field, "bucket", bucket, "key", key)
    }
    
    // FIXED: Parse JSON path using brace-aware splitting
    // This allows variables like {sensor.choice} to be treated as single tokens
    jsonPath, err := SplitPathRespectingBraces(jsonPathPart)
    if err != nil {
        return fmt.Errorf("invalid JSON path '%s' in field %s: %w", jsonPathPart, field, err)
    }
    
    // Validate each path segment
    for i, segment := range jsonPath {
        if segment == "" {
            return fmt.Errorf("empty JSON path segment at position %d in field: %s", i, field)
        }
        
        // If segment contains variables, validate syntax
        if strings.Contains(segment, "{") {
            if err := l.validateVariableSyntax(segment); err != nil {
                return fmt.Errorf("invalid variable syntax in JSON path segment '%s': %w", segment, err)
            }
        } else {
            // Validate literal segments
            if err := l.validateJSONPathSegment(segment); err != nil {
                return fmt.Errorf("invalid JSON path segment '%s' in field %s: %w", segment, field, err)
            }
        }
    }
    
    l.logger.Debug("successfully validated KV field with colon delimiter", "field", field, "bucket", bucket, "jsonPath", jsonPath)
    return nil
}

// validateVariableSyntax ensures variable placeholders are properly formatted
func (l *RulesLoader) validateVariableSyntax(text string) error {
    // Find all variable patterns in the text
    matches := variablePattern.FindAllStringSubmatch(text, -1)
    
    for _, match := range matches {
        if len(match) != 2 {
            continue
        }
        
        varName := match[1]
        if varName == "" {
            return fmt.Errorf("empty variable name in: %s", text)
        }
        
        // Basic validation - variable names should be reasonable
        if strings.Contains(varName, "{") || strings.Contains(varName, "}") {
            return fmt.Errorf("nested braces not allowed in variable name: %s", varName)
        }
        
        l.logger.Debug("validated variable syntax", "variable", varName, "context", text)
    }
    
    return nil
}

// validateJSONPathSegment validates individual JSON path segments
func (l *RulesLoader) validateJSONPathSegment(segment string) error {
    // Allow alphanumeric, underscore, dash, and numbers (for array indices)
    for _, char := range segment {
        if !((char >= 'a' && char <= 'z') || 
             (char >= 'A' && char <= 'Z') || 
             (char >= '0' && char <= '9') || 
             char == '_' || char == '-') {
            return fmt.Errorf("invalid character '%c' in JSON path segment", char)
        }
    }
    return nil
}

// validateBucketNameFormat validates NATS KV bucket naming rules
func (l *RulesLoader) validateBucketNameFormat(name string) error {
    if len(name) == 0 {
        return fmt.Errorf("bucket name cannot be empty")
    }
    if len(name) > 64 {
        return fmt.Errorf("bucket name too long (max 64 characters)")
    }
    
    // NATS bucket names: letters, numbers, dash, underscore
    for _, char := range name {
        if !((char >= 'a' && char <= 'z') || 
             (char >= 'A' && char <= 'Z') || 
             (char >= '0' && char <= '9') || 
             char == '-' || char == '_') {
            return fmt.Errorf("invalid character '%c' (allowed: a-z, A-Z, 0-9, -, _)", char)
        }
    }
    
    return nil
}

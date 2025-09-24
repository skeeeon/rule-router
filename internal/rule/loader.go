//file: internal/rule/loader.go

package rule

import (
    json "github.com/goccy/go-json"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "strings"

    "gopkg.in/yaml.v3"
    "rule-router/internal/logger"
)

// Enhanced regex patterns for KV field validation
var (
    // Matches KV fields in templates: {@kv.bucket.key} or {@kv.bucket.{var}.path}
    templateKVPattern = regexp.MustCompile(`\{@kv\.[^}]+\}`)
    
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
    
    log.Info("rules loader initialized", "kvBucketsConfigured", len(buckets), "buckets", kvBuckets)
    
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

    // Log summary - using Info instead of Warn to avoid interface issues
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

    // Validate action subject for wildcard patterns too - using Info instead of Warn
    if containsWildcards(rule.Action.Subject) {
        l.logger.Info("action subject contains wildcards - ensure this is intentional", "actionSubject", rule.Action.Subject, "ruleSubject", rule.Subject, "file", filePath, "index", ruleIndex)
    }

    // ENHANCED: Validate KV field references in action payload
    if err := l.validateKVFieldsInTemplate(rule.Action.Payload); err != nil {
        return fmt.Errorf("invalid KV field in action payload: %w", err)
    }

    // ENHANCED: Validate KV field references in action subject template
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
        
        // ENHANCED: Validate KV field references with variable support
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

// validateSubjectField validates subject field references like @subject.1, @subject.count
func (l *RulesLoader) validateSubjectField(field string) error {
    validSubjectFields := []string{
        "@subject",
        "@subject.count",
        "@subject.first", 
        "@subject.last",
    }
    
    // Check exact matches first
    for _, valid := range validSubjectFields {
        if field == valid {
            return nil
        }
    }
    
    // Check indexed fields: @subject.0, @subject.1, etc.
    if strings.HasPrefix(field, "@subject.") {
        suffix := field[9:] // Remove "@subject."
        
        // Simple validation for numeric indices
        if len(suffix) == 1 && suffix >= "0" && suffix <= "9" {
            return nil // @subject.0 through @subject.9 are valid
        }
        
        // For higher numbers, we could use strconv.Atoi but keeping it simple
        return fmt.Errorf("unsupported subject field suffix '%s', use @subject.0-9, @subject.count, @subject.first, or @subject.last", suffix)
    }
    
    return fmt.Errorf("invalid subject field format")
}

// isValidOperator checks if the operator is supported
func (l *RulesLoader) isValidOperator(op string) bool {
    validOperators := map[string]bool{
        "eq":       true,
        "neq":      true,
        "gt":       true,
        "lt":       true,
        "gte":      true,
        "lte":      true,
        "exists":   true,
        "contains": true,
    }
    return validOperators[op]
}

// ENHANCED: validateKVFieldsInTemplate validates all KV field references in a template string
func (l *RulesLoader) validateKVFieldsInTemplate(template string) error {
    // Find all {@kv.bucket.key} patterns in the template
    matches := templateKVPattern.FindAllString(template, -1)
    
    for _, match := range matches {
        // Extract the field part (remove { and })
        field := match[1 : len(match)-1] // Remove { and }
        if err := l.validateKVFieldWithVariables(field); err != nil {
            return fmt.Errorf("invalid KV field '%s' in template: %w", field, err)
        }
    }
    
    return nil
}

// ENHANCED: validateKVFieldWithVariables validates KV fields that may contain variables
func (l *RulesLoader) validateKVFieldWithVariables(field string) error {
    l.logger.Debug("validating KV field with variable support", "field", field)
    
    // Parse the field with variable awareness
    if !strings.HasPrefix(field, "@kv.") {
        return fmt.Errorf("KV field must start with '@kv.', got: %s", field)
    }
    
    remainder := field[4:] // Remove "@kv."
    parts := strings.Split(remainder, ".")
    if len(parts) < 2 {
        return fmt.Errorf("KV field must have at least '@kv.bucket.key' format, got: %s", field)
    }
    
    bucket := parts[0]
    key := parts[1]
    jsonPath := parts[2:] // Everything after bucket.key is JSON path
    
    // Validate bucket name format (must be literal, not a variable)
    if bucket == "" {
        return fmt.Errorf("KV bucket name cannot be empty in field: %s", field)
    }
    
    // ENHANCED: Check for variables in bucket name (not allowed)
    if strings.Contains(bucket, "{") {
        return fmt.Errorf("variables in bucket names are not supported: %s", field)
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
    
    // ENHANCED: Validate key (can be a variable or literal)
    if key == "" {
        return fmt.Errorf("KV key name cannot be empty in field: %s", field)
    }
    
    // If key contains variables, validate variable syntax but don't validate content
    if strings.Contains(key, "{") {
        if err := l.validateVariableSyntax(key); err != nil {
            return fmt.Errorf("invalid variable syntax in key '%s': %w", key, err)
        }
        l.logger.Debug("validated KV field with variables in key", "field", field, "bucket", bucket, "key", key)
    }
    
    // ENHANCED: Validate JSON path segments (can contain variables)
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
    
    l.logger.Debug("successfully validated KV field with variables", "field", field, "bucket", bucket)
    return nil
}

// NEW: validateVariableSyntax ensures variable placeholders are properly formatted
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
        
        // Variables can be:
        // - Simple field names: customer_id, sensor_id
        // - System fields: @subject.1, @time.hour
        // - Complex paths: user.profile.name
        l.logger.Debug("validated variable syntax", "variable", varName, "context", text)
    }
    
    return nil
}

// NEW: validateJSONPathSegment validates individual JSON path segments
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

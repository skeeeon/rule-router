// file: internal/rule/loader.go

package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
	"rule-router/internal/logger"
)

// RulesLoader handles loading and validating rule definitions from YAML files
type RulesLoader struct {
	logger              *logger.Logger
	configuredKVBuckets []string
}

// NewRulesLoader creates a new rules loader
func NewRulesLoader(log *logger.Logger, kvBuckets []string) *RulesLoader {
	return &RulesLoader{
		logger:              log,
		configuredKVBuckets: kvBuckets,
	}
}

// LoadFromDirectory loads all .yaml and .yml files from a directory, recursively,
// while skipping any directories with a "_test" suffix.
func (l *RulesLoader) LoadFromDirectory(dirPath string) ([]Rule, error) {
	l.logger.Info("loading rules from directory", "path", dirPath)

	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to access rules directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", dirPath)
	}

	var files []string
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
			l.logger.Debug("skipping test directory", "path", path)
			return filepath.SkipDir
		}

		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if ext == ".yaml" || ext == ".yml" {
				files = append(files, path)
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error walking rules directory: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no YAML rule files found in directory: %s", dirPath)
	}

	l.logger.Info("found rule files", "count", len(files), "files", files)

	var allRules []Rule
	for _, file := range files {
		rules, err := l.LoadFromFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to load rules from %s: %w", file, err)
		}
		allRules = append(allRules, rules...)
	}

	l.logger.Info("successfully loaded all rules",
		"totalRules", len(allRules),
		"fileCount", len(files))

	return allRules, nil
}

// LoadFromFile loads rules from a single YAML file
func (l *RulesLoader) LoadFromFile(filePath string) ([]Rule, error) {
	l.logger.Debug("loading rules from file", "path", filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var rules []Rule
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	l.logger.Debug("parsed rules from file",
		"file", filePath,
		"ruleCount", len(rules))

	for i := range rules {
		if err := l.validateRule(&rules[i], filePath, i); err != nil {
			return nil, fmt.Errorf("rule %d in %s is invalid: %w", i, filePath, err)
		}
	}

	l.logger.Debug("all rules validated successfully",
		"file", filePath,
		"ruleCount", len(rules))

	return rules, nil
}

// validateRule validates a complete rule with new trigger/action format
func (l *RulesLoader) validateRule(rule *Rule, filePath string, ruleIndex int) error {
	if err := l.validateTrigger(&rule.Trigger, filePath, ruleIndex); err != nil {
		return err
	}

	if err := l.validateAction(&rule.Action, filePath, ruleIndex); err != nil {
		return err
	}

	if rule.Conditions != nil {
		if err := l.validateConditions(rule.Conditions); err != nil {
			return fmt.Errorf("invalid conditions: %w", err)
		}
	}

	return nil
}

// validateTrigger ensures exactly one trigger type is specified
func (l *RulesLoader) validateTrigger(trigger *Trigger, filePath string, ruleIndex int) error {
	natsCount := 0
	httpCount := 0

	if trigger.NATS != nil {
		natsCount++
		if trigger.NATS.Subject == "" {
			return fmt.Errorf("NATS trigger subject cannot be empty")
		}
		if err := l.validateWildcardPattern(trigger.NATS.Subject); err != nil {
			return fmt.Errorf("invalid NATS subject pattern: %w", err)
		}
	}

	if trigger.HTTP != nil {
		httpCount++
		if trigger.HTTP.Path == "" {
			return fmt.Errorf("HTTP trigger path cannot be empty")
		}
		if !strings.HasPrefix(trigger.HTTP.Path, "/") {
			return fmt.Errorf("HTTP path must start with '/': %s", trigger.HTTP.Path)
		}
		if trigger.HTTP.Method != "" {
			validMethods := map[string]bool{
				"GET": true, "POST": true, "PUT": true, "PATCH": true,
				"DELETE": true, "HEAD": true, "OPTIONS": true,
			}
			method := strings.ToUpper(trigger.HTTP.Method)
			if !validMethods[method] {
				return fmt.Errorf("invalid HTTP method: %s", trigger.HTTP.Method)
			}
			trigger.HTTP.Method = method
		}
	}

	if natsCount+httpCount == 0 {
		return fmt.Errorf("rule must have either a NATS or HTTP trigger")
	}

	if natsCount+httpCount > 1 {
		return fmt.Errorf("rule must have exactly one trigger type (found NATS=%d, HTTP=%d)", natsCount, httpCount)
	}

	return nil
}

// validateAction ensures exactly one action type is specified
func (l *RulesLoader) validateAction(action *Action, filePath string, ruleIndex int) error {
	natsCount := 0
	httpCount := 0

	if action.NATS != nil {
		natsCount++
		if err := l.validateNATSAction(action.NATS); err != nil {
			return fmt.Errorf("invalid NATS action: %w", err)
		}
	}

	if action.HTTP != nil {
		httpCount++
		if err := l.validateHTTPAction(action.HTTP); err != nil {
			return fmt.Errorf("invalid HTTP action: %w", err)
		}
	}

	if natsCount+httpCount == 0 {
		return fmt.Errorf("rule must have either a NATS or HTTP action")
	}

	if natsCount+httpCount > 1 {
		return fmt.Errorf("rule must have exactly one action type (found NATS=%d, HTTP=%d)", natsCount, httpCount)
	}

	return nil
}

// validateNATSAction validates a NATS action configuration
func (l *RulesLoader) validateNATSAction(action *NATSAction) error {
	if action.Subject == "" {
		return fmt.Errorf("NATS action subject cannot be empty")
	}

	if action.Passthrough && action.Payload != "" {
		return fmt.Errorf("cannot specify both 'payload' and 'passthrough: true' - choose one")
	}

	// NEW: Validate forEach configuration
	if action.ForEach != "" {
		if err := l.validateForEachConfig(action.ForEach, action.Filter); err != nil {
			return fmt.Errorf("invalid forEach configuration: %w", err)
		}
	}

	if containsWildcards(action.Subject) {
		l.logger.Debug("NATS action subject contains wildcards - ensure this is intentional",
			"actionSubject", action.Subject)
	}

	if err := l.validateKVFieldsInTemplate(action.Payload); err != nil {
		return fmt.Errorf("invalid KV field in payload: %w", err)
	}

	if err := l.validateKVFieldsInTemplate(action.Subject); err != nil {
		return fmt.Errorf("invalid KV field in subject: %w", err)
	}

	if action.Headers != nil {
		for key, value := range action.Headers {
			if key == "" {
				return fmt.Errorf("header name cannot be empty")
			}
			if err := l.validateKVFieldsInTemplate(value); err != nil {
				return fmt.Errorf("invalid template in header '%s': %w", key, err)
			}
		}
	}

	return nil
}

// validateHTTPAction validates an HTTP action configuration
func (l *RulesLoader) validateHTTPAction(action *HTTPAction) error {
	if action.URL == "" {
		return fmt.Errorf("HTTP action URL cannot be empty")
	}

	if !strings.Contains(action.URL, "{") && !strings.HasPrefix(action.URL, "http://") && !strings.HasPrefix(action.URL, "https://") {
		return fmt.Errorf("HTTP action URL must start with http:// or https://: %s", action.URL)
	}

	if action.Method == "" {
		return fmt.Errorf("HTTP action method cannot be empty")
	}

	validMethods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true,
		"DELETE": true, "HEAD": true, "OPTIONS": true,
	}
	method := strings.ToUpper(action.Method)
	if !validMethods[method] {
		return fmt.Errorf("invalid HTTP method: %s", action.Method)
	}
	action.Method = method

	if action.Passthrough && action.Payload != "" {
		return fmt.Errorf("cannot specify both 'payload' and 'passthrough: true' - choose one")
	}

	// NEW: Validate forEach configuration
	if action.ForEach != "" {
		if err := l.validateForEachConfig(action.ForEach, action.Filter); err != nil {
			return fmt.Errorf("invalid forEach configuration: %w", err)
		}
	}

	if err := l.validateKVFieldsInTemplate(action.Payload); err != nil {
		return fmt.Errorf("invalid KV field in payload: %w", err)
	}

	if err := l.validateKVFieldsInTemplate(action.URL); err != nil {
		return fmt.Errorf("invalid KV field in URL: %w", err)
	}

	if action.Headers != nil {
		for key, value := range action.Headers {
			if key == "" {
				return fmt.Errorf("header name cannot be empty")
			}
			if err := l.validateKVFieldsInTemplate(value); err != nil {
				return fmt.Errorf("invalid template in header '%s': %w", key, err)
			}
		}
	}

	if action.Retry != nil {
		if action.Retry.MaxAttempts < 1 {
			action.Retry.MaxAttempts = 1
		}
		if action.Retry.InitialDelay != "" {
			if _, err := time.ParseDuration(action.Retry.InitialDelay); err != nil {
				return fmt.Errorf("invalid retry initialDelay '%s': %w", action.Retry.InitialDelay, err)
			}
		}
		if action.Retry.MaxDelay != "" {
			if _, err := time.ParseDuration(action.Retry.MaxDelay); err != nil {
				return fmt.Errorf("invalid retry maxDelay '%s': %w", action.Retry.MaxDelay, err)
			}
		}
	}

	return nil
}

// NEW: validateForEachConfig validates forEach field configuration
func (l *RulesLoader) validateForEachConfig(forEachField string, filter *Conditions) error {
	// Ensure it's a valid JSON path (no wildcards)
	if strings.Contains(forEachField, "*") || strings.Contains(forEachField, ">") {
		return fmt.Errorf("forEach field cannot contain wildcards: %s", forEachField)
	}

	// NEW: Validate path isn't too deeply nested (prevent stack overflow)
	if strings.Count(forEachField, ".") > 10 {
		return fmt.Errorf("forEach path too deeply nested (max depth: 10): %s", forEachField)
	}

	// NEW: Warn about potential performance issues
	if filter == nil {
		l.logger.Warn("forEach without filter may process large arrays",
			"field", forEachField,
			"recommendation", "Consider adding filter conditions to limit iterations")
	}

	// Validate filter conditions if present
	if filter != nil {
		if err := l.validateConditions(filter); err != nil {
			return fmt.Errorf("invalid forEach filter conditions: %w", err)
		}
	}

	return nil
}

// validateWildcardPattern validates NATS wildcard pattern syntax
func (l *RulesLoader) validateWildcardPattern(subject string) error {
	if err := ValidatePattern(subject); err != nil {
		return err
	}

	if strings.Contains(subject, "**") {
		return fmt.Errorf("double wildcards '**' are not valid, use '>' for multi-level wildcards")
	}

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

	for i, condition := range conditions.Items {
		if condition.Field == "" {
			return fmt.Errorf("condition field cannot be empty at index %d", i)
		}
		if !l.isValidOperator(condition.Operator) {
			return fmt.Errorf("invalid condition operator '%s' at index %d", condition.Operator, i)
		}

		// NEW: Validate array operators require nested conditions
		if condition.Operator == "any" || condition.Operator == "all" || condition.Operator == "none" {
			if condition.Conditions == nil {
				return fmt.Errorf("array operator '%s' requires nested conditions at index %d", condition.Operator, i)
			}
			
			if err := l.validateConditions(condition.Conditions); err != nil {
				return fmt.Errorf("invalid nested conditions for array operator '%s' at index %d: %w", 
					condition.Operator, i, err)
			}
		}

		if strings.HasPrefix(condition.Field, "@subject") {
			if err := l.validateSubjectField(condition.Field); err != nil {
				return fmt.Errorf("invalid subject field '%s' at index %d: %w", condition.Field, i, err)
			}
		}

		if strings.HasPrefix(condition.Field, "@path") {
			if err := l.validatePathField(condition.Field); err != nil {
				return fmt.Errorf("invalid path field '%s' at index %d: %w", condition.Field, i, err)
			}
		}

		if strings.HasPrefix(condition.Field, "@kv") {
			if err := l.validateKVFieldWithVariables(condition.Field); err != nil {
				return fmt.Errorf("invalid KV field '%s' at index %d: %w", condition.Field, i, err)
			}
		}
	}

	for i, group := range conditions.Groups {
		if err := l.validateConditions(&group); err != nil {
			return fmt.Errorf("invalid nested condition group at index %d: %w", i, err)
		}
	}

	return nil
}

// validateSubjectField validates subject field references
func (l *RulesLoader) validateSubjectField(field string) error {
	validFields := map[string]bool{
		"@subject":       true,
		"@subject.count": true,
	}

	if validFields[field] {
		return nil
	}

	if strings.HasPrefix(field, "@subject.") {
		indexStr := field[9:]
		if _, err := strconv.Atoi(indexStr); err == nil {
			return nil
		}
		return fmt.Errorf("invalid subject field format (expected @subject.N where N is a non-negative integer)")
	}

	return fmt.Errorf("invalid subject field (must be @subject, @subject.count, or @subject.N)")
}

// validatePathField validates HTTP path field references
func (l *RulesLoader) validatePathField(field string) error {
	validFields := map[string]bool{
		"@path":       true,
		"@path.count": true,
	}

	if validFields[field] {
		return nil
	}

	if strings.HasPrefix(field, "@path.") {
		indexStr := field[6:]
		if _, err := strconv.Atoi(indexStr); err == nil {
			return nil
		}
		return fmt.Errorf("invalid path field format (expected @path.N where N is a non-negative integer)")
	}

	return fmt.Errorf("invalid path field (must be @path, @path.count, or @path.N)")
}

// validateKVFieldsInTemplate extracts and validates all KV field references
func (l *RulesLoader) validateKVFieldsInTemplate(template string) error {
	if template == "" {
		return nil
	}

	kvFields := l.extractKVFieldsFromTemplate(template)

	for _, field := range kvFields {
		if err := l.validateKVFieldWithVariables(field); err != nil {
			return err
		}

		bucket := l.extractBucketFromKVField(field)
		if bucket != "" && !l.isBucketConfigured(bucket) {
			l.logger.Debug("KV field references unconfigured bucket",
				"field", field,
				"bucket", bucket,
				"configuredBuckets", l.configuredKVBuckets,
				"impact", "This KV lookup will fail at runtime if KV is enabled")
		}
	}

	return nil
}

// extractKVFieldsFromTemplate finds all KV field references
func (l *RulesLoader) extractKVFieldsFromTemplate(template string) []string {
	var fields []string
	re := regexp.MustCompile(`\{@kv\.(.+?)\}`)
	matches := re.FindAllStringSubmatch(template, -1)
	for _, match := range matches {
		if len(match) > 1 {
			fields = append(fields, "@kv."+match[1])
		}
	}
	return fields
}

// validateKVFieldWithVariables validates KV fields with colon delimiter syntax
func (l *RulesLoader) validateKVFieldWithVariables(field string) error {
	l.logger.Debug("validating KV field with colon delimiter", "field", field)

	if !strings.HasPrefix(field, "@kv.") {
		return fmt.Errorf("KV field must start with '@kv.', got: %s", field)
	}

	remainder := field[4:]

	if !strings.Contains(remainder, ":") {
		return fmt.Errorf("KV field must use ':' to separate key from JSON path (format: @kv.bucket.key:path), got: %s", field)
	}

	if strings.Count(remainder, ":") > 1 {
		return fmt.Errorf("KV field must contain exactly one ':' delimiter, got: %s", field)
	}

	colonIndex := strings.Index(remainder, ":")
	bucketKeyPart := remainder[:colonIndex]
	jsonPathPart := remainder[colonIndex+1:]

	if jsonPathPart == "" {
		return fmt.Errorf("JSON path after ':' cannot be empty (format: @kv.bucket.key:path), got: %s", field)
	}

	bucketKeyParts := strings.SplitN(bucketKeyPart, ".", 2)
	if len(bucketKeyParts) != 2 {
		return fmt.Errorf("KV field must have 'bucket.key' before ':', got: %s", bucketKeyPart)
	}

	bucket := bucketKeyParts[0]
	key := bucketKeyParts[1]

	if bucket == "" {
		return fmt.Errorf("KV bucket name cannot be empty in field: %s", field)
	}
	if key == "" {
		return fmt.Errorf("KV key name cannot be empty in field: %s", field)
	}

	return nil
}

// extractBucketFromKVField extracts the bucket name
func (l *RulesLoader) extractBucketFromKVField(field string) string {
	if !strings.HasPrefix(field, "@kv.") {
		return ""
	}

	remainder := field[4:]
	parts := strings.SplitN(remainder, ".", 2)
	if len(parts) > 0 {
		return parts[0]
	}

	return ""
}

// isBucketConfigured checks if a bucket is configured
func (l *RulesLoader) isBucketConfigured(bucket string) bool {
	if len(l.configuredKVBuckets) == 0 {
		return true
	}
	for _, configured := range l.configuredKVBuckets {
		if configured == bucket {
			return true
		}
	}
	return false
}

// isValidOperator checks if an operator is valid
// NEW: Now includes array operators (any, all, none)
func (l *RulesLoader) isValidOperator(op string) bool {
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

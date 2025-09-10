//file: internal/rule/loader.go

package rule

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "gopkg.in/yaml.v3"
    "rule-router/internal/logger"
)

// RulesLoader handles loading and validation of rules from the filesystem
type RulesLoader struct {
    logger *logger.Logger
}

// NewRulesLoader creates a new rules loader instance
func NewRulesLoader(log *logger.Logger) *RulesLoader {
    if log == nil {
        return nil
    }
    return &RulesLoader{
        logger: log,
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
                l.logger.Error("skipping invalid rule", "file", filePath, "index", i, "topic", rule.Topic, "error", err)
                errorCount++
                continue
            }
            
            validRules = append(validRules, rule)
            l.logger.Debug("validated rule successfully", "file", filePath, "index", i, "topic", rule.Topic, "isPattern", containsWildcards(rule.Topic))
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

// validateRule performs comprehensive validation of rule configuration including wildcard patterns
func (l *RulesLoader) validateRule(rule *Rule, filePath string, ruleIndex int) error {
    if rule == nil {
        return fmt.Errorf("rule cannot be nil")
    }

    if rule.Topic == "" {
        return fmt.Errorf("rule topic cannot be empty")
    }

    // Validate wildcard patterns if present
    if containsWildcards(rule.Topic) {
        if err := l.validateWildcardPattern(rule.Topic); err != nil {
            return fmt.Errorf("invalid wildcard pattern '%s': %w", rule.Topic, err)
        }
        
        l.logger.Debug("validated wildcard pattern", "topic", rule.Topic, "file", filePath, "index", ruleIndex)
    }

    if rule.Action == nil {
        return fmt.Errorf("rule action cannot be nil")
    }

    if rule.Action.Topic == "" {
        return fmt.Errorf("action topic cannot be empty")
    }

    // Validate action topic for wildcard patterns too - using Info instead of Warn
    if containsWildcards(rule.Action.Topic) {
        l.logger.Info("action topic contains wildcards - ensure this is intentional", "actionTopic", rule.Action.Topic, "ruleTopic", rule.Topic, "file", filePath, "index", ruleIndex)
    }

    if rule.Conditions != nil {
        if err := l.validateConditions(rule.Conditions); err != nil {
            return fmt.Errorf("invalid conditions: %w", err)
        }
    }

    return nil
}

// validateWildcardPattern validates NATS wildcard pattern syntax
func (l *RulesLoader) validateWildcardPattern(topic string) error {
    // Use the existing ValidatePattern function from pattern.go
    if err := ValidatePattern(topic); err != nil {
        return err
    }
    
    // Additional validation for common mistakes
    if strings.Contains(topic, "**") {
        return fmt.Errorf("double wildcards '**' are not valid, use '>' for multi-level wildcards")
    }
    
    // Check for empty tokens which are common mistakes
    if strings.Contains(topic, "..") {
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

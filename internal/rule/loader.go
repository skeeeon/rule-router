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

	var rules []Rule

	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error accessing path %s: %w", path, err)
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		l.logger.Debug("loading rule file", "path", path)

		data, err := os.ReadFile(path)
		if err != nil {
			l.logger.Error("failed to read rule file",
				"path", path,
				"error", err)
			return fmt.Errorf("failed to read rule file %s: %w", path, err)
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
			l.logger.Error("failed to parse rule file",
				"path", path,
				"error", parseErr)
			return fmt.Errorf("failed to parse rule file %s: %w", path, parseErr)
		}

		l.logger.Debug("successfully loaded rules",
			"path", path,
			"count", len(ruleSet))

		// Validate rules before adding them
		for i, rule := range ruleSet {
			if err := validateRule(&rule); err != nil {
				l.logger.Error("invalid rule configuration",
					"path", path,
					"ruleIndex", i,
					"error", err)
				return fmt.Errorf("invalid rule in file %s at index %d: %w", path, i, err)
			}
			rules = append(rules, rule)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	l.logger.Info("rules loaded successfully",
		"totalRules", len(rules))

	return rules, nil
}

// validateRule performs basic validation of rule configuration
func validateRule(rule *Rule) error {
	if rule == nil {
		return fmt.Errorf("rule cannot be nil")
	}

	if rule.Topic == "" {
		return fmt.Errorf("rule topic cannot be empty")
	}

	if rule.Action == nil {
		return fmt.Errorf("rule action cannot be nil")
	}

	if rule.Action.Topic == "" {
		return fmt.Errorf("action topic cannot be empty")
	}

	if rule.Conditions != nil {
		if err := validateConditions(rule.Conditions); err != nil {
			return fmt.Errorf("invalid conditions: %w", err)
		}
	}

	return nil
}

// validateConditions recursively validates condition groups
func validateConditions(conditions *Conditions) error {
	if conditions == nil {
		return fmt.Errorf("conditions cannot be nil")
	}

	if conditions.Operator != "and" && conditions.Operator != "or" {
		return fmt.Errorf("invalid operator: %s", conditions.Operator)
	}

	// Validate individual conditions
	for _, condition := range conditions.Items {
		if condition.Field == "" {
			return fmt.Errorf("condition field cannot be empty")
		}
		if !isValidOperator(condition.Operator) {
			return fmt.Errorf("invalid condition operator: %s", condition.Operator)
		}
	}

	// Recursively validate nested condition groups
	for _, group := range conditions.Groups {
		if err := validateConditions(&group); err != nil {
			return fmt.Errorf("invalid nested condition group: %w", err)
		}
	}

	return nil
}

// isValidOperator checks if the operator is supported
func isValidOperator(op string) bool {
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

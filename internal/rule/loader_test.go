package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"rule-router/internal/logger"
)

// newTestLoader creates a loader with a nop logger and predefined KV buckets for testing.
func newTestLoader() *RulesLoader {
	// Pre-configure some buckets to test KV validation logic.
	configuredBuckets := []string{"device_status", "device_config", "customer_data"}
	return NewRulesLoader(logger.NewNopLogger(), configuredBuckets)
}

// helper function to create a temporary rule file.
func createTempRuleFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write temp file %s: %v", path, err)
	}
}

// TestNewRulesLoader verifies the constructor.
func TestNewRulesLoader(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		loader := newTestLoader()
		if loader == nil {
			t.Fatal("NewRulesLoader returned nil")
		}
		if loader.logger == nil {
			t.Error("Logger was not initialized")
		}
		if !loader.configuredBuckets["device_status"] {
			t.Error("Configured KV buckets were not set correctly")
		}
	})

	t.Run("nil logger", func(t *testing.T) {
		loader := NewRulesLoader(nil, []string{})
		if loader != nil {
			t.Error("NewRulesLoader should return nil if logger is nil")
		}
	})
}

// TestLoadFromDirectory_SuccessCases tests successful loading scenarios.
func TestLoadFromDirectory_SuccessCases(t *testing.T) {
	loader := newTestLoader()
	tempDir := t.TempDir()

	createTempRuleFile(t, tempDir, "rule1.yaml", `
- subject: sensors.temp
  action:
    subject: alerts.temp
    payload: "{}"`)

	createTempRuleFile(t, tempDir, "rule2.json", `
[
  {
    "subject": "sensors.humidity",
    "action": { "subject": "alerts.humidity", "payload": "{}" }
  }
]`)

	createTempRuleFile(t, tempDir, "multi_rule.yml", `
- subject: multi.1
  action:
    subject: out.1
    payload: "{}"
- subject: multi.2
  action:
    subject: out.2
    payload: "{}"`)

	rules, err := loader.LoadFromDirectory(tempDir)
	if err != nil {
		t.Fatalf("LoadFromDirectory() returned unexpected error: %v", err)
	}

	if len(rules) != 4 {
		t.Errorf("Expected to load 4 rules, but got %d", len(rules))
	}
}

// TestLoadFromDirectory_FileHandling tests how the loader handles different files and directories.
func TestLoadFromDirectory_FileHandling(t *testing.T) {
	loader := newTestLoader()

	t.Run("non-existent directory", func(t *testing.T) {
		_, err := loader.LoadFromDirectory("non_existent_dir")
		if err == nil {
			t.Fatal("Expected an error for a non-existent directory, but got nil")
		}
	})

	t.Run("ignores non-rule files", func(t *testing.T) {
		tempDir := t.TempDir()
		createTempRuleFile(t, tempDir, "rule.yaml", `
- subject: a
  action:
    subject: b
    payload: ""`)
		createTempRuleFile(t, tempDir, "README.md", "This is a readme.")
		createTempRuleFile(t, tempDir, "config.txt", "some config")

		rules, err := loader.LoadFromDirectory(tempDir)
		if err != nil {
			t.Fatalf("LoadFromDirectory() returned unexpected error: %v", err)
		}
		if len(rules) != 1 {
			t.Errorf("Expected 1 rule to be loaded, got %d", len(rules))
		}
	})

	t.Run("skips _test directories", func(t *testing.T) {
		tempDir := t.TempDir()
		createTempRuleFile(t, tempDir, "main_rule.yaml", `
- subject: a
  action:
    subject: b
    payload: ""`)

		testSubDir := filepath.Join(tempDir, "main_rule_test")
		if err := os.Mkdir(testSubDir, 0755); err != nil {
			t.Fatalf("Failed to create test subdir: %v", err)
		}
		createTempRuleFile(t, testSubDir, "test_rule.yaml", `
- subject: test.subject
  action:
    subject: test.out
    payload: ""`)

		rules, err := loader.LoadFromDirectory(tempDir)
		if err != nil {
			t.Fatalf("LoadFromDirectory() returned unexpected error: %v", err)
		}
		if len(rules) != 1 {
			t.Errorf("Expected 1 rule to be loaded (and _test dir ignored), but got %d", len(rules))
		}
	})
}

// TestLoadFromDirectory_ParsingErrors tests handling of malformed files.
func TestLoadFromDirectory_ParsingErrors(t *testing.T) {
	loader := newTestLoader()
	tempDir := t.TempDir()

	createTempRuleFile(t, tempDir, "bad.yaml", `
- subject: valid.subject
  action:
    subject: valid.action
    payload: ""
this: is: completely: invalid: yaml
`)

	_, err := loader.LoadFromDirectory(tempDir)
	if err == nil {
		t.Fatal("Expected a parsing error for malformed YAML, but got nil")
	}
}

// TestLoadFromDirectory_ValidationErrors tests that invalid rules within a file are skipped.
func TestLoadFromDirectory_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		errMsg      string
	}{
		{name: "empty subject", ruleContent: `- subject: ""
  action: {subject: a, payload: ""}`, errMsg: "rule subject cannot be empty"},
		{name: "nil action", ruleContent: `- subject: a`, errMsg: "rule action cannot be nil"},
		{name: "empty action subject", ruleContent: `- subject: a
  action: {subject: "", payload: ""}`, errMsg: "action subject cannot be empty"},
		{name: "invalid condition operator", ruleContent: `- subject: a
  conditions: {operator: "xor"}
  action: {subject: b, payload: ""}`, errMsg: "invalid operator: xor"},
		{name: "invalid condition item operator", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: f, operator: "equals", value: "v"}]}
  action: {subject: b, payload: ""}`, errMsg: "invalid condition operator 'equals'"},
		{name: "invalid subject field (non-numeric)", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: "@subject.abc", operator: "exists"}]}
  action: {subject: b, payload: ""}`, errMsg: "invalid subject field token accessor 'abc'"},
		{name: "invalid subject field (negative index)", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: "@subject.-1", operator: "exists"}]}
  action: {subject: b, payload: ""}`, errMsg: "subject token index cannot be negative"},
		{
			name: "valid_high_subject_field_index_(FIX_VERIFICATION)",
			// FIX: Added a valid 'operator' to make the condition logically valid.
			ruleContent: `- subject: a
  conditions:
    operator: "and"
    items:
      - field: "@subject.15"
        operator: "exists"
  action:
    subject: b
    payload: ""`,
			errMsg: "", // This one should NOT produce an error
		},
		{name: "KV field missing colon", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: "@kv.device_status.key.path", operator: "exists"}]}
  action: {subject: b, payload: ""}`, errMsg: "must use ':' to separate key from JSON path"},
		{name: "KV field with variable in bucket", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: "@kv.{bucket}.key:path", operator: "exists"}]}
  action: {subject: b, payload: ""}`, errMsg: "variables in bucket names are not supported"},
		{name: "KV field with unconfigured bucket", ruleContent: `- subject: a
  conditions: {operator: "and", items: [{field: "@kv.unconfigured_bucket.key:path", operator: "exists"}]}
  action: {subject: b, payload: ""}`, errMsg: "KV bucket 'unconfigured_bucket' not configured"},
		{name: "KV field in template missing colon", ruleContent: `- subject: a
  action: {subject: "out.{@kv.device_status.key.path}", payload: ""}`, errMsg: "KV field in template must use ':' delimiter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()

			fullContent := fmt.Sprintf("- subject: valid\n  action: {subject: valid, payload: \"\"}\n%s", tt.ruleContent)
			createTempRuleFile(t, tempDir, "test.yaml", fullContent)

			rules, err := loader.LoadFromDirectory(tempDir)
			if err != nil {
				t.Fatalf("LoadFromDirectory returned an unexpected fatal error: %v", err)
			}

			if tt.errMsg != "" {
				if len(rules) != 1 {
					t.Errorf("Expected 1 valid rule to be loaded (and invalid rule skipped), but got %d. Error case: '%s'", len(rules), tt.errMsg)
				}
			} else {
				if len(rules) != 2 {
					t.Errorf("Expected 2 valid rules to be loaded, but got %d.", len(rules))
				}
			}
		})
	}
}

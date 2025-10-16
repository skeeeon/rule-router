// file: internal/rule/loader_test.go

package rule

import (
	"os"
	"path/filepath"
	"strings"
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

		// Correctly check if the slice contains the expected bucket.
		found := false
		for _, bucket := range loader.configuredKVBuckets {
			if bucket == "device_status" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Configured KV buckets were not set correctly")
		}
	})
}

// TestLoadFromDirectory_SuccessCases tests successful loading scenarios.
func TestLoadFromDirectory_SuccessCases(t *testing.T) {
	loader := newTestLoader()
	tempDir := t.TempDir()

	// UPDATED: YAML content now uses the new trigger/action structure.
	createTempRuleFile(t, tempDir, "rule1.yaml", `
- trigger:
    nats:
      subject: sensors.temp
  action:
    nats:
      subject: alerts.temp
      payload: "{}"`)

	createTempRuleFile(t, tempDir, "rule2.yml", `
- trigger:
    nats:
      subject: sensors.humidity
  action:
    nats:
      subject: alerts.humidity
      payload: "{}"`)

	createTempRuleFile(t, tempDir, "multi_rule.yml", `
- trigger:
    nats:
      subject: multi.1
  action:
    nats:
      subject: out.1
      payload: "{}"
- trigger:
    nats:
      subject: multi.2
  action:
    nats:
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
- trigger:
    nats:
      subject: a
  action:
    nats:
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

	// REINSTATED AND CORRECTED: This test now verifies the loader skips test directories.
	t.Run("skips _test directories", func(t *testing.T) {
		tempDir := t.TempDir()
		createTempRuleFile(t, tempDir, "main_rule.yaml", `
- trigger:
    nats:
      subject: a
  action:
    nats:
      subject: b
      payload: ""`)

		testSubDir := filepath.Join(tempDir, "main_rule_test")
		if err := os.Mkdir(testSubDir, 0755); err != nil {
			t.Fatalf("Failed to create test subdir: %v", err)
		}
		createTempRuleFile(t, testSubDir, "test_rule.yaml", `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
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
- trigger:
    nats:
      subject: valid.subject
  action:
    nats:
      subject: valid.action
      payload: ""
this: is: completely: invalid: yaml
`)

	_, err := loader.LoadFromDirectory(tempDir)
	if err == nil {
		t.Fatal("Expected a parsing error for malformed YAML, but got nil")
	}
}

// TestLoadFromDirectory_ValidationErrors tests that invalid rules are correctly rejected.
func TestLoadFromDirectory_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		errMsg      string
	}{
		{
			name: "empty nats trigger subject",
			ruleContent: `- trigger: { nats: { subject: "" } }
  action: { nats: { subject: a, payload: "" } }`,
			errMsg: "NATS trigger subject cannot be empty",
		},
		{
			name: "no action specified",
			ruleContent: `- trigger: { nats: { subject: a } }`,
			errMsg: "rule must have either a NATS or HTTP action",
		},
		{
			name: "empty nats action subject",
			ruleContent: `- trigger: { nats: { subject: a } }
  action: { nats: { subject: "", payload: "" } }`,
			errMsg: "NATS action subject cannot be empty",
		},
		{
			name: "invalid condition operator",
			ruleContent: `- trigger: { nats: { subject: a } }
  conditions: { operator: "xor" }
  action: { nats: { subject: b, payload: "" } }`,
			errMsg: "invalid operator: xor",
		},
		{
			name: "invalid condition item operator",
			ruleContent: `- trigger: { nats: { subject: a } }
  conditions: { operator: "and", items: [{field: f, operator: "equals", value: "v"}] }
  action: { nats: { subject: b, payload: "" } }`,
			errMsg: "invalid condition operator 'equals'",
		},
		{
			name: "invalid subject field (non-numeric)",
			ruleContent: `- trigger: { nats: { subject: a } }
  conditions: { operator: "and", items: [{field: "@subject.abc", operator: "exists"}] }
  action: { nats: { subject: b, payload: "" } }`,
			errMsg: "invalid subject field format",
		},
		{
			name: "KV field missing colon",
			ruleContent: `- trigger: { nats: { subject: a } }
  conditions: { operator: "and", items: [{field: "@kv.device_status.key.path", operator: "exists"}] }
  action: { nats: { subject: b, payload: "" } }`,
			errMsg: "must use ':' to separate key from JSON path",
		},
		{
			name: "KV field in template missing colon",
			ruleContent: `- trigger: { nats: { subject: a } }
  action: { nats: { subject: "out.{@kv.device_status.key.path}", payload: "" } }`,
			errMsg: "KV field must use ':' to separate key from JSON path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)
			if err == nil {
				t.Fatalf("Expected validation error containing '%s', but got nil", tt.errMsg)
			}
			if !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
			}
		})
	}
}

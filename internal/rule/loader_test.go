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
		// MODIFIED: Removed tests for missing colon, as it's now valid.
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

// NEW: Dedicated test suite for KV field validation.
func TestKVField_Validation(t *testing.T) {
	tests := []struct {
		name        string
		field       string
		shouldPass  bool
		errMsg      string
	}{
		{
			name:       "valid with path",
			field:      "@kv.device_status.key:path.to.field",
			shouldPass: true,
		},
		{
			name:       "valid without path",
			field:      "@kv.device_status.key",
			shouldPass: true,
		},
		{
			name:       "valid with trailing colon",
			field:      "@kv.device_status.key:",
			shouldPass: true,
		},
		{
			name:       "valid with dots in key and path",
			field:      "@kv.device_config.sensor.temp.001:thresholds.max",
			shouldPass: true,
		},
		{
			name:       "valid with dots in key and no path",
			field:      "@kv.device_config.sensor.temp.001",
			shouldPass: true,
		},
		{
			name:       "invalid - multiple colons",
			field:      "@kv.bucket.key:path:extra",
			shouldPass: false,
			errMsg:     "must contain at most one ':' delimiter",
		},
		{
			name:       "invalid - missing key",
			field:      "@kv.bucket.:path",
			shouldPass: false,
			errMsg:     "KV key name cannot be empty",
		},
		{
			name:       "invalid - missing bucket",
			field:      "@kv..key:path",
			shouldPass: false,
			errMsg:     "KV bucket name cannot be empty",
		},
		{
			name:       "invalid - missing bucket and key",
			field:      "@kv.:path",
			shouldPass: false,
			errMsg:     "must have 'bucket.key' before ':'",
		},
		{
			name:       "invalid - only bucket, no key, no path",
			field:      "@kv.bucket",
			shouldPass: false,
			errMsg:     "must have 'bucket.key' format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			ruleContent := `- trigger: { nats: { subject: a } }
  conditions: { operator: "and", items: [{field: "` + tt.field + `", operator: "exists"}] }
  action: { nats: { subject: b, payload: "" } }`
			
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}


// ========================================
// ARRAY OPERATOR VALIDATION TESTS
// ========================================

// TestArrayOperator_Validation tests validation of array operators (any/all/none)
func TestArrayOperator_Validation(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		shouldPass  bool
		errMsg      string
	}{
		{
			name: "valid any operator with nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: notifications
        operator: any
        conditions:
          operator: and
          items:
            - field: type
              operator: eq
              value: CRITICAL
  action:
    nats:
      subject: alerts.critical
      payload: "{}"`,
			shouldPass: true,
		},
		{
			name: "valid all operator with nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: all
        conditions:
          operator: and
          items:
            - field: status
              operator: eq
              value: active
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: true,
		},
		{
			name: "valid none operator with nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: none
        conditions:
          operator: and
          items:
            - field: status
              operator: eq
              value: error
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: true,
		},
		{
			name: "any operator missing nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: any
        value: something
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: false,
			errMsg:     "array operator 'any' requires nested conditions",
		},
		{
			name: "all operator missing nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: all
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: false,
			errMsg:     "array operator 'all' requires nested conditions",
		},
		{
			name: "none operator missing nested conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: none
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: false,
			errMsg:     "array operator 'none' requires nested conditions",
		},
		{
			name: "array operator with invalid nested operator",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: any
        conditions:
          operator: xor
          items:
            - field: status
              operator: eq
              value: active
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: false,
			errMsg:     "invalid operator: xor",
		},
		{
			name: "array operator with invalid nested condition operator",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: items
        operator: any
        conditions:
          operator: and
          items:
            - field: status
              operator: equals
              value: active
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: false,
			errMsg:     "invalid condition operator 'equals'",
		},
		{
			name: "nested array operators",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: outer
        operator: any
        conditions:
          operator: and
          items:
            - field: inner
              operator: all
              conditions:
                operator: and
                items:
                  - field: status
                    operator: eq
                    value: active
  action:
    nats:
      subject: output
      payload: "{}"`,
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

// ========================================
// FOREACH VALIDATION TESTS
// ========================================

// TestForEach_NATS_Validation tests forEach validation for NATS actions
func TestForEach_NATS_Validation(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		shouldPass  bool
		errMsg      string
	}{
		{
			name: "valid forEach without filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: notifications
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "valid forEach with filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: notifications
      filter:
        operator: and
        items:
          - field: type
            operator: eq
            value: CRITICAL
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "valid forEach with nested path",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: data.items.notifications
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "forEach with wildcard (should fail)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items.*
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "forEach field cannot contain wildcards",
		},
		{
			name: "forEach with greedy wildcard (should fail)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items.>
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "forEach field cannot contain wildcards",
		},
		{
			name: "forEach with invalid filter conditions",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items
      filter:
        operator: invalid_op
        items:
          - field: status
            operator: eq
            value: active
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "invalid operator: invalid_op",
		},
		{
			name: "forEach with invalid filter condition operator",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items
      filter:
        operator: and
        items:
          - field: status
            operator: equals
            value: active
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "invalid condition operator 'equals'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

// TestForEach_HTTP_Validation tests forEach validation for HTTP actions
func TestForEach_HTTP_Validation(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		shouldPass  bool
		errMsg      string
	}{
		{
			name: "valid HTTP forEach without filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    http:
      forEach: items
      url: https://api.example.com/items/{id}
      method: POST
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "valid HTTP forEach with filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    http:
      forEach: items
      filter:
        operator: and
        items:
          - field: status
            operator: eq
            value: pending
      url: https://api.example.com/items/{id}
      method: POST
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "HTTP forEach with wildcard (should fail)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    http:
      forEach: items.*
      url: https://api.example.com/items/{id}
      method: POST
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "forEach field cannot contain wildcards",
		},
		{
			name: "HTTP forEach with invalid filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    http:
      forEach: items
      filter:
        operator: and
        items:
          - field: status
            operator: invalid_op
            value: active
      url: https://api.example.com/items/{id}
      method: POST
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "invalid condition operator 'invalid_op'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

// TestForEach_MutualExclusivity tests that forEach and passthrough work correctly together
func TestForEach_MutualExclusivity(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		shouldPass  bool
		errMsg      string
	}{
		{
			name: "forEach with passthrough (should work)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items
      subject: output.{id}
      passthrough: true`,
			shouldPass: true,
		},
		{
			name: "forEach with payload (should work)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      forEach: items
      subject: output.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: true,
		},
		{
			name: "passthrough with payload but no forEach (should fail)",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  action:
    nats:
      subject: output
      payload: '{"id": "{id}"}'
      passthrough: true`,
			shouldPass: false,
			errMsg:     "cannot specify both 'payload' and 'passthrough: true'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

// ========================================
// COMBINED FEATURE TESTS
// ========================================

// TestArrayOperator_And_ForEach_Combined tests rules using both features
func TestArrayOperator_And_ForEach_Combined(t *testing.T) {
	tests := []struct {
		name        string
		ruleContent string
		shouldPass  bool
		errMsg      string
	}{
		{
			name: "valid: array operator in condition + forEach in action",
			ruleContent: `
- trigger:
    nats:
      subject: device.events
  conditions:
    operator: and
    items:
      - field: notifications
        operator: any
        conditions:
          operator: and
          items:
            - field: type
              operator: eq
              value: CRITICAL
  action:
    nats:
      forEach: notifications
      filter:
        operator: and
        items:
          - field: type
            operator: eq
            value: CRITICAL
      subject: alerts.{id}
      payload: '{"id": "{id}", "type": "{type}"}'`,
			shouldPass: true,
		},
		{
			name: "valid: nested array operator + forEach with complex filter",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: events
        operator: any
        conditions:
          operator: or
          items:
            - field: severity
              operator: eq
              value: high
            - field: priority
              operator: gt
              value: 5
  action:
    nats:
      forEach: events
      filter:
        operator: and
        items:
          - field: severity
            operator: eq
            value: high
          - field: acknowledged
            operator: eq
            value: false
      subject: alerts.{event_id}
      payload: '{"event_id": "{event_id}", "severity": "{severity}"}'`,
			shouldPass: true,
		},
		{
			name: "invalid: array operator missing conditions + forEach valid",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: notifications
        operator: any
  action:
    nats:
      forEach: notifications
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "array operator 'any' requires nested conditions",
		},
		{
			name: "invalid: valid array operator + forEach with wildcard",
			ruleContent: `
- trigger:
    nats:
      subject: test.subject
  conditions:
    operator: and
    items:
      - field: notifications
        operator: any
        conditions:
          operator: and
          items:
            - field: type
              operator: eq
              value: CRITICAL
  action:
    nats:
      forEach: notifications.*
      subject: alerts.{id}
      payload: '{"id": "{id}"}'`,
			shouldPass: false,
			errMsg:     "forEach field cannot contain wildcards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := newTestLoader()
			tempDir := t.TempDir()
			createTempRuleFile(t, tempDir, "test.yaml", tt.ruleContent)

			_, err := loader.LoadFromDirectory(tempDir)

			if tt.shouldPass {
				if err != nil {
					t.Errorf("Expected rule to pass validation, but got error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("Expected validation error containing '%s', but got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', but got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

// TestRealWorldScenario_BatchNotifications tests the complete real-world use case
func TestRealWorldScenario_BatchNotifications(t *testing.T) {
	loader := newTestLoader()
	tempDir := t.TempDir()

	// Complete real-world rule from implementation plan
	ruleContent := `
- trigger:
    nats:
      subject: device.notifications
  conditions:
    operator: and
    items:
      - field: type
        operator: eq
        value: NOTIFICATION
      - field: notification
        operator: any
        conditions:
          operator: and
          items:
            - field: type
              operator: eq
              value: DEVICE_MOTION_START
  action:
    nats:
      forEach: notification
      filter:
        operator: and
        items:
          - field: type
            operator: eq
            value: DEVICE_MOTION_START
      subject: alerts.motion.{originatingServerId}.{event.alarmId}
      payload: |
        {
          "alertType": "motion_detected",
          "alarmId": "{event.alarmId}",
          "alarmName": "{event.alarmName}",
          "camera": "{cameraId}",
          "eventMessage": "{event.alarmTrigger.triggerEvent.eventMsg}",
          "timestamp": "{event.alarmTrigger.timestamp}",
          "originatingServer": "{originatingServerId}",
          "siteId": "{@msg.siteId}",
          "notificationTime": "{@msg.time}",
          "processedAt": "{@timestamp()}"
        }`

	createTempRuleFile(t, tempDir, "batch_notifications.yaml", ruleContent)

	rules, err := loader.LoadFromDirectory(tempDir)
	if err != nil {
		t.Fatalf("Real-world rule failed validation: %v", err)
	}

	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}

	// Verify the rule structure
	rule := rules[0]
	if rule.Trigger.NATS == nil {
		t.Error("Expected NATS trigger")
	}
	if rule.Conditions == nil {
		t.Error("Expected conditions")
	}
	if rule.Action.NATS == nil {
		t.Error("Expected NATS action")
	}
	if rule.Action.NATS.ForEach != "notification" {
		t.Errorf("Expected forEach='notification', got '%s'", rule.Action.NATS.ForEach)
	}
	if rule.Action.NATS.Filter == nil {
		t.Error("Expected forEach filter")
	}
}

// TestOperatorWhitelist_IncludesArrayOperators verifies array operators are in whitelist
func TestOperatorWhitelist_IncludesArrayOperators(t *testing.T) {
	loader := newTestLoader()

	// Test that all three array operators are accepted
	operators := []string{"any", "all", "none"}

	for _, op := range operators {
		t.Run("operator_"+op, func(t *testing.T) {
			if !loader.isValidOperator(op) {
				t.Errorf("Array operator '%s' should be in whitelist", op)
			}
		})
	}

	// Also verify existing operators still work
	existingOps := []string{"eq", "neq", "gt", "lt", "gte", "lte", "contains", "in", "exists", "recent"}
	for _, op := range existingOps {
		t.Run("existing_operator_"+op, func(t *testing.T) {
			if !loader.isValidOperator(op) {
				t.Errorf("Existing operator '%s' should still be in whitelist", op)
			}
		})
	}

	// Verify invalid operators are still rejected
	invalidOps := []string{"equals", "notequals", "xor", "invalid"}
	for _, op := range invalidOps {
		t.Run("invalid_operator_"+op, func(t *testing.T) {
			if loader.isValidOperator(op) {
				t.Errorf("Invalid operator '%s' should not be in whitelist", op)
			}
		})
	}
}

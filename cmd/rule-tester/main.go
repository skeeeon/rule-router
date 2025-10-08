package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// TestConfig holds optional test-specific configurations
type TestConfig struct {
	Subject  string `json:"subject"`
	MockTime string `json:"mockTime"`
}

// ExpectedOutput defines the structure for output validation files
type ExpectedOutput struct {
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
}

func main() {
	// CLI Flags
	lint := flag.Bool("lint", false, "Run in Linter mode. Requires --rules.")
	scaffold := flag.String("scaffold", "", "Run in Scaffold mode. Provide path to a rule file.")
	test := flag.Bool("test", false, "Run in Batch Test mode. Requires --rules.")
	rulePath := flag.String("rule", "", "Path to a single rule file for Quick Check mode.")
	messagePath := flag.String("message", "", "Path to a single message file for Quick Check mode.")
	rulesDir := flag.String("rules", "", "Path to the root directory for rules (used for --lint and --test).")
	flag.Parse()

	if *lint && *rulesDir != "" {
		runLinter(*rulesDir)
	} else if *scaffold != "" {
		runScaffold(*scaffold)
	} else if *test && *rulesDir != "" {
		runBatchTest(*rulesDir)
	} else if *rulePath != "" && *messagePath != "" {
		runQuickCheck(*rulePath, *messagePath)
	} else {
		fmt.Println("Invalid usage. Please use one of the following modes:")
		fmt.Println("  --lint --rules <dir>       : Validate syntax of all rules in a directory.")
		fmt.Println("  --scaffold <rule_file>   : Create a test directory and placeholder files for a rule.")
		fmt.Println("  --test --rules <dir>       : Run all batch tests found in a directory.")
		fmt.Println("  --rule <file> --message <file> : Run a single quick check.")
		os.Exit(1)
	}
}

// --- Mode Implementations ---

func runLinter(rulesDir string) {
	fmt.Printf("▶ LINTING rules in %s\n\n", rulesDir)
	var failed bool

	filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			// In lint mode, we just need to validate the file itself, not its whole directory.
			if _, err := loadSingleRuleFile(path); err != nil {
				fmt.Printf("✖ FAIL: %s\n  Error: %v\n", path, err)
				failed = true
			} else {
				fmt.Printf("✔ PASS: %s\n", path)
			}
		}
		return nil
	})

	if failed {
		os.Exit(1)
	}
	fmt.Println("\nLinting complete. All files are valid.")
}


func runScaffold(rulePath string) {
	if !strings.HasSuffix(rulePath, ".yaml") && !strings.HasSuffix(rulePath, ".yml") {
		fmt.Println("Error: --scaffold flag requires a path to a .yaml rule file.")
		os.Exit(1)
	}

	testDir := strings.TrimSuffix(rulePath, filepath.Ext(rulePath)) + "_test"
	if err := os.MkdirAll(testDir, 0755); err != nil {
		fmt.Printf("Error creating directory %s: %v\n", testDir, err)
		os.Exit(1)
	}

	// --- Smart Scaffolding Logic ---
	// Create _test_config.json with the rule's subject
	rules, err := loadSingleRuleFile(rulePath)
	if err != nil || len(rules) == 0 {
		fmt.Printf("Warning: Could not read subject from rule file %s. A default will be used.\n", rulePath)
	} else {
		subject := rules[0].Subject
		var content string
		if strings.Contains(subject, "*") || strings.Contains(subject, ">") {
			content = fmt.Sprintf("{\n  // IMPORTANT: Please replace this placeholder with a concrete subject for your test.\n  \"subject\": \"%s\"\n}\n", "subject")
		} else {
			content = fmt.Sprintf("{\n  \"subject\": \"%s\"\n}\n", subject)
		}
		ioutil.WriteFile(filepath.Join(testDir, "_test_config.json"), []byte(content), 0644)
	}

	// Create placeholder message files
	ioutil.WriteFile(filepath.Join(testDir, "match_1.json"), []byte("{}\n"), 0644)
	ioutil.WriteFile(filepath.Join(testDir, "not_match_1.json"), []byte("{}\n"), 0644)

	fmt.Printf("✔ Scaffolded test directory at: %s\n", testDir)
	fmt.Println("  - _test_config.json")
	fmt.Println("  - match_1.json")
	fmt.Println("  - not_match_1.json")
	fmt.Println("\n💡 Tip:")
	fmt.Println("   - To validate the action's final output, create a corresponding 'match_1_output.json'.")
	fmt.Println("   - For dependencies, add 'mock_kv_data.json'.")
}


func runQuickCheck(rulePath, messagePath string) {
	// For a quick check, we use default mocks
	testConfig := &TestConfig{Subject: "test.subject"}
	processor := setupTestProcessor(rulePath, nil, testConfig)

	// Load the message
	msgBytes, err := ioutil.ReadFile(messagePath)
	if err != nil {
		fmt.Printf("Error reading message file %s: %v\n", messagePath, err)
		os.Exit(1)
	}

	// Process
	actions, err := processor.ProcessWithSubject(testConfig.Subject, msgBytes)
	if err != nil {
		fmt.Printf("Error during processing: %v\n", err)
		os.Exit(1)
	}

	// Report
	if len(actions) > 0 {
		fmt.Println("Rule Matched: True")
		for _, action := range actions {
			fmt.Println("\n--- Rendered Action ---")
			fmt.Printf("Subject: %s\n", action.Subject)
			fmt.Printf("Payload: %s\n", action.Payload)
			fmt.Println("-----------------------")
		}
	} else {
		fmt.Println("Rule Matched: False")
	}
}

func runBatchTest(rulesDir string) {
	fmt.Printf("▶ RUNNING TESTS in %s\n\n", rulesDir)
	var total, passed, failed int

	filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			testDir := strings.TrimSuffix(path, filepath.Ext(path)) + "_test"
			if _, err := os.Stat(testDir); !os.IsNotExist(err) {
				fmt.Printf("=== RULE: %s ===\n", path)
				// Load mocks for this suite
				kvData := loadMockKV(filepath.Join(testDir, "mock_kv_data.json"))
				testConfig := loadTestConfig(filepath.Join(testDir, "_test_config.json"))
				processor := setupTestProcessor(path, kvData, testConfig)

				// Find and run test cases
				testFiles, _ := filepath.Glob(filepath.Join(testDir, "*.json"))
				for _, testFile := range testFiles {
					baseName := filepath.Base(testFile)
					if strings.HasSuffix(baseName, "_output.json") || baseName == "mock_kv_data.json" || baseName == "_test_config.json" {
						continue // Skip output, config and mock files
					}

					total++
					result, err := runSingleTestCase(processor, testFile, testConfig.Subject)
					if err != nil {
						fmt.Printf("  ✖ %s\n    Error: %v\n", baseName, err)
						failed++
					} else if result {
						fmt.Printf("  ✔ %s\n", baseName)
						passed++
					} else {
						// This case should not be hit if runSingleTestCase always returns an error on failure
						fmt.Printf("  ✖ %s\n    Error: Assertion failed without a specific error message.\n", baseName)
						failed++
					}
				}
				fmt.Println()
			}
		}
		return nil
	})

	fmt.Println("--- SUMMARY ---")
	fmt.Printf("Total Tests: %d, Passed: %d, Failed: %d\n", total, passed, failed)
	if failed > 0 {
		os.Exit(1)
	}
}


// --- Helper Functions ---

func runSingleTestCase(processor *rule.Processor, messagePath, subject string) (bool, error) {
	baseName := filepath.Base(messagePath)
	shouldMatch := strings.HasPrefix(baseName, "match_")

	// Load message
	msgBytes, err := ioutil.ReadFile(messagePath)
	if err != nil {
		return false, fmt.Errorf("could not read message file: %w", err)
	}

	// Process
	actions, err := processor.ProcessWithSubject(subject, msgBytes)
	if err != nil {
		return false, fmt.Errorf("processing error: %w", err)
	}

	matched := len(actions) > 0

	// Validate outcome
	if matched != shouldMatch {
		return false, fmt.Errorf("assertion failed: expected match result '%v', but got '%v'", shouldMatch, matched)
	}

	// If it matched and we expect an output, validate it
	if matched {
		outputFile := strings.TrimSuffix(messagePath, ".json") + "_output.json"
		if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
			return validateOutput(actions[0], outputFile)
		}
	}

	return true, nil // Test passed
}


func validateOutput(action *rule.Action, outputFile string) (bool, error) {
	// Read expected output file
	expectedBytes, err := ioutil.ReadFile(outputFile)
	if err != nil {
		return false, fmt.Errorf("could not read expected output file: %w", err)
	}

	var expected ExpectedOutput
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		return false, fmt.Errorf("could not parse expected output file: %w", err)
	}

	// Compare subject
	if action.Subject != expected.Subject {
		return false, fmt.Errorf("subject mismatch: got '%s', want '%s'", action.Subject, expected.Subject)
	}

	// Compare payload by unmarshalling to interface{} to ignore formatting differences
	var actualPayload, expectedPayload interface{}
	if err := json.Unmarshal([]byte(action.Payload), &actualPayload); err != nil {
		return false, fmt.Errorf("could not parse actual action payload: %w", err)
	}
	if err := json.Unmarshal(expected.Payload, &expectedPayload); err != nil {
		return false, fmt.Errorf("could not parse expected payload from output file: %w", err)
	}

	// Marshal back to canonical JSON for a reliable string comparison
	actualCanon, _ := json.Marshal(actualPayload)
	expectedCanon, _ := json.Marshal(expectedPayload)

	if string(actualCanon) != string(expectedCanon) {
		return false, fmt.Errorf("payload mismatch:\ngot:  %s\nwant: %s", string(actualCanon), string(expectedCanon))
	}

	return true, nil
}

func setupTestProcessor(rulePath string, kvData map[string]map[string]interface{}, testConfig *TestConfig) *rule.Processor {
	log := logger.NewNopLogger()
	
	// Load only the specific rule file for the test
	rules, err := loadSingleRuleFile(rulePath)
	if err != nil {
		fmt.Printf("Error loading rule file %s: %v\n", rulePath, err)
		os.Exit(1)
	}
	
	// Setup Mocks
	var kvContext *rule.KVContext
	if kvData != nil {
		cache := rule.NewLocalKVCache(log)
		for bucket, keys := range kvData {
			for key, val := range keys {
				cache.Set(bucket, key, val)
			}
		}
		kvContext = rule.NewKVContext(nil, log, cache)
	}

	processor := rule.NewProcessor(log, nil, kvContext)
	processor.LoadRules(rules)

	var timeProvider rule.TimeProvider = rule.NewSystemTimeProvider()
	if testConfig.MockTime != "" {
		t, err := time.Parse(time.RFC3339, testConfig.MockTime)
		if err != nil {
			fmt.Printf("Error parsing mockTime '%s': %v\n", testConfig.MockTime, err)
		} else {
			timeProvider = rule.NewMockTimeProvider(t)
		}
	}
	processor.SetTimeProvider(timeProvider)

	return processor
}

// loadSingleRuleFile reads and parses a single YAML rule file.
func loadSingleRuleFile(path string) ([]rule.Rule, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read rule file: %w", err)
	}

	var rules []rule.Rule
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to parse yaml rule file: %w", err)
	}

	return rules, nil
}


func loadMockKV(path string) map[string]map[string]interface{} {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil
	}
	var data map[string]map[string]interface{}
	json.Unmarshal(bytes, &data)
	return data
}

func loadTestConfig(path string) *TestConfig {
	config := &TestConfig{Subject: "test.subject"} // Default subject
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return config
	}
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return config
	}
	json.Unmarshal(bytes, &config)
	return config
}

func getBucketKeys(data map[string]map[string]interface{}) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys
}

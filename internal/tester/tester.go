// internal/tester/tester.go

package tester

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// Tester holds the configuration and logic for the rule-tester utility.
type Tester struct {
	Logger          *logger.Logger
	Verbose         bool
	ParallelWorkers int
}

// New creates a new Tester instance.
func New(log *logger.Logger, verbose bool, parallel int) *Tester {
	return &Tester{
		Logger:          log,
		Verbose:         verbose,
		ParallelWorkers: parallel,
	}
}

// --- Mode Implementations ---

// Lint runs the linter mode.
func (t *Tester) Lint(rulesDir string) error {
	fmt.Printf("▶ LINTING rules in %s\n\n", rulesDir)
	var failed bool

	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip _test directories
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
			return filepath.SkipDir
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		if _, err := loadSingleRuleFile(path); err != nil {
			fmt.Printf("✖ FAIL: %s\n  Error: %v\n", path, err)
			failed = true
		} else {
			fmt.Printf("✔ PASS: %s\n", path)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("walk error: %w", err)
	}

	if failed {
		return fmt.Errorf("linting failed")
	}

	fmt.Println("\nLinting complete. All files are valid.")
	return nil
}

// Scaffold runs the scaffold mode.
func (t *Tester) Scaffold(rulePath string, noOverwrite bool) error {
	if !strings.HasSuffix(rulePath, ".yaml") && !strings.HasSuffix(rulePath, ".yml") {
		return fmt.Errorf("--scaffold requires a path to a .yaml rule file")
	}

	testDir := strings.TrimSuffix(rulePath, filepath.Ext(rulePath)) + "_test"

	// Check if directory exists
	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		if noOverwrite {
			return fmt.Errorf("test directory already exists: %s (use without --no-overwrite to proceed)", testDir)
		}

		fmt.Printf("⚠️  Test directory already exists: %s\n", testDir)
		fmt.Printf("   Overwrite? (y/N): ")

		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := os.MkdirAll(testDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", testDir, err)
	}

	rules, err := loadSingleRuleFile(rulePath)
	if err != nil || len(rules) == 0 {
		fmt.Printf("Warning: Could not read subject from rule file %s. Using placeholder.\n", rulePath)
	}

	subject := "subject"
	if err == nil && len(rules) > 0 {
		ruleSubject := rules[0].Subject
		if !strings.Contains(ruleSubject, "*") && !strings.Contains(ruleSubject, ">") {
			subject = ruleSubject
		}
	}

	testConfig := TestConfig{
		Subject:  subject,
		MockTime: time.Now().Format(time.RFC3339),
	}
	configBytes, _ := json.MarshalIndent(testConfig, "", "  ")

	// Use os.WriteFile (Fix A)
	if err := os.WriteFile(filepath.Join(testDir, "_test_config.json"), configBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(testDir, "match_1.json"), []byte("{}\n"), 0644); err != nil {
		return fmt.Errorf("failed to write match file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "not_match_1.json"), []byte("{}\n"), 0644); err != nil {
		return fmt.Errorf("failed to write not_match file: %w", err)
	}

	fmt.Printf("✔ Scaffolded test directory at: %s\n", testDir)
	fmt.Println("  - _test_config.json (populated with subject and mock time)")
	fmt.Println("  - match_1.json")
	fmt.Println("  - not_match_1.json")
	fmt.Println("\n💡 Tip:")
	fmt.Println("   - To validate the action's final output, create a corresponding 'match_1_output.json'.")
	fmt.Println("   - For dependencies, add 'mock_kv_data.json'.")
	return nil
}

// QuickCheck runs the quick check mode.
func (t *Tester) QuickCheck(rulePath, messagePath, subjectOverride, kvMockPath string) error {
	var testSubject string

	if subjectOverride != "" {
		testSubject = subjectOverride
	} else {
		rules, err := loadSingleRuleFile(rulePath)
		if err != nil || len(rules) == 0 {
			return fmt.Errorf("could not load or parse rule file %s: %w", rulePath, err)
		}

		ruleSubject := rules[0].Subject
		if strings.Contains(ruleSubject, "*") || strings.Contains(ruleSubject, ">") {
			testSubject = "subject"
			fmt.Printf("ℹ Warning: Rule subject '%s' contains a wildcard.\n", ruleSubject)
			fmt.Println("  Using placeholder subject 'subject' for this test.")
			fmt.Println("  Use the --subject flag to specify a concrete subject for testing wildcards.")
			fmt.Println()
		} else {
			testSubject = ruleSubject
		}
	}

	// Load KV mock data if provided
	var kvData map[string]map[string]interface{}
	if kvMockPath != "" {
		kvData = loadMockKV(kvMockPath)
		if kvData == nil {
			fmt.Printf("⚠️  Warning: Could not load KV mock data from %s\n", kvMockPath)
		} else {
			fmt.Printf("✓ Loaded KV mock data: %d bucket(s)\n", len(kvData))
		}
	}

	testConfig := &TestConfig{Subject: testSubject}
	processor := setupTestProcessor(rulePath, kvData, testConfig, t.Verbose)

	// Use os.ReadFile (Fix A)
	msgBytes, err := os.ReadFile(messagePath)
	if err != nil {
		return fmt.Errorf("failed to read message file %s: %w", messagePath, err)
	}

	fmt.Printf("▶ Running Quick Check on subject: %s\n\n", testSubject)

	start := time.Now()
	actions, err := processor.ProcessWithSubject(testConfig.Subject, msgBytes)
	duration := time.Since(start)

	if err != nil {
		return fmt.Errorf("processing error: %w", err)
	}

	if len(actions) > 0 {
		fmt.Println("Rule Matched: True")
		fmt.Printf("Processing Time: %v\n", duration)
		for i, action := range actions {
			fmt.Printf("\n--- Rendered Action %d ---\n", i+1)
			fmt.Printf("Subject: %s\n", action.Subject)
			fmt.Printf("Payload: %s\n", action.Payload)
			fmt.Println("-----------------------")
		}
	} else {
		fmt.Println("Rule Matched: False")
		fmt.Printf("Processing Time: %v\n", duration)

		if t.Verbose {
			fmt.Println("\nMessage data:")
			var prettyMsg interface{}
			if err := json.Unmarshal(msgBytes, &prettyMsg); err == nil {
				pretty, _ := json.MarshalIndent(prettyMsg, "  ", "  ")
				fmt.Printf("  %s\n", string(pretty))
			}
		}
	}

	return nil
}

// RunBatchTest runs the batch test mode.
func (t *Tester) RunBatchTest(rulesDir string) (TestSummary, error) {
	startTime := time.Now()

	// 1. Collect all test groups (one per rule file)
	testGroups, err := t.collectTestGroups(rulesDir)
	if err != nil {
		return TestSummary{}, fmt.Errorf("failed to collect test groups: %w", err)
	}

	// 2. Execute tests (parallel or sequential)
	var summary TestSummary
	if t.ParallelWorkers > 0 {
		summary = t.runTestsParallel(testGroups)
	} else {
		summary = t.runTestsSequential(testGroups)
	}

	summary.DurationMs = time.Since(startTime).Milliseconds()

	return summary, nil
}

// runTestsSequential executes tests sequentially, optimizing by reusing the processor per rule group. (Fix B)
func (t *Tester) runTestsSequential(groups []TestGroup) TestSummary {
	summary := TestSummary{}

	for _, group := range groups {
		fmt.Printf("=== RULE: %s ===\n", group.RulePath)

		// Setup processor ONCE per rule file (Fix B)
		processor := setupTestProcessor(group.RulePath, group.KVData, group.TestConfig, false)

		for _, testFile := range group.TestFiles {
			baseName := filepath.Base(testFile)
			summary.Total++

			result := t.runSingleTestCase(processor, testFile, group.TestConfig.Subject)
			summary.Results = append(summary.Results, result)

			if result.Passed {
				summary.Passed++
				fmt.Printf("  ✔ %s (%dms)\n", baseName, result.DurationMs)
			} else {
				summary.Failed++
				fmt.Printf("  ✖ %s (%dms)\n", baseName, result.DurationMs)
				if t.Verbose {
					fmt.Printf("    Error: %s\n", result.Error)
					if result.Details != "" {
						fmt.Printf("    Details: %s\n", strings.ReplaceAll(result.Details, "\n", "\n    "))
					}
				} else {
					fmt.Printf("    Error: %s\n", result.Error)
				}
			}
		}

		fmt.Println()
	}

	return summary
}

// runTestsParallel executes tests in parallel, creating jobs with the pre-initialized processor. (Fix B)
func (t *Tester) runTestsParallel(groups []TestGroup) TestSummary {
	// Create job queue
	jobs := make(chan TestJob, 100)
	results := make(chan TestResult, 100)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < t.ParallelWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				// Worker receives pre-initialized processor
				result := t.runSingleTestCase(j.Processor, j.TestFile, j.Subject)
				results <- result
			}
		}()
	}

	// Send jobs
	go func() {
		for _, group := range groups {
			// Setup processor ONCE per rule file (Fix B)
			processor := setupTestProcessor(group.RulePath, group.KVData, group.TestConfig, false)
			for _, testFile := range group.TestFiles {
				jobs <- TestJob{
					Processor: processor,
					TestFile:  testFile,
					Subject:   group.TestConfig.Subject,
					Verbose:   t.Verbose,
				}
			}
		}
		close(jobs)
	}()

	// Collect results
	go func() {
		wg.Wait()
		close(results)
	}()

	summary := TestSummary{}
	completed := 0
	totalTests := 0
	for _, group := range groups {
		totalTests += len(group.TestFiles)
	}

	for result := range results {
		completed++
		summary.Total++
		summary.Results = append(summary.Results, result)

		if result.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}

		// Progress indication
		fmt.Printf("\r[%d/%d] Tests completed... (Failed: %d)", completed, totalTests, summary.Failed)
	}

	fmt.Printf("\r") // Clear progress line

	return summary
}

// --- Helper Functions ---

// collectTestGroups walks the rules directory and groups test files by rule.
func (t *Tester) collectTestGroups(rulesDir string) ([]TestGroup, error) {
	var testGroups []TestGroup

	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip _test directories themselves
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
			return filepath.SkipDir
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		testDir := strings.TrimSuffix(path, filepath.Ext(path)) + "_test"
		if _, err := os.Stat(testDir); !os.IsNotExist(err) {
			// Load test files
			testFiles, err := filepath.Glob(filepath.Join(testDir, "*.json"))
			if err != nil {
				return fmt.Errorf("failed to list test files in %s: %w", testDir, err)
			}

			// Filter out special files
			var validTests []string
			for _, testFile := range testFiles {
				baseName := filepath.Base(testFile)
				if strings.HasSuffix(baseName, "_output.json") ||
					baseName == "mock_kv_data.json" ||
					baseName == "_test_config.json" {
					continue
				}
				validTests = append(validTests, testFile)
			}

			if len(validTests) > 0 {
				kvData := loadMockKV(filepath.Join(testDir, "mock_kv_data.json"))
				testConfig := loadTestConfig(filepath.Join(testDir, "_test_config.json"))

				testGroups = append(testGroups, TestGroup{
					RulePath:   path,
					TestDir:    testDir,
					TestFiles:  validTests,
					KVData:     kvData,
					TestConfig: testConfig,
				})
			}
		}
		return nil
	})

	return testGroups, err
}

// runSingleTestCase executes a single test against a pre-initialized processor.
func (t *Tester) runSingleTestCase(processor *rule.Processor, messagePath, subject string) TestResult {
	start := time.Now()
	baseName := filepath.Base(messagePath)
	shouldMatch := strings.HasPrefix(baseName, "match_")

	result := TestResult{
		File: baseName,
	}

	// Use os.ReadFile (Fix A)
	msgBytes, err := os.ReadFile(messagePath)
	if err != nil {
		result.Error = fmt.Sprintf("could not read message file: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	actions, err := processor.ProcessWithSubject(subject, msgBytes)
	result.DurationMs = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = fmt.Sprintf("processing error: %v", err)
		return result
	}

	matched := len(actions) > 0
	if matched != shouldMatch {
		result.Error = fmt.Sprintf("expected match=%v, got match=%v", shouldMatch, matched)

		// Add detailed information
		if t.Verbose {
			var msg map[string]interface{}
			json.Unmarshal(msgBytes, &msg)
			details, _ := json.MarshalIndent(msg, "", "  ")
			result.Details = fmt.Sprintf("Message:\n%s", string(details))
		}
		return result
	}

	// If matched, validate output if expected output file exists
	if matched {
		outputFile := strings.TrimSuffix(messagePath, ".json") + "_output.json"
		if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
			if err := validateOutput(actions[0], outputFile); err != nil {
				result.Error = fmt.Sprintf("output validation failed: %v", err)
				return result
			}
		}
	}

	result.Passed = true
	return result
}

// validateOutput checks if the actual action matches the expected output file.
func validateOutput(action *rule.Action, outputFile string) error {
	// Use os.ReadFile (Fix A)
	expectedBytes, err := os.ReadFile(outputFile)
	if err != nil {
		return fmt.Errorf("could not read expected output file: %w", err)
	}

	var expected ExpectedOutput
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		return fmt.Errorf("could not parse expected output file: %w", err)
	}

	if action.Subject != expected.Subject {
		return fmt.Errorf("subject mismatch: got '%s', want '%s'", action.Subject, expected.Subject)
	}

	var actualPayload, expectedPayload interface{}
	if err := json.Unmarshal([]byte(action.Payload), &actualPayload); err != nil {
		return fmt.Errorf("could not parse actual action payload: %w", err)
	}
	if err := json.Unmarshal(expected.Payload, &expectedPayload); err != nil {
		return fmt.Errorf("could not parse expected payload from output file: %w", err)
	}

	actualCanon, _ := json.Marshal(actualPayload)
	expectedCanon, _ := json.Marshal(expectedPayload)

	if string(actualCanon) != string(expectedCanon) {
		return fmt.Errorf("payload mismatch:\ngot:  %s\nwant: %s", string(actualCanon), string(expectedCanon))
	}

	return nil
}

// setupTestProcessor creates a rule processor configured for testing.
func setupTestProcessor(rulePath string, kvData map[string]map[string]interface{}, testConfig *TestConfig, verbose bool) *rule.Processor {
	// Always use NopLogger for test processor
	log := logger.NewNopLogger()

	rules, err := loadSingleRuleFile(rulePath)
	if err != nil {
		fmt.Printf("Error loading rule file %s: %v\n", rulePath, err)
		os.Exit(1)
	}

	var kvContext *rule.KVContext
	if kvData != nil {
		cache := rule.NewLocalKVCache(log)
		for bucket, keys := range kvData {
			for key, val := range keys {
				cache.Set(bucket, key, val)
			}
		}
		// Pass nil for NATS KV stores since we only use the local cache mock
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

// loadSingleRuleFile loads and parses a single rule file.
func loadSingleRuleFile(path string) ([]rule.Rule, error) {
	// Use os.ReadFile (Fix A)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read rule file: %w", err)
	}

	var rules []rule.Rule
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to parse yaml rule file: %w", err)
	}

	return rules, nil
}

// loadMockKV loads mock KV data from a JSON file.
func loadMockKV(path string) map[string]map[string]interface{} {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	// Use os.ReadFile (Fix A)
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data map[string]map[string]interface{}
	json.Unmarshal(bytes, &data)
	return data
}

// loadTestConfig loads the test configuration from a JSON file.
func loadTestConfig(path string) *TestConfig {
	config := &TestConfig{Subject: "test.subject"}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return config
	}
	// Use os.ReadFile (Fix A)
	bytes, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	json.Unmarshal(bytes, &config)
	return config
}

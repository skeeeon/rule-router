// cmd/rule-tester/main.go

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestResult represents the outcome of a single test
type TestResult struct {
	File      string `json:"file"`
	Passed    bool   `json:"passed"`
	Error     string `json:"error,omitempty"`
	Details   string `json:"details,omitempty"`
	DurationMs int64 `json:"duration_ms"`
}

// TestSummary aggregates all test results
type TestSummary struct {
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	DurationMs int64        `json:"duration_ms"`
	Results    []TestResult `json:"results"`
}

// testCase represents a single test case with its configuration
type testCase struct {
	rulePath   string
	testDir    string
	testFiles  []string
	kvData     map[string]map[string]interface{}
	testConfig *TestConfig
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// CLI Flags
	lint := flag.Bool("lint", false, "Run in Linter mode. Requires --rules.")
	scaffold := flag.String("scaffold", "", "Run in Scaffold mode. Provide path to a rule file.")
	test := flag.Bool("test", false, "Run in Batch Test mode. Requires --rules.")
	rulePath := flag.String("rule", "", "Path to a single rule file for Quick Check mode.")
	messagePath := flag.String("message", "", "Path to a single message file for Quick Check mode.")
	subjectOverride := flag.String("subject", "", "Manually specify a subject for Quick Check mode.")
	rulesDir := flag.String("rules", "", "Path to the root directory for rules.")
	
	// NEW: Additional flags
	kvMockPath := flag.String("kv-mock", "", "Path to mock KV data file for Quick Check mode.")
	outputFormat := flag.String("output", "pretty", "Output format: pretty, json")
	verbose := flag.Bool("verbose", false, "Show detailed output for failures")
	noOverwrite := flag.Bool("no-overwrite", false, "Skip scaffold if test directory exists")
	parallel := flag.Int("parallel", 4, "Number of parallel test workers (0 = sequential)")
	
	flag.Parse()

	// Route to appropriate mode
	if *lint && *rulesDir != "" {
		return runLinter(*rulesDir)
	} else if *scaffold != "" {
		return runScaffold(*scaffold, *noOverwrite)
	} else if *test && *rulesDir != "" {
		return runBatchTest(*rulesDir, *outputFormat, *verbose, *parallel)
	} else if *rulePath != "" && *messagePath != "" {
		return runQuickCheck(*rulePath, *messagePath, *subjectOverride, *kvMockPath, *verbose)
	} else {
		return fmt.Errorf("invalid usage - use --help for options")
	}
}

// --- Mode Implementations ---

func runLinter(rulesDir string) error {
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

func runScaffold(rulePath string, noOverwrite bool) error {
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
	if err := ioutil.WriteFile(filepath.Join(testDir, "_test_config.json"), configBytes, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if err := ioutil.WriteFile(filepath.Join(testDir, "match_1.json"), []byte("{}\n"), 0644); err != nil {
		return fmt.Errorf("failed to write match file: %w", err)
	}
	if err := ioutil.WriteFile(filepath.Join(testDir, "not_match_1.json"), []byte("{}\n"), 0644); err != nil {
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

func runQuickCheck(rulePath, messagePath, subjectOverride, kvMockPath string, verbose bool) error {
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
	processor := setupTestProcessor(rulePath, kvData, testConfig, verbose)

	msgBytes, err := ioutil.ReadFile(messagePath)
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
		
		if verbose {
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

func runBatchTest(rulesDir, outputFormat string, verbose bool, parallelWorkers int) error {
	startTime := time.Now()
	
	if outputFormat == "pretty" {
		fmt.Printf("▶ RUNNING TESTS in %s\n\n", rulesDir)
	}
	
	summary := TestSummary{
		Results: make([]TestResult, 0),
	}

	// Collect all test cases
	var testCases []testCase
	
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
			// Load test files - FIX: Handle glob errors
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
				
				testCases = append(testCases, testCase{
					rulePath:   path,
					testDir:    testDir,
					testFiles:  validTests,
					kvData:     kvData,
					testConfig: testConfig,
				})
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to walk rules directory: %w", err)
	}

	// Execute tests (parallel or sequential)
	if parallelWorkers > 0 {
		summary = runTestsParallel(testCases, parallelWorkers, outputFormat, verbose)
	} else {
		summary = runTestsSequential(testCases, outputFormat, verbose)
	}
	
	summary.DurationMs = time.Since(startTime).Milliseconds()

	// Output results
	if outputFormat == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summary)
	} else {
		fmt.Println("--- SUMMARY ---")
		fmt.Printf("Total Tests: %d, Passed: %d, Failed: %d\n", 
			summary.Total, summary.Passed, summary.Failed)
		fmt.Printf("Duration: %dms\n", summary.DurationMs)
		
		if summary.Failed > 0 {
			return fmt.Errorf("tests failed")
		}
		return nil
	}
}

func runTestsSequential(testCases []testCase, outputFormat string, verbose bool) TestSummary {
	summary := TestSummary{}
	
	for _, tc := range testCases {
		if outputFormat == "pretty" {
			fmt.Printf("=== RULE: %s ===\n", tc.rulePath)
		}
		
		processor := setupTestProcessor(tc.rulePath, tc.kvData, tc.testConfig, false)
		
		for _, testFile := range tc.testFiles {
			baseName := filepath.Base(testFile)
			summary.Total++
			
			result := runSingleTestCase(processor, testFile, tc.testConfig.Subject, verbose)
			summary.Results = append(summary.Results, result)
			
			if result.Passed {
				summary.Passed++
				if outputFormat == "pretty" {
					fmt.Printf("  ✔ %s (%dms)\n", baseName, result.DurationMs)
				}
			} else {
				summary.Failed++
				if outputFormat == "pretty" {
					fmt.Printf("  ✖ %s (%dms)\n", baseName, result.DurationMs)
					fmt.Printf("    Error: %s\n", result.Error)
					if verbose && result.Details != "" {
						fmt.Printf("    Details: %s\n", result.Details)
					}
				}
			}
		}
		
		if outputFormat == "pretty" {
			fmt.Println()
		}
	}
	
	return summary
}

func runTestsParallel(testCases []testCase, workers int, outputFormat string, verbose bool) TestSummary {
	type job struct {
		rulePath   string
		testFile   string
		testConfig *TestConfig
		kvData     map[string]map[string]interface{}
	}
	
	// Create job queue
	jobs := make(chan job, 100)
	results := make(chan TestResult, 100)
	
	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				processor := setupTestProcessor(j.rulePath, j.kvData, j.testConfig, false)
				result := runSingleTestCase(processor, j.testFile, j.testConfig.Subject, verbose)
				results <- result
			}
		}()
	}
	
	// Send jobs
	go func() {
		for _, tc := range testCases {
			for _, testFile := range tc.testFiles {
				jobs <- job{
					rulePath:   tc.rulePath,
					testFile:   testFile,
					testConfig: tc.testConfig,
					kvData:     tc.kvData,
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
	for _, tc := range testCases {
		totalTests += len(tc.testFiles)
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
		if outputFormat == "pretty" {
			fmt.Printf("\r[%d/%d] Tests completed...", completed, totalTests)
		}
	}
	
	if outputFormat == "pretty" {
		fmt.Printf("\r") // Clear progress line
	}
	
	return summary
}

func runSingleTestCase(processor *rule.Processor, messagePath, subject string, verbose bool) TestResult {
	start := time.Now()
	baseName := filepath.Base(messagePath)
	shouldMatch := strings.HasPrefix(baseName, "match_")

	result := TestResult{
		File: baseName,
	}

	msgBytes, err := ioutil.ReadFile(messagePath)
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
		if verbose {
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

func validateOutput(action *rule.Action, outputFile string) error {
	expectedBytes, err := ioutil.ReadFile(outputFile)
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

func setupTestProcessor(rulePath string, kvData map[string]map[string]interface{}, testConfig *TestConfig, verbose bool) *rule.Processor {
	// Always use NopLogger for test processor
	// Verbose output is handled by the test runner, not the processor
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
	config := &TestConfig{Subject: "test.subject"}
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

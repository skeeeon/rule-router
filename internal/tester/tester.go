// file: internal/tester/tester.go

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
		if err != nil { return err }
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") { return filepath.SkipDir }
		if info.IsDir() { return nil }
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" { return nil }
		if _, err := loadSingleRuleFile(path); err != nil {
			fmt.Printf("✖ FAIL: %s\n  Error: %v\n", path, err)
			failed = true
		} else {
			fmt.Printf("✔ PASS: %s\n", path)
		}
		return nil
	})

	if err != nil { return fmt.Errorf("walk error: %w", err) }
	if failed { return fmt.Errorf("linting failed") }
	fmt.Println("\nLinting complete. All files are valid.")
	return nil
}

// Scaffold runs the scaffold mode.
func (t *Tester) Scaffold(rulePath string, noOverwrite bool) error {
	if !strings.HasSuffix(rulePath, ".yaml") && !strings.HasSuffix(rulePath, ".yml") {
		return fmt.Errorf("--scaffold requires a path to a .yaml rule file")
	}

	testDir := strings.TrimSuffix(rulePath, filepath.Ext(rulePath)) + "_test"

	// Restore the overwrite protection and user prompt logic.
	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		if noOverwrite {
			return fmt.Errorf("test directory already exists: %s (use without --no-overwrite to proceed)", testDir)
		}

		fmt.Printf("⚠️  Test directory already exists: %s\n", testDir)
		fmt.Printf("   Overwrite? (y/N): ")

		var response string
		fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := os.MkdirAll(testDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", testDir, err)
	}

	rules, _ := loadSingleRuleFile(rulePath)
	subject := "subject"
	if len(rules) > 0 {
		ruleSubject := rules[0].Subject
		if !strings.Contains(ruleSubject, "*") && !strings.Contains(ruleSubject, ">") {
			subject = ruleSubject
		}
	}

	testConfig := TestConfig{
		Subject:  subject,
		MockTime: time.Now().Format(time.RFC3339),
		Headers:  make(map[string]string),
	}
	configBytes, _ := json.MarshalIndent(testConfig, "", "  ")

	os.WriteFile(filepath.Join(testDir, "_test_config.json"), configBytes, 0644)
	os.WriteFile(filepath.Join(testDir, "match_1.json"), []byte("{}\n"), 0644)
	os.WriteFile(filepath.Join(testDir, "not_match_1.json"), []byte("{}\n"), 0644)

	fmt.Printf("✔ Scaffolded test directory at: %s\n", testDir)
	fmt.Println("  - _test_config.json (includes 'headers' field)")
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
			testSubject = "subject" // Placeholder
		} else {
			testSubject = ruleSubject
		}
	}

	kvData := loadMockKV(kvMockPath)
	testConfig := &TestConfig{Subject: testSubject}
	processor := setupTestProcessor(rulePath, kvData, testConfig, t.Verbose)

	msgBytes, err := os.ReadFile(messagePath)
	if err != nil {
		return fmt.Errorf("failed to read message file %s: %w", messagePath, err)
	}

	fmt.Printf("▶ Running Quick Check on subject: %s\n\n", testSubject)
	start := time.Now()
	actions, err := processor.ProcessWithSubject(testConfig.Subject, msgBytes, nil)
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
			if action.Passthrough {
				fmt.Printf("Payload: %s (passthrough)\n", string(action.RawPayload))
			} else {
				fmt.Printf("Payload: %s\n", action.Payload)
			}
			fmt.Println("-----------------------")
		}
	} else {
		fmt.Println("Rule Matched: False")
		fmt.Printf("Processing Time: %v\n", duration)
	}
	return nil
}

// --- Batch Test Logic ---

func (t *Tester) RunBatchTest(rulesDir string) (TestSummary, error) {
	startTime := time.Now()
	testGroups, err := t.collectTestGroups(rulesDir)
	if err != nil {
		return TestSummary{}, fmt.Errorf("failed to collect test groups: %w", err)
	}
	var summary TestSummary
	if t.ParallelWorkers > 0 {
		summary = t.runTestsParallel(testGroups)
	} else {
		summary = t.runTestsSequential(testGroups)
	}
	summary.DurationMs = time.Since(startTime).Milliseconds()
	return summary, nil
}

func (t *Tester) runTestsSequential(groups []TestGroup) TestSummary {
	summary := TestSummary{}
	for _, group := range groups {
		fmt.Printf("=== RULE: %s ===\n", group.RulePath)
		processor := setupTestProcessor(group.RulePath, group.KVData, group.TestConfig, false)
		for _, testFile := range group.TestFiles {
			baseName := filepath.Base(testFile)
			summary.Total++
			result := t.runSingleTestCase(processor, testFile, group.TestConfig.Subject, group.TestConfig.Headers)
			summary.Results = append(summary.Results, result)
			if result.Passed {
				summary.Passed++
				fmt.Printf("  ✔ %s (%dms)\n", baseName, result.DurationMs)
			} else {
				summary.Failed++
				fmt.Printf("  ✖ %s (%dms)\n", baseName, result.DurationMs)
			}
		}
		fmt.Println()
	}
	return summary
}

func (t *Tester) runTestsParallel(groups []TestGroup) TestSummary {
	jobs := make(chan TestJob, 100)
	results := make(chan TestResult, 100)
	var wg sync.WaitGroup
	for i := 0; i < t.ParallelWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- t.runSingleTestCase(j.Processor, j.TestFile, j.Subject, j.Headers)
			}
		}()
	}
	go func() {
		for _, group := range groups {
			processor := setupTestProcessor(group.RulePath, group.KVData, group.TestConfig, false)
			for _, testFile := range group.TestFiles {
				jobs <- TestJob{
					Processor: processor, TestFile: testFile, Subject: group.TestConfig.Subject,
					Headers: group.TestConfig.Headers, Verbose: t.Verbose,
				}
			}
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	summary := TestSummary{}
	for result := range results {
		summary.Total++
		summary.Results = append(summary.Results, result)
		if result.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}
	return summary
}

func (t *Tester) runSingleTestCase(processor *rule.Processor, messagePath, subject string, headers map[string]string) TestResult {
	start := time.Now()
	baseName := filepath.Base(messagePath)
	shouldMatch := strings.HasPrefix(baseName, "match_")
	result := TestResult{File: baseName}
	msgBytes, err := os.ReadFile(messagePath)
	if err != nil {
		result.Error = fmt.Sprintf("could not read message file: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	actions, err := processor.ProcessWithSubject(subject, msgBytes, headers)
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("processing error: %v", err)
		return result
	}
	matched := len(actions) > 0
	if matched != shouldMatch {
		result.Error = fmt.Sprintf("expected match=%v, got match=%v", shouldMatch, matched)
		return result
	}
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

func loadTestConfig(path string) *TestConfig {
	config := &TestConfig{
		Subject: "test.subject",
		Headers: make(map[string]string),
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	json.Unmarshal(bytes, &config)
	if config.Headers == nil {
		config.Headers = make(map[string]string)
	}
	return config
}

// --- Other helper functions ---

func (t *Tester) collectTestGroups(rulesDir string) ([]TestGroup, error) {
	var testGroups []TestGroup
	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") { return filepath.SkipDir }
		if info.IsDir() { return nil }
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" { return nil }
		testDir := strings.TrimSuffix(path, filepath.Ext(path)) + "_test"
		if _, err := os.Stat(testDir); !os.IsNotExist(err) {
			testFiles, _ := filepath.Glob(filepath.Join(testDir, "*.json"))
			var validTests []string
			for _, testFile := range testFiles {
				baseName := filepath.Base(testFile)
				if !strings.HasSuffix(baseName, "_output.json") && baseName != "mock_kv_data.json" && baseName != "_test_config.json" {
					validTests = append(validTests, testFile)
				}
			}
			if len(validTests) > 0 {
				testGroups = append(testGroups, TestGroup{
					RulePath: path, TestDir: testDir, TestFiles: validTests,
					KVData: loadMockKV(filepath.Join(testDir, "mock_kv_data.json")),
					TestConfig: loadTestConfig(filepath.Join(testDir, "_test_config.json")),
				})
			}
		}
		return nil
	})
	return testGroups, err
}

func validateOutput(action *rule.Action, outputFile string) error {
	expectedBytes, err := os.ReadFile(outputFile)
	if err != nil { return err }
	var expected ExpectedOutput
	if err := json.Unmarshal(expectedBytes, &expected); err != nil { return err }
	if action.Subject != expected.Subject { return fmt.Errorf("subject mismatch: got '%s', want '%s'", action.Subject, expected.Subject) }
	var actualPayload, expectedPayload interface{}
	payloadBytes := []byte(action.Payload)
	if action.Passthrough { payloadBytes = action.RawPayload }
	if err := json.Unmarshal(payloadBytes, &actualPayload); err != nil { return err }
	if err := json.Unmarshal(expected.Payload, &expectedPayload); err != nil { return err }
	actualCanon, _ := json.Marshal(actualPayload)
	expectedCanon, _ := json.Marshal(expectedPayload)
	if string(actualCanon) != string(expectedCanon) { return fmt.Errorf("payload mismatch:\ngot:  %s\nwant: %s", string(actualCanon), string(expectedCanon)) }
	return nil
}

func setupTestProcessor(rulePath string, kvData map[string]map[string]interface{}, testConfig *TestConfig, verbose bool) *rule.Processor {
	log := logger.NewNopLogger()
	var bucketNames []string
	for bucket := range kvData { bucketNames = append(bucketNames, bucket) }
	loader := rule.NewRulesLoader(log, bucketNames)
	rules, _ := loader.LoadFromDirectory(filepath.Dir(rulePath))
	var kvContext *rule.KVContext
	if kvData != nil {
		cache := rule.NewLocalKVCache(log)
		for bucket, keys := range kvData {
			for key, val := range keys { cache.Set(bucket, key, val) }
		}
		kvContext = rule.NewKVContext(nil, log, cache)
	}
	processor := rule.NewProcessor(log, nil, kvContext)
	processor.LoadRules(rules)
	if testConfig.MockTime != "" {
		if t, err := time.Parse(time.RFC3339, testConfig.MockTime); err == nil {
			processor.SetTimeProvider(rule.NewMockTimeProvider(t))
		}
	}
	return processor
}

func loadSingleRuleFile(path string) ([]rule.Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil { return nil, err }
	var rules []rule.Rule
	if err := yaml.Unmarshal(data, &rules); err != nil { return nil, err }
	return rules, nil
}

func loadMockKV(path string) map[string]map[string]interface{} {
	bytes, err := os.ReadFile(path)
	if err != nil { return nil }
	var data map[string]map[string]interface{}
	json.Unmarshal(bytes, &data)
	return data
}

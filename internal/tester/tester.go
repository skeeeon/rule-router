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

// Lint runs the linter mode. It validates the syntax of all rule files in a directory.
func (t *Tester) Lint(rulesDir string) error {
	fmt.Printf("▶ LINTING rules in %s\n\n", rulesDir)
	var failed bool

	// Use the official loader which contains all validation logic
	loader := rule.NewRulesLoader(t.Logger, nil) // KV buckets not needed for linting

	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		// LoadFromFile now performs all necessary validation including new syntax
		if _, err := loader.LoadFromFile(path); err != nil {
			fmt.Printf("✖ FAIL: %s\n  Error: %v\n", path, err)
			failed = true
		} else {
			fmt.Printf("✓ PASS: %s\n", path)
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

// Scaffold runs the scaffold mode, generating a test directory for a rule.
// UPDATED: Detects forEach and generates appropriate test templates with new {braces} syntax
func (t *Tester) Scaffold(rulePath string, noOverwrite bool) error {
	if !strings.HasSuffix(rulePath, ".yaml") && !strings.HasSuffix(rulePath, ".yml") {
		return fmt.Errorf("--scaffold requires a path to a .yaml rule file")
	}

	testDir := strings.TrimSuffix(rulePath, filepath.Ext(rulePath)) + "_test"

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

	rules, err := loadSingleRuleFile(rulePath)
	if err != nil || len(rules) == 0 {
		return fmt.Errorf("could not load rule file to determine trigger type: %w", err)
	}
	r := rules[0]

	// Create a test config based on the rule's trigger type
	testConfig := TestConfig{
		MockTime: time.Now().Format(time.RFC3339),
		Headers:  make(map[string]string),
	}

	if r.Trigger.NATS != nil {
		testConfig.MockTrigger.NATS = r.Trigger.NATS
	} else if r.Trigger.HTTP != nil {
		testConfig.MockTrigger.HTTP = r.Trigger.HTTP
	}

	configBytes, _ := json.MarshalIndent(testConfig, "", "  ")
	os.WriteFile(filepath.Join(testDir, "_test_config.json"), configBytes, 0644)

	// Detect if rule uses forEach and generate appropriate examples
	usesForEach, forEachField := detectForEach(&r)
	
	if usesForEach {
		fmt.Printf("✓ Detected forEach operation on field: %s\n", forEachField)
		fmt.Printf("  Generating array-based test examples...\n")
		
		// Generate array input example with new syntax
		t.generateForEachExamples(testDir, forEachField, &r)
	} else {
		// Check if rule has variable comparisons in conditions
		hasVarComparison := detectVariableComparisons(&r)
		
		if hasVarComparison {
			fmt.Printf("✓ Detected variable-to-variable comparisons\n")
			fmt.Printf("  Generating examples with dynamic values...\n")
			t.generateVariableComparisonExamples(testDir, &r)
		} else {
			// Standard single-object examples
			os.WriteFile(filepath.Join(testDir, "match_1.json"), []byte("{\n  \"field\": \"value\"\n}\n"), 0644)
			os.WriteFile(filepath.Join(testDir, "not_match_1.json"), []byte("{\n  \"field\": \"other\"\n}\n"), 0644)
		}
	}

	fmt.Printf("✓ Scaffolded test directory at: %s\n", testDir)
	if usesForEach {
		fmt.Printf("\n💡 TIP: Your rule uses forEach - example test files include:\n")
		fmt.Printf("   • Array input messages (with {braces} syntax)\n")
		fmt.Printf("   • Array output validation (multiple actions)\n")
		fmt.Printf("   • Filter condition examples\n")
	}
	return nil
}

// detectForEach checks if a rule uses forEach and returns the field path
// UPDATED: Handles new {braces} syntax
func detectForEach(r *rule.Rule) (bool, string) {
	if r.Action.NATS != nil && r.Action.NATS.ForEach != "" {
		return true, r.Action.NATS.ForEach
	}
	if r.Action.HTTP != nil && r.Action.HTTP.ForEach != "" {
		return true, r.Action.HTTP.ForEach
	}
	return false, ""
}

// detectVariableComparisons checks if a rule uses variable-to-variable comparisons
// NEW: Helps scaffold better examples for rules with dynamic comparisons
func detectVariableComparisons(r *rule.Rule) bool {
	if r.Conditions == nil {
		return false
	}
	return hasVariableComparisonsRecursive(r.Conditions)
}

// hasVariableComparisonsRecursive checks conditions recursively for variable comparisons
func hasVariableComparisonsRecursive(conds *rule.Conditions) bool {
	for _, item := range conds.Items {
		// Check if value is a template variable (not a literal)
		if valueStr, ok := item.Value.(string); ok {
			if strings.HasPrefix(valueStr, "{") && strings.HasSuffix(valueStr, "}") {
				return true
			}
		}
		
		// Check nested conditions (for array operators)
		if item.Conditions != nil {
			if hasVariableComparisonsRecursive(item.Conditions) {
				return true
			}
		}
	}
	
	// Check nested groups
	for _, group := range conds.Groups {
		if hasVariableComparisonsRecursive(&group) {
			return true
		}
	}
	
	return false
}

// generateForEachExamples creates example test files for forEach rules
// UPDATED: Uses new {braces} syntax and generates more realistic examples
func (t *Tester) generateForEachExamples(testDir, forEachField string, r *rule.Rule) {
	// Extract field name from {braces} if present
	fieldName := forEachField
	if strings.HasPrefix(forEachField, "{") && strings.HasSuffix(forEachField, "}") {
		fieldName = forEachField[1 : len(forEachField)-1]
	}
	
	// Create example match case with array
	matchExample := map[string]interface{}{
		"timestamp": "2025-01-15T10:30:00Z",
		"batch_id":  "batch-001",
	}
	
	// Build realistic array data
	arrayData := []interface{}{
		map[string]interface{}{
			"id":     "item-1",
			"status": "active",
			"value":  100,
			"type":   "CRITICAL",
		},
		map[string]interface{}{
			"id":     "item-2",
			"status": "active",
			"value":  200,
			"type":   "CRITICAL",
		},
		map[string]interface{}{
			"id":     "item-3",
			"status": "inactive",
			"value":  150,
			"type":   "INFO",
		},
	}
	
	// Handle nested paths in forEach field (e.g., "data.items")
	if strings.Contains(fieldName, ".") {
		parts := strings.Split(fieldName, ".")
		current := matchExample
		for i := 0; i < len(parts)-1; i++ {
			newLevel := make(map[string]interface{})
			current[parts[i]] = newLevel
			current = newLevel
		}
		current[parts[len(parts)-1]] = arrayData
	} else {
		matchExample[fieldName] = arrayData
	}
	
	matchBytes, _ := json.MarshalIndent(matchExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "match_1.json"), matchBytes, 0644)

	// Create example output file (array of expected actions)
	var outputExample []ExpectedOutput
	
	if r.Action.NATS != nil {
		// Generate 2 expected NATS actions (assuming filter matches first 2 items)
		outputExample = []ExpectedOutput{
			{
				Subject: "example.item-1",
				Payload: json.RawMessage(`{"id":"item-1","status":"active","value":100,"type":"CRITICAL"}`),
			},
			{
				Subject: "example.item-2",
				Payload: json.RawMessage(`{"id":"item-2","status":"active","value":200,"type":"CRITICAL"}`),
			},
		}
	} else if r.Action.HTTP != nil {
		// Generate 2 expected HTTP actions
		outputExample = []ExpectedOutput{
			{
				URL:     "https://api.example.com/items/item-1",
				Method:  "POST",
				Payload: json.RawMessage(`{"id":"item-1","status":"active","value":100,"type":"CRITICAL"}`),
			},
			{
				URL:     "https://api.example.com/items/item-2",
				Method:  "POST",
				Payload: json.RawMessage(`{"id":"item-2","status":"active","value":200,"type":"CRITICAL"}`),
			},
		}
	}
	
	outputBytes, _ := json.MarshalIndent(outputExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "match_1_output.json"), outputBytes, 0644)

	// Create example non-match case (elements that don't pass filter)
	notMatchExample := map[string]interface{}{
		"timestamp": "2025-01-15T10:30:00Z",
		"batch_id":  "batch-002",
	}
	
	notMatchArray := []interface{}{
		map[string]interface{}{
			"id":     "item-99",
			"status": "inactive",
			"value":  50,
			"type":   "INFO",
		},
	}
	
	if strings.Contains(fieldName, ".") {
		parts := strings.Split(fieldName, ".")
		current := notMatchExample
		for i := 0; i < len(parts)-1; i++ {
			newLevel := make(map[string]interface{})
			current[parts[i]] = newLevel
			current = newLevel
		}
		current[parts[len(parts)-1]] = notMatchArray
	} else {
		notMatchExample[fieldName] = notMatchArray
	}
	
	notMatchBytes, _ := json.MarshalIndent(notMatchExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "not_match_1.json"), notMatchBytes, 0644)

	// Create additional example with empty array
	emptyArrayExample := map[string]interface{}{
		"timestamp": "2025-01-15T10:30:00Z",
		"batch_id":  "batch-003",
	}
	
	if strings.Contains(fieldName, ".") {
		parts := strings.Split(fieldName, ".")
		current := emptyArrayExample
		for i := 0; i < len(parts)-1; i++ {
			newLevel := make(map[string]interface{})
			current[parts[i]] = newLevel
			current = newLevel
		}
		current[parts[len(parts)-1]] = []interface{}{}
	} else {
		emptyArrayExample[fieldName] = []interface{}{}
	}
	
	emptyBytes, _ := json.MarshalIndent(emptyArrayExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "not_match_2_empty_array.json"), emptyBytes, 0644)
	
	// Generate README explaining the examples
	readme := fmt.Sprintf(`# Test Suite for Rule with forEach

This rule uses forEach on the field: %s

## Test Files

### match_1.json
- Contains an array with 3 items
- First 2 items should match the filter conditions
- Expected to generate 2 actions (see match_1_output.json)

### match_1_output.json
- Array of expected actions
- Each element represents one expected action from the forEach iteration
- Update this based on your actual rule's subject/URL and payload templates

### not_match_1.json
- Contains items that don't match the filter conditions
- Expected to generate 0 actions

### not_match_2_empty_array.json
- Contains an empty array
- Expected to generate 0 actions

## Tips

1. Update the field values to match your actual conditions
2. If your rule has a filter, ensure test data reflects what should/shouldn't pass
3. The output file should match the EXACT subject/URL and payload your rule generates
4. Add more test cases for edge cases specific to your rule

## New Syntax Note

This rule uses the new {braces} syntax:
- Condition fields: {field}
- ForEach fields: {arrayField}
- Template variables: {variable}

All field references now consistently use {braces}!
`, forEachField)
	
	os.WriteFile(filepath.Join(testDir, "README.md"), []byte(readme), 0644)
}

// generateVariableComparisonExamples creates examples for rules with variable-to-variable comparisons
// NEW: Helps users understand dynamic comparison rules
func (t *Tester) generateVariableComparisonExamples(testDir string, r *rule.Rule) {
	// Create example with both fields present (should match)
	matchExample := map[string]interface{}{
		"temperature": 105,
		"threshold":   100,
		"sensor_id":   "sensor-001",
		"timestamp":   "2025-01-15T10:30:00Z",
	}
	
	matchBytes, _ := json.MarshalIndent(matchExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "match_1_above_threshold.json"), matchBytes, 0644)

	// Create example where comparison fails (below threshold)
	notMatchExample := map[string]interface{}{
		"temperature": 95,
		"threshold":   100,
		"sensor_id":   "sensor-001",
		"timestamp":   "2025-01-15T10:30:00Z",
	}
	
	notMatchBytes, _ := json.MarshalIndent(notMatchExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "not_match_1_below_threshold.json"), notMatchBytes, 0644)

	// Create example with missing comparison field
	missingExample := map[string]interface{}{
		"temperature": 105,
		// threshold is missing
		"sensor_id": "sensor-001",
		"timestamp": "2025-01-15T10:30:00Z",
	}
	
	missingBytes, _ := json.MarshalIndent(missingExample, "", "  ")
	os.WriteFile(filepath.Join(testDir, "not_match_2_missing_field.json"), missingBytes, 0644)
	
	// Generate README explaining variable comparisons
	readme := `# Test Suite for Rule with Variable Comparisons

This rule uses variable-to-variable comparisons in conditions.

## New Syntax Features

This rule demonstrates the new {braces} syntax for variable comparisons:

` + "```yaml" + `
conditions:
  items:
    - field: "{temperature}"    # Left side: message field
      operator: gt
      value: "{threshold}"      # Right side: another message field (or @kv, @time, etc.)
` + "```" + `

## Test Files

### match_1_above_threshold.json
- Both fields present (temperature and threshold)
- Comparison passes (temperature > threshold)
- Expected to match and generate action

### not_match_1_below_threshold.json
- Both fields present
- Comparison fails (temperature < threshold)
- Expected to NOT match

### not_match_2_missing_field.json
- Missing the comparison field (threshold)
- Expected to NOT match (missing variables fail gracefully)

## Tips for Variable Comparison Rules

1. **Always test with both fields present** - this is your happy path
2. **Test with missing fields** - ensure your rule handles this gracefully
3. **Test with type mismatches** - what if threshold is a string?
4. **Use KV for dynamic thresholds**:
   ` + "```yaml" + `
   value: "{@kv.sensor_config.{sensor_id}:max_temp}"
   ` + "```" + `

5. **Update mock_kv_data.json** if using KV lookups

## Common Patterns

### Dynamic Threshold (from message)
` + "```yaml" + `
field: "{temperature}"
operator: gt
value: "{threshold}"
` + "```" + `

### Dynamic Threshold (from KV)
` + "```yaml" + `
field: "{temperature}"
operator: gt
value: "{@kv.config.{sensor_id}:max_temp}"
` + "```" + `

### Cross-Field Validation
` + "```yaml" + `
field: "{end_time}"
operator: gt
value: "{start_time}"
` + "```" + `

### Time-Based Comparison
` + "```yaml" + `
field: "{scheduled_hour}"
operator: eq
value: "{@time.hour}"
` + "```" + `
`
	
	os.WriteFile(filepath.Join(testDir, "README.md"), []byte(readme), 0644)
}

// QuickCheck runs the quick check mode for interactive testing.
func (t *Tester) QuickCheck(rulePath, messagePath, subjectOverride, kvMockPath string) error {
	rules, err := loadSingleRuleFile(rulePath)
	if err != nil || len(rules) == 0 {
		return fmt.Errorf("could not load or parse rule file %s: %w", rulePath, err)
	}
	r := rules[0]
	
	// Setup test config based on the actual rule trigger
	testConfig := &TestConfig{Headers: make(map[string]string)}
	if r.Trigger.NATS != nil {
		testConfig.MockTrigger.NATS = r.Trigger.NATS
		if subjectOverride != "" {
			testConfig.MockTrigger.NATS.Subject = subjectOverride
		}
		fmt.Printf("▶ Running Quick Check (NATS) on subject: %s\n\n", testConfig.MockTrigger.NATS.Subject)
	} else if r.Trigger.HTTP != nil {
		testConfig.MockTrigger.HTTP = r.Trigger.HTTP
		fmt.Printf("▶ Running Quick Check (HTTP) on path: %s, method: %s\n\n", testConfig.MockTrigger.HTTP.Path, testConfig.MockTrigger.HTTP.Method)
	} else {
		return fmt.Errorf("rule has no valid trigger")
	}

	kvData := loadMockKV(kvMockPath)
	processor := setupTestProcessor(rulePath, kvData, testConfig, t.Verbose)

	msgBytes, err := os.ReadFile(messagePath)
	if err != nil {
		return fmt.Errorf("failed to read message file %s: %w", messagePath, err)
	}

	start := time.Now()
	var actions []*rule.Action
	// Dispatch to the correct processor method based on trigger type
	if testConfig.MockTrigger.NATS != nil {
		actions, err = processor.ProcessNATS(testConfig.MockTrigger.NATS.Subject, msgBytes, testConfig.Headers)
	} else {
		actions, err = processor.ProcessHTTP(testConfig.MockTrigger.HTTP.Path, testConfig.MockTrigger.HTTP.Method, msgBytes, testConfig.Headers)
	}
	duration := time.Since(start)

	if err != nil {
		return fmt.Errorf("processing error: %w", err)
	}

	if len(actions) > 0 {
		fmt.Println("Rule Matched: True")
		fmt.Printf("Actions Generated: %d\n", len(actions))
		fmt.Printf("Processing Time: %v\n", duration)
		for i, action := range actions {
			fmt.Printf("\n--- Rendered Action %d ---\n", i+1)
			if action.NATS != nil {
				fmt.Printf("Type: NATS\n")
				fmt.Printf("Subject: %s\n", action.NATS.Subject)
				if action.NATS.Passthrough {
					fmt.Printf("Payload: %s (passthrough)\n", string(action.NATS.RawPayload))
				} else {
					fmt.Printf("Payload: %s\n", action.NATS.Payload)
				}
				if len(action.NATS.Headers) > 0 {
					fmt.Printf("Headers: %v\n", action.NATS.Headers)
				}
			} else if action.HTTP != nil {
				fmt.Printf("Type: HTTP\n")
				fmt.Printf("URL: %s\n", action.HTTP.URL)
				fmt.Printf("Method: %s\n", action.HTTP.Method)
				if action.HTTP.Passthrough {
					fmt.Printf("Payload: %s (passthrough)\n", string(action.HTTP.RawPayload))
				} else {
					fmt.Printf("Payload: %s\n", action.HTTP.Payload)
				}
				if len(action.HTTP.Headers) > 0 {
					fmt.Printf("Headers: %v\n", action.HTTP.Headers)
				}
			}
			fmt.Println("-----------------------")
		}
	} else {
		fmt.Println("Rule Matched: False")
		fmt.Printf("Processing Time: %v\n", duration)
	}
	return nil
}

// RunBatchTest executes all test suites found in a directory.
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
			result := t.runSingleTestCase(processor, testFile, group.TestConfig)
			summary.Results = append(summary.Results, result)
			if result.Passed {
				summary.Passed++
				fmt.Printf("  ✓ %s (%dms)\n", baseName, result.DurationMs)
			} else {
				summary.Failed++
				fmt.Printf("  ✖ %s (%dms)\n", baseName, result.DurationMs)
				if t.Verbose && result.Error != "" {
					fmt.Printf("    Error: %s\n", result.Error)
					if result.Details != "" {
						fmt.Printf("    Details: %s\n", result.Details)
					}
				}
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
				results <- t.runSingleTestCase(j.Processor, j.TestFile, j.Config)
			}
		}()
	}
	go func() {
		for _, group := range groups {
			processor := setupTestProcessor(group.RulePath, group.KVData, group.TestConfig, false)
			for _, testFile := range group.TestFiles {
				jobs <- TestJob{
					Processor: processor,
					TestFile:  testFile,
					Config:    group.TestConfig,
					Verbose:   t.Verbose,
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

// runSingleTestCase updated to handle multiple actions (forEach support)
func (t *Tester) runSingleTestCase(processor *rule.Processor, messagePath string, testConfig *TestConfig) TestResult {
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

	var actions []*rule.Action
	// Dispatch to the correct processor method based on the mock trigger type
	if testConfig.MockTrigger.NATS != nil {
		actions, err = processor.ProcessNATS(testConfig.MockTrigger.NATS.Subject, msgBytes, testConfig.Headers)
	} else if testConfig.MockTrigger.HTTP != nil {
		actions, err = processor.ProcessHTTP(testConfig.MockTrigger.HTTP.Path, testConfig.MockTrigger.HTTP.Method, msgBytes, testConfig.Headers)
	} else {
		result.Error = "no mock trigger specified in test config"
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
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

	// If a match was expected, and an output file exists, validate ALL actions
	if matched {
		outputFile := strings.TrimSuffix(messagePath, ".json") + "_output.json"
		if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
			// Pass all actions for validation (supports both single and multiple)
			if err := validateOutputMultiple(actions, outputFile); err != nil {
				result.Error = fmt.Sprintf("output validation failed: %v", err)
				if t.Verbose {
					result.Details = fmt.Sprintf("Expected output file: %s\nActions generated: %d", outputFile, len(actions))
				}
				return result
			}
		}
	}

	result.Passed = true
	return result
}

// validateOutputMultiple handles both single action and multiple actions (forEach)
func validateOutputMultiple(actions []*rule.Action, outputFile string) error {
	expectedBytes, err := os.ReadFile(outputFile)
	if err != nil {
		return fmt.Errorf("failed to read output file: %w", err)
	}

	// TRY 1: Parse as array of ExpectedOutput (forEach case)
	var expectedArray []ExpectedOutput
	if err := json.Unmarshal(expectedBytes, &expectedArray); err == nil {
		// Successfully parsed as array - validate each action
		if len(actions) != len(expectedArray) {
			return fmt.Errorf("action count mismatch: got %d actions, expected %d", len(actions), len(expectedArray))
		}

		for i, action := range actions {
			if err := validateSingleOutput(action, &expectedArray[i]); err != nil {
				return fmt.Errorf("action %d validation failed: %w", i+1, err)
			}
		}
		return nil
	}

	// TRY 2: Parse as single ExpectedOutput (backward compatibility)
	var expectedSingle ExpectedOutput
	if err := json.Unmarshal(expectedBytes, &expectedSingle); err != nil {
		return fmt.Errorf("output file is neither valid array nor valid single object: %w", err)
	}

	// Single expected output - should have exactly 1 action
	if len(actions) != 1 {
		return fmt.Errorf("expected 1 action (single output format), got %d actions", len(actions))
	}

	return validateSingleOutput(actions[0], &expectedSingle)
}

// validateSingleOutput validates one action against one expected output
func validateSingleOutput(action *rule.Action, expected *ExpectedOutput) error {
	if action.NATS != nil {
		return validateNATSOutput(action.NATS, expected)
	} else if action.HTTP != nil {
		return validateHTTPOutput(action.HTTP, expected)
	}
	return fmt.Errorf("action has no NATS or HTTP configuration")
}

func validateNATSOutput(action *rule.NATSAction, expected *ExpectedOutput) error {
	if expected.Subject != "" && action.Subject != expected.Subject {
		return fmt.Errorf("subject mismatch: got '%s', want '%s'", action.Subject, expected.Subject)
	}
	if expected.Payload != nil {
		if err := validatePayload(action.Payload, action.Passthrough, action.RawPayload, expected.Payload); err != nil {
			return err
		}
	}
	if len(expected.Headers) > 0 {
		if err := validateHeaders(action.Headers, expected.Headers); err != nil {
			return fmt.Errorf("headers mismatch: %w", err)
		}
	}
	return nil
}

func validateHTTPOutput(action *rule.HTTPAction, expected *ExpectedOutput) error {
	if expected.URL != "" && action.URL != expected.URL {
		return fmt.Errorf("URL mismatch: got '%s', want '%s'", action.URL, expected.URL)
	}
	if expected.Method != "" && action.Method != expected.Method {
		return fmt.Errorf("method mismatch: got '%s', want '%s'", action.Method, expected.Method)
	}
	if expected.Payload != nil {
		if err := validatePayload(action.Payload, action.Passthrough, action.RawPayload, expected.Payload); err != nil {
			return err
		}
	}
	if len(expected.Headers) > 0 {
		if err := validateHeaders(action.Headers, expected.Headers); err != nil {
			return fmt.Errorf("headers mismatch: %w", err)
		}
	}
	return nil
}

func validatePayload(payload string, passthrough bool, rawPayload []byte, expectedPayload json.RawMessage) error {
	var actualPayload, expectedParsed interface{}
	payloadBytes := []byte(payload)
	if passthrough {
		payloadBytes = rawPayload
	}
	if err := json.Unmarshal(payloadBytes, &actualPayload); err != nil {
		return fmt.Errorf("could not unmarshal actual payload: %w", err)
	}
	if err := json.Unmarshal(expectedPayload, &expectedParsed); err != nil {
		return fmt.Errorf("could not unmarshal expected payload: %w", err)
	}
	actualCanon, _ := json.Marshal(actualPayload)
	expectedCanon, _ := json.Marshal(expectedParsed)
	if string(actualCanon) != string(expectedCanon) {
		return fmt.Errorf("payload mismatch:\ngot:  %s\nwant: %s", string(actualCanon), string(expectedCanon))
	}
	return nil
}

// validateHeaders checks if actual headers match expected headers
func validateHeaders(actualHeaders, expectedHeaders map[string]string) error {
	for key, expectedValue := range expectedHeaders {
		actualValue, exists := actualHeaders[key]
		if !exists {
			return fmt.Errorf("missing header '%s'", key)
		}
		if actualValue != expectedValue {
			return fmt.Errorf("header '%s': got '%s', want '%s'", key, actualValue, expectedValue)
		}
	}
	return nil
}

// --- Helper Functions ---

func (t *Tester) collectTestGroups(rulesDir string) ([]TestGroup, error) {
	var testGroups []TestGroup
	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && strings.HasSuffix(info.Name(), "_test") {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
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
					RulePath:   path,
					TestDir:    testDir,
					TestFiles:  validTests,
					KVData:     loadMockKV(filepath.Join(testDir, "mock_kv_data.json")),
					TestConfig: loadTestConfig(filepath.Join(testDir, "_test_config.json")),
				})
			}
		}
		return nil
	})
	return testGroups, err
}

func setupTestProcessor(rulePath string, kvData map[string]map[string]interface{}, testConfig *TestConfig, verbose bool) *rule.Processor {
	log := logger.NewNopLogger()
	var bucketNames []string
	for bucket := range kvData {
		bucketNames = append(bucketNames, bucket)
	}
	loader := rule.NewRulesLoader(log, bucketNames)
	// Only load the specific rule file for this test group
	rules, _ := loader.LoadFromFile(rulePath)

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

	var sigVerification *rule.SignatureVerification
	if testConfig.MockSignature != nil {
		sigVerification = rule.NewSignatureVerification(true, "Nats-Public-Key", "Nats-Signature")
	}

	processor := rule.NewProcessor(log, nil, kvContext, sigVerification)
	processor.LoadRules(rules)

	if testConfig.MockTime != "" {
		if t, err := time.Parse(time.RFC3339, testConfig.MockTime); err == nil {
			processor.SetTimeProvider(rule.NewMockTimeProvider(t))
		}
	}

	return processor
}

func loadTestConfig(path string) *TestConfig {
	config := &TestConfig{Headers: make(map[string]string)}
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

func loadSingleRuleFile(path string) ([]rule.Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []rule.Rule
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func loadMockKV(path string) map[string]map[string]interface{} {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data map[string]map[string]interface{}
	json.Unmarshal(bytes, &data)
	return data
}

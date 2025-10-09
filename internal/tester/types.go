// internal/tester/types.go

package tester

import (
	"encoding/json"
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
	File       string `json:"file"`
	Passed     bool   `json:"passed"`
	Error      string `json:"error,omitempty"`
	Details    string `json:"details,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// TestSummary aggregates all test results
type TestSummary struct {
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	DurationMs int64        `json:"duration_ms"`
	Results    []TestResult `json:"results"`
}

// TestGroup represents a single rule file and all its associated test cases.
// This is the unit of work for the optimized test runner (Fix B).
type TestGroup struct {
	RulePath   string
	TestDir    string
	TestFiles  []string
	KVData     map[string]map[string]interface{}
	TestConfig *TestConfig
}

// TestJob is used for parallel execution
type TestJob struct {
	Processor *rule.Processor // Pre-initialized processor
	TestFile  string
	Subject   string
	Verbose   bool
}

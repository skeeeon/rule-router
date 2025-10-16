// file: internal/tester/types.go

package tester

import (
	"encoding/json"

	"rule-router/internal/rule"
)

// TestConfig holds optional test-specific configurations.
// It defines the mock environment for a test run.
type TestConfig struct {
	MockTrigger   MockTrigger            `json:"mockTrigger"`
	MockTime      string                 `json:"mockTime,omitempty"`
	Headers       map[string]string      `json:"headers,omitempty"`
	MockSignature *MockSignatureConfig   `json:"mockSignature,omitempty"`
}

// MockTrigger specifies the type and details of the event that triggers the rule.
// Exactly one of NATS or HTTP must be non-nil.
type MockTrigger struct {
	NATS *rule.NATSTrigger `json:"nats,omitempty"`
	HTTP *rule.HTTPTrigger `json:"http,omitempty"`
}

// MockSignatureConfig allows mocking signature verification results in tests.
type MockSignatureConfig struct {
	Valid     bool   `json:"valid"`
	PublicKey string `json:"publicKey"`
}

// ExpectedOutput defines the structure for output validation files.
// It contains fields for both NATS and HTTP actions.
type ExpectedOutput struct {
	// For NATS actions
	Subject string `json:"subject,omitempty"`

	// For HTTP actions
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`

	// Common fields for both action types
	Payload json.RawMessage   `json:"payload"`
	Headers map[string]string `json:"headers,omitempty"`
}

// TestResult represents the outcome of a single test case.
type TestResult struct {
	File       string `json:"file"`
	Passed     bool   `json:"passed"`
	Error      string `json:"error,omitempty"`
	Details    string `json:"details,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// TestSummary aggregates all test results for a batch run.
type TestSummary struct {
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	DurationMs int64        `json:"duration_ms"`
	Results    []TestResult `json:"results"`
}

// TestGroup represents a single rule file and all its associated test cases.
type TestGroup struct {
	RulePath   string
	TestDir    string
	TestFiles  []string
	KVData     map[string]map[string]interface{}
	TestConfig *TestConfig
}

// TestJob is used for parallel execution of test cases.
type TestJob struct {
	Processor *rule.Processor
	TestFile  string
	Config    *TestConfig
	Verbose   bool
}

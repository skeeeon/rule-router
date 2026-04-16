// cmd/wasm/main.go
// WASM entry point for the rule evaluation engine.
// Exposes evaluateRule() to JavaScript for browser-based rule testing.

//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"
	"time"

	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// evaluateOptions represents the options passed from JavaScript.
type evaluateOptions struct {
	TriggerType string                            `json:"triggerType"`
	Subject     string                            `json:"subject"`
	Path        string                            `json:"path"`
	Method      string                            `json:"method"`
	Headers     map[string]string                 `json:"headers"`
	KVMock      map[string]map[string]interface{} `json:"kvMock"`
	MockTime    string                            `json:"mockTime"`
	RuleIndex   int                               `json:"ruleIndex"`
}

// evaluateResult is the JSON structure returned to JavaScript.
type evaluateResult struct {
	Matched         bool            `json:"matched"`
	Actions         []actionResult  `json:"actions"`
	Error           string          `json:"error,omitempty"`
	ValidationError string          `json:"validationError,omitempty"`
}

type actionResult struct {
	Type        string            `json:"type"`
	Subject     string            `json:"subject,omitempty"`
	URL         string            `json:"url,omitempty"`
	Method      string            `json:"method,omitempty"`
	Payload     string            `json:"payload"`
	Headers     map[string]string `json:"headers,omitempty"`
	Passthrough bool              `json:"passthrough"`
}

func evaluateRule(_ js.Value, args []js.Value) interface{} {
	if len(args) < 3 {
		return errorResult("evaluateRule requires 3 arguments: yamlStr, messageStr, optionsJSON")
	}

	yamlStr := args[0].String()
	messageStr := args[1].String()
	optionsJSON := args[2].String()

	var opts evaluateOptions
	if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
		return errorResult(fmt.Sprintf("invalid options JSON: %v", err))
	}

	log := logger.NewNopLogger()

	// Parse and validate rules from YAML
	var kvBuckets []string
	for bucket := range opts.KVMock {
		kvBuckets = append(kvBuckets, bucket)
	}
	loader := rule.NewRulesLoader(log, kvBuckets)
	rules, err := loader.ParseAndValidateYAML([]byte(yamlStr), "web-tester")
	if err != nil {
		return marshalResult(evaluateResult{ValidationError: err.Error()})
	}
	if len(rules) == 0 {
		return marshalResult(evaluateResult{ValidationError: "no rules found in YAML"})
	}

	// Resolve rule index
	ruleIndex := opts.RuleIndex
	if ruleIndex < 0 || ruleIndex >= len(rules) {
		ruleIndex = 0
	}
	selectedRule := rules[ruleIndex : ruleIndex+1]

	// Set up KV context with mock data
	cache := rule.NewLocalKVCache(log)
	for bucket, keys := range opts.KVMock {
		for key, value := range keys {
			cache.Set(bucket, key, value)
		}
	}
	kvCtx := rule.NewKVContext(nil, log, cache)

	// Create processor
	processor := rule.NewProcessor(log, nil, kvCtx, nil)
	if err := processor.LoadRules(selectedRule); err != nil {
		return marshalResult(evaluateResult{Error: fmt.Sprintf("failed to load rules: %v", err)})
	}

	// Set mock time if provided
	if opts.MockTime != "" {
		if t, err := time.Parse(time.RFC3339, opts.MockTime); err == nil {
			processor.SetTimeProvider(rule.NewMockTimeProvider(t))
		}
	}

	// Process the message
	msgBytes := []byte(messageStr)
	headers := opts.Headers
	if headers == nil {
		headers = make(map[string]string)
	}

	var actions []*rule.Action
	switch opts.TriggerType {
	case "http":
		path := opts.Path
		if path == "" {
			path = "/"
		}
		method := opts.Method
		if method == "" {
			method = "POST"
		}
		actions, err = processor.ProcessHTTP(path, method, msgBytes, headers)
	default:
		subject := opts.Subject
		if subject == "" {
			subject = "test"
		}
		actions, err = processor.ProcessNATS(subject, msgBytes, headers)
	}

	if err != nil {
		return marshalResult(evaluateResult{Error: fmt.Sprintf("processing error: %v", err)})
	}

	// Build result
	result := evaluateResult{
		Matched: len(actions) > 0,
		Actions: make([]actionResult, 0, len(actions)),
	}

	for _, a := range actions {
		if a.NATS != nil {
			payload := a.NATS.Payload
			if a.NATS.Passthrough && len(a.NATS.RawPayload) > 0 {
				payload = string(a.NATS.RawPayload)
			}
			result.Actions = append(result.Actions, actionResult{
				Type:        "nats",
				Subject:     a.NATS.Subject,
				Payload:     payload,
				Headers:     a.NATS.Headers,
				Passthrough: a.NATS.Passthrough,
			})
		} else if a.HTTP != nil {
			payload := a.HTTP.Payload
			if a.HTTP.Passthrough && len(a.HTTP.RawPayload) > 0 {
				payload = string(a.HTTP.RawPayload)
			}
			result.Actions = append(result.Actions, actionResult{
				Type:        "http",
				URL:         a.HTTP.URL,
				Method:      a.HTTP.Method,
				Payload:     payload,
				Headers:     a.HTTP.Headers,
				Passthrough: a.HTTP.Passthrough,
			})
		}
	}

	return marshalResult(result)
}

func errorResult(msg string) string {
	return marshalResult(evaluateResult{Error: msg})
}

func marshalResult(result evaluateResult) string {
	b, err := json.Marshal(result)
	if err != nil {
		return `{"error":"failed to marshal result"}`
	}
	return string(b)
}

func main() {
	js.Global().Set("evaluateRule", js.FuncOf(evaluateRule))

	// Block forever — WASM must stay alive for JS to call the registered function
	select {}
}

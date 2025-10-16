// cmd/rule-tester/main.go

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"rule-router/internal/logger"
	"rule-router/internal/tester"
)

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
	subjectOverride := flag.String("subject", "", "Manually specify a NATS subject for Quick Check mode (for NATS triggers).")
	rulesDir := flag.String("rules", "", "Path to the root directory for rules.")

	// Additional flags
	kvMockPath := flag.String("kv-mock", "", "Path to mock KV data file for Quick Check mode.")
	outputFormat := flag.String("output", "pretty", "Output format: pretty, json")
	verbose := flag.Bool("verbose", false, "Show detailed output for failures")
	noOverwrite := flag.Bool("no-overwrite", false, "Skip scaffold if test directory exists")
	parallel := flag.Int("parallel", 4, "Number of parallel test workers (0 = sequential)")

	flag.Parse()

	// Initialize Tester
	log := logger.NewNopLogger() // Use NopLogger for the test utility itself
	testRunner := tester.New(log, *verbose, *parallel)

	// Route to appropriate mode
	if *lint && *rulesDir != "" {
		return testRunner.Lint(*rulesDir)
	} else if *scaffold != "" {
		return testRunner.Scaffold(*scaffold, *noOverwrite)
	} else if *test && *rulesDir != "" {
		if *outputFormat == "pretty" {
			fmt.Printf("▶ RUNNING TESTS in %s\n\n", *rulesDir)
		}

		summary, err := testRunner.RunBatchTest(*rulesDir)

		if *outputFormat == "json" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			if encodeErr := encoder.Encode(summary); encodeErr != nil {
				return encodeErr
			}
		} else {
			printSummaryPretty(summary)
		}

		if err != nil {
			return err
		}

		if summary.Failed > 0 {
			return fmt.Errorf("tests failed")
		}
		return nil

	} else if *rulePath != "" && *messagePath != "" {
		return testRunner.QuickCheck(*rulePath, *messagePath, *subjectOverride, *kvMockPath)
	} else {
		return fmt.Errorf("invalid usage - use --help for options")
	}
}

func printSummaryPretty(summary tester.TestSummary) {
	fmt.Println("--- SUMMARY ---")
	fmt.Printf("Total Tests: %d, Passed: %d, Failed: %d\n",
		summary.Total, summary.Passed, summary.Failed)
	fmt.Printf("Duration: %dms\n", summary.DurationMs)

	if summary.Failed > 0 {
		fmt.Println("\n--- FAILURES ---")
		for _, result := range summary.Results {
			if !result.Passed {
				fmt.Printf("✖ %s\n", result.File)
				fmt.Printf("  Error: %s\n", result.Error)
				if result.Details != "" {
					fmt.Printf("  Details: %s\n", strings.ReplaceAll(result.Details, "\n", "\n  "))
				}
			}
		}
	}
}

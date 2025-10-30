// file: cmd/rule-cli/cmd/test.go
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"rule-router/internal/logger"
	"rule-router/internal/tester"
)

var testCmd = &cobra.Command{
	Use:   "test --rules <dir>",
	Short: "Run all test suites for rules in a directory",
	Long: `The test command discovers and runs all test suites. A test suite is a directory
named 'my_rule_test/' that corresponds to a 'my_rule.yaml' file. It runs all
'match_*.json' and 'not_match_*.json' files within the suite, validating
conditions, templates, and forEach logic.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rulesDir, _ := cmd.Flags().GetString("rules")
		outputFormat, _ := cmd.Flags().GetString("output")
		verbose, _ := cmd.Flags().GetBool("verbose")
		parallel, _ := cmd.Flags().GetInt("parallel")

		if rulesDir == "" {
			return cmd.Help()
		}

		if outputFormat == "pretty" {
			fmt.Printf("▶ RUNNING TESTS in %s\n\n", rulesDir)
		}

		log := logger.NewNopLogger()
		testRunner := tester.New(log, verbose, parallel)
		summary, err := testRunner.RunBatchTest(rulesDir)

		if outputFormat == "json" {
			encoder := json.NewEncoder(cmd.OutOrStdout())
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
	},
}

func init() {
	testCmd.Flags().StringP("rules", "r", "", "Path to the root directory for rules (required)")
	testCmd.Flags().StringP("output", "o", "pretty", "Output format: pretty, json")
	testCmd.Flags().BoolP("verbose", "v", false, "Show detailed output for failures")
	testCmd.Flags().IntP("parallel", "p", 4, "Number of parallel test workers (0 = sequential)")
	testCmd.MarkFlagRequired("rules")
}

// printSummaryPretty is a helper to print the test summary in a human-readable format.
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

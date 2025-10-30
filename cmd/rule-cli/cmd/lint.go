// file: cmd/rule-cli/cmd/lint.go
package cmd

import (
	"github.com/spf13/cobra"
	"rule-router/internal/logger"
	"rule-router/internal/tester"
)

var lintCmd = &cobra.Command{
	Use:   "lint --rules <dir>",
	Short: "Validate the syntax and structure of all rule files in a directory",
	Long: `The lint command recursively walks a directory to find all .yaml and .yml files.
It parses each file to ensure it conforms to the valid rule structure, including
triggers, actions, and conditions. This is a great first step for CI/CD pipelines.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rulesDir, _ := cmd.Flags().GetString("rules")
		if rulesDir == "" {
			return cmd.Help()
		}

		log := logger.NewNopLogger()
		testRunner := tester.New(log, false, 0)

		return testRunner.Lint(rulesDir)
	},
}

func init() {
	// The --rules flag is required for this command.
	lintCmd.Flags().StringP("rules", "r", "", "Path to the root directory for rules (required)")
	lintCmd.MarkFlagRequired("rules")
}

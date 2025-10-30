// file: cmd/rule-cli/cmd/check.go
package cmd

import (
	"github.com/spf13/cobra"
	"rule-router/internal/logger"
	"rule-router/internal/tester"
)

var checkCmd = &cobra.Command{
	Use:   "check --rule <rule.yaml> --message <message.json>",
	Short: "Run a quick check of a single rule against a single message",
	Long: `The check command provides a way to quickly test a single rule against a single
message payload for rapid iteration and debugging. It prints the outcome (match or no match)
and displays the fully rendered action(s) if the rule matches.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rulePath, _ := cmd.Flags().GetString("rule")
		messagePath, _ := cmd.Flags().GetString("message")
		subjectOverride, _ := cmd.Flags().GetString("subject")
		kvMockPath, _ := cmd.Flags().GetString("kv-mock")

		if rulePath == "" || messagePath == "" {
			return cmd.Help()
		}

		log := logger.NewNopLogger()
		testRunner := tester.New(log, false, 0)

		return testRunner.QuickCheck(rulePath, messagePath, subjectOverride, kvMockPath)
	},
}

func init() {
	checkCmd.Flags().String("rule", "", "Path to a single rule file (required)")
	checkCmd.Flags().String("message", "", "Path to a single message file (required)")
	checkCmd.Flags().String("subject", "", "Manually specify a NATS subject to override the one in the rule's trigger")
	checkCmd.Flags().String("kv-mock", "", "Path to a mock KV data file")
	checkCmd.MarkFlagRequired("rule")
	checkCmd.MarkFlagRequired("message")
}

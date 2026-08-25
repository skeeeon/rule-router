package cmd

import (
	"fmt"
	"strings"

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
		ruleIndex, _ := cmd.Flags().GetInt("rule-index")
		headerArgs, _ := cmd.Flags().GetStringArray("header")

		if rulePath == "" || messagePath == "" {
			return cmd.Help()
		}

		headers, err := parseHeaderFlags(headerArgs)
		if err != nil {
			return err
		}

		log := logger.NewNop()
		testRunner := tester.New(log, false, 0)

		return testRunner.QuickCheck(rulePath, messagePath, subjectOverride, kvMockPath, ruleIndex, headers)
	},
}

func init() {
	checkCmd.Flags().String("rule", "", "Path to a single rule file (required)")
	checkCmd.Flags().String("message", "", "Path to a single message file (required)")
	checkCmd.Flags().String("subject", "", "Manually specify a NATS subject to override the one in the rule's trigger")
	checkCmd.Flags().String("kv-mock", "", "Path to a mock KV data file")
	checkCmd.Flags().IntP("rule-index", "n", -1, "Index of the rule to check in a multi-rule file (0-based)")
	checkCmd.Flags().StringArray("header", nil, "Request header as 'Name: value' (repeatable). Set Content-Type to pick the payload decoder")
	checkCmd.MarkFlagRequired("rule")
	checkCmd.MarkFlagRequired("message")
}

// parseHeaderFlags turns repeated --header "Name: value" arguments into a map.
// Only the first colon separates the name from the value, so values may
// themselves contain colons.
func parseHeaderFlags(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}

	headers := make(map[string]string, len(args))
	for _, arg := range args {
		name, value, found := strings.Cut(arg, ":")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			return nil, fmt.Errorf("invalid --header %q: want 'Name: value'", arg)
		}
		headers[name] = strings.TrimSpace(value)
	}
	return headers, nil
}

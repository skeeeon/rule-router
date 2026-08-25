package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
	"rule-router/internal/tester"
)

var checkCmd = &cobra.Command{
	Use:   "check --rule <rule.yaml> --message <message.json>",
	Short: "Run a quick check of a single rule against a single message",
	Long: `The check command provides a way to quickly test a single rule against a single
message payload for rapid iteration and debugging. It prints the outcome (match or no match)
and displays the fully rendered action(s) if the rule matches.

The --message file is sent verbatim as the request body, so it can hold JSON, a
URL-encoded form, or plain text. Set --header 'Content-Type: ...' to choose how
the engine decodes it.`,
	Example: `  # JSON body against a NATS-triggered rule
  rule-cli check --rule rules/router/alert.yaml --message msg.json

  # Pick a rule out of a multi-rule file
  rule-cli check --rule rules/http/webhooks.yaml --message msg.json -n 2

  # Resolve {@header.X-GitHub-Event} in conditions
  rule-cli check --rule webhook.yaml --message msg.json \
      --header 'X-GitHub-Event: pull_request'

  # Form-encoded body: the Content-Type header selects the decoder, so
  # fields land as {device_id}, {user_id}, ... instead of one raw string
  printf 'device_id=9876&user_id=42' > body.form
  rule-cli check --rule idface.yaml --message body.form \
      --header 'Content-Type: application/x-www-form-urlencoded'

  # Resolve {@query.tenant} — paste the query string straight from a URL
  rule-cli check --rule webhook.yaml --message msg.json --query '?tenant=acme&page=2'

  # Resolve {@kv.bucket.key} lookups from a mock KV file
  rule-cli check --rule enrich.yaml --message msg.json --kv-mock mock_kv.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rulePath, _ := cmd.Flags().GetString("rule")
		messagePath, _ := cmd.Flags().GetString("message")
		subjectOverride, _ := cmd.Flags().GetString("subject")
		kvMockPath, _ := cmd.Flags().GetString("kv-mock")
		ruleIndex, _ := cmd.Flags().GetInt("rule-index")
		headerArgs, _ := cmd.Flags().GetStringArray("header")
		queryArg, _ := cmd.Flags().GetString("query")

		if rulePath == "" || messagePath == "" {
			return cmd.Help()
		}

		headers, err := parseHeaderFlags(headerArgs)
		if err != nil {
			return err
		}

		query, err := parseQueryFlag(queryArg)
		if err != nil {
			return err
		}

		log := logger.NewNop()
		testRunner := tester.New(log, false, 0)

		return testRunner.QuickCheck(rulePath, messagePath, subjectOverride, kvMockPath, ruleIndex, headers, query)
	},
}

func init() {
	checkCmd.Flags().String("rule", "", "Path to a single rule file (required)")
	checkCmd.Flags().String("message", "", "Path to a single message file (required)")
	checkCmd.Flags().String("subject", "", "Manually specify a NATS subject to override the one in the rule's trigger")
	checkCmd.Flags().String("kv-mock", "", "Path to a mock KV data file")
	checkCmd.Flags().IntP("rule-index", "n", -1, "Index of the rule to check in a multi-rule file (0-based)")
	checkCmd.Flags().StringArray("header", nil, "Request header as 'Name: value' (repeatable). Set Content-Type to pick the payload decoder")
	checkCmd.Flags().String("query", "", "Query string to resolve {@query.name} against, e.g. 'tenant=acme&debug=1'")
	checkCmd.MarkFlagRequired("rule")
	checkCmd.MarkFlagRequired("message")
}

// parseQueryFlag turns a raw --query string into query parameters, so a value
// can be pasted straight from a URL. A leading "?" is tolerated. Only the first
// value of a repeated name is kept, matching the gateway.
func parseQueryFlag(raw string) (rule.QueryParams, error) {
	raw = strings.TrimPrefix(raw, "?")
	if raw == "" {
		return nil, nil
	}

	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid --query %q: %w", raw, err)
	}

	query := make(rule.QueryParams, len(values))
	for name, v := range values {
		if len(v) > 0 {
			query[name] = v[0]
		}
	}
	return query, nil
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

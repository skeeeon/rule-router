// Package cmd defines the rule-cli subcommands: scaffolding a rules tree,
// creating rules interactively, linting them, checking them against sample
// input, and moving them in and out of a NATS KV bucket.
package cmd

import "github.com/spf13/cobra"

// AddCommands adds all the subcommands to the root command.
func AddCommands(root *cobra.Command) {
	root.AddCommand(newCmd)
	root.AddCommand(lintCmd)
	root.AddCommand(testCmd)
	root.AddCommand(scaffoldCmd)
	root.AddCommand(checkCmd)
	root.AddCommand(kvCmd)
}

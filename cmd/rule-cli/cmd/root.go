// file: cmd/rule-cli/cmd/root.go
package cmd

import "github.com/spf13/cobra"

// AddCommands adds all the subcommands to the root command.
func AddCommands(root *cobra.Command) {
	root.AddCommand(newCmd)
	root.AddCommand(lintCmd)
	root.AddCommand(testCmd)
	root.AddCommand(scaffoldCmd)
	root.AddCommand(checkCmd)
}

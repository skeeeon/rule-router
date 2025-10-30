// file: cmd/rule-cli/main.go
package main

import (
	"os"

	"github.com/spf13/cobra"
	"rule-router/cmd/rule-cli/cmd"
)

var rootCmd = &cobra.Command{
	Use:   "rule-cli",
	Short: "A CLI for creating, testing, and managing rules for the rule-router and http-gateway.",
	Long: `rule-cli is a comprehensive command-line tool that helps you build, validate,
and test your rule files in an offline environment. It supports the full rule
syntax, including NATS/HTTP triggers, array operations, and dependency mocking.`,
	// If a subcommand is not provided, default to showing help.
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	// Add all subcommands from the cmd package
	cmd.AddCommands(rootCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra prints the error, so we just need to exit
		os.Exit(1)
	}
}

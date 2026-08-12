// Command rule-cli scaffolds, authors, lints, and tests rule-router rules, and
// moves them between the filesystem and a NATS KV bucket. Rule checks run
// through the real rule engine, so what it reports is what the daemon would do.
package main

import (
	"os"

	"github.com/spf13/cobra"
	"rule-router/cmd/rule-cli/cmd"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "rule-cli",
	Version: version,
	Short:   "A CLI for creating, testing, and managing rules for the rule-router and http-gateway.",
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

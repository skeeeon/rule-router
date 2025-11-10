// file: cmd/rule-cli/cmd/new.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"rule-router/internal/cli"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
	"rule-router/internal/tester"
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new rule from a template or interactively",
	Long: `The new command helps you create new rule files. You can either generate a rule
from a predefined template for common use cases or build one step-by-step
through an interactive prompt.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		renderer := cli.NewRenderer()
		prompter := cli.NewPrompter()

		// Handle --list flag
		if list, _ := cmd.Flags().GetBool("list"); list {
			return listTemplates(renderer)
		}

		// Handle --show flag
		if showTemplate, _ := cmd.Flags().GetString("show"); showTemplate != "" {
			return showTemplateContent(renderer, showTemplate)
		}

		// Main logic: interactive or template-based creation
		templateName, _ := cmd.Flags().GetString("template")
		isInteractive, _ := cmd.Flags().GetBool("interactive")
		withTests, _ := cmd.Flags().GetBool("with-tests")
		output, _ := cmd.Flags().GetString("output")

		var ruleBytes []byte
		var err error

		// If interactive flag is set, or no template is provided, run the builder
		if isInteractive || templateName == "" {
			builder := cli.NewRuleBuilder(prompter)
			ruleBytes, err = builder.BuildRule()
			if err != nil {
				return fmt.Errorf("interactive build failed: %w", err)
			}
		} else {
			// Template-based creation
			content, err := renderer.GetTemplateContent(templateName)
			if err != nil {
				return err
			}
			ruleBytes = []byte(content)
		}

		// Determine output path
		if output == "" {
			output, err = prompter.AskWithDefault("Enter filename for the new rule:", "new-rule.yaml")
			if err != nil {
				return err
			}
		}
		// Ensure .yaml extension and place in 'rules' dir if appropriate
		output = normalizeOutputPath(output)

		// Write the file
		if err := writeFileWithConfirm(output, ruleBytes); err != nil {
			// If writeFileWithConfirm returns nil, it means the user cancelled, so we should exit gracefully.
			if err.Error() == "cancelled" {
				return nil
			}
			return err
		}
		fmt.Printf("%s✓ Success! Rule file '%s' created.%s\n", cli.ColorGreen, output, cli.ColorReset)

		// Phase 4: Validation and Test Scaffolding
		if err := validateRuleFile(output); err != nil {
			fmt.Printf("%sWarning: The generated rule has a validation issue: %v%s\n", cli.ColorYellow, err, cli.ColorReset)
		}

		scaffoldTests := withTests
		if !scaffoldTests {
			scaffoldTests, _ = prompter.Confirm("Generate test scaffold for this rule?")
		}

		if scaffoldTests {
			testRunner := tester.New(logger.NewNopLogger(), false, 0)
			if err := testRunner.Scaffold(output, false); err != nil {
				return fmt.Errorf("failed to scaffold tests: %w", err)
			}
		}

		return nil
	},
}

func init() {
	newCmd.Flags().StringP("template", "t", "", "Create a rule from a named template (e.g., nats-basic)")
	newCmd.Flags().StringP("output", "o", "", "Path to write the new rule file")
	newCmd.Flags().BoolP("interactive", "i", false, "Start the interactive rule builder")
	newCmd.Flags().Bool("list", false, "List available templates")
	newCmd.Flags().String("show", "", "Show the content of a specific template")
	newCmd.Flags().Bool("with-tests", false, "Automatically scaffold tests after creating the rule")
}

func listTemplates(r *cli.Renderer) error {
	templates, err := r.ListTemplates()
	if err != nil {
		return err
	}
	fmt.Println("Available templates:")
	for _, t := range templates {
		fmt.Printf("  - %s\n", t)
	}
	return nil
}

func showTemplateContent(r *cli.Renderer, templateName string) error {
	content, err := r.GetTemplateContent(templateName)
	if err != nil {
		return err
	}
	fmt.Printf("--- Template: %s ---\n", templateName)
	fmt.Println(content)
	return nil
}

func normalizeOutputPath(path string) string {
	if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
		path += ".yaml"
	}
	if filepath.Dir(path) == "." {
		if _, err := os.Stat("rules"); !os.IsNotExist(err) {
			path = filepath.Join("rules", path)
		}
	}
	return path
}

func writeFileWithConfirm(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("File '%s' already exists. Overwrite? (y/N): ", path)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Println("Cancelled.")
			return fmt.Errorf("cancelled") // Return a specific error to signal cancellation
		}
	}
	return os.WriteFile(path, data, 0644)
}

func validateRuleFile(path string) error {
	loader := rule.NewRulesLoader(logger.NewNopLogger(), nil)
	_, err := loader.LoadFromFile(path)
	return err
}

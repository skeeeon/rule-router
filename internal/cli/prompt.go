// file: internal/cli/prompt.go
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ANSI Color Codes for better output
const (
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorReset  = "\033[0m"
)

// Prompter defines the interface for user interaction, allowing for mock implementations in tests.
type Prompter interface {
	Ask(question string) (string, error)
	AskWithDefault(question, defaultVal string) (string, error)
	Confirm(question string) (bool, error)
	Select(question string, options []string) (int, error)
}

// StdinPrompter is the standard implementation of Prompter that reads from os.Stdin.
type StdinPrompter struct {
	reader *bufio.Reader
}

// NewPrompter creates a new prompter that reads from standard input.
func NewPrompter() *StdinPrompter {
	return &StdinPrompter{
		reader: bufio.NewReader(os.Stdin),
	}
}

// Ask poses a question to the user and returns their input.
func (p *StdinPrompter) Ask(question string) (string, error) {
	fmt.Printf("%s%s%s ", ColorYellow, question, ColorReset)
	input, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// AskWithDefault poses a question with a default value.
func (p *StdinPrompter) AskWithDefault(question, defaultVal string) (string, error) {
	q := fmt.Sprintf("%s [%s]:", question, defaultVal)
	input, err := p.Ask(q)
	if err != nil {
		return "", err
	}
	if input == "" {
		return defaultVal, nil
	}
	return input, nil
}

// Confirm asks a yes/no question.
func (p *StdinPrompter) Confirm(question string) (bool, error) {
	input, err := p.Ask(fmt.Sprintf("%s (y/N):", question))
	if err != nil {
		return false, err
	}
	return strings.ToLower(input) == "y", nil
}

// Select presents a list of options and asks the user to choose one.
func (p *StdinPrompter) Select(question string, options []string) (int, error) {
	fmt.Printf("%s%s%s\n", ColorBlue, question, ColorReset)
	for i, opt := range options {
		fmt.Printf("  %d) %s\n", i+1, opt)
	}

	for {
		input, err := p.Ask("Your choice:")
		if err != nil {
			return -1, err
		}
		choice, err := strconv.Atoi(input)
		if err == nil && choice > 0 && choice <= len(options) {
			return choice - 1, nil // Return 0-based index
		}
		fmt.Println("Invalid option. Please try again.")
	}
}

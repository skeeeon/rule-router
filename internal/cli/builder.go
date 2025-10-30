// file: internal/cli/builder.go
package cli

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rule-router/internal/rule"
)

// RuleBuilder interactively constructs a rule.Rule object.
type RuleBuilder struct {
	prompter Prompter
}

// NewRuleBuilder creates a new interactive rule builder.
func NewRuleBuilder(p Prompter) *RuleBuilder {
	return &RuleBuilder{prompter: p}
}

// BuildRule starts the interactive process to build a complete rule.
func (rb *RuleBuilder) BuildRule() ([]byte, error) {
	var r rule.Rule

	fmt.Printf("\n%s--- Building a New Rule ---%s\n", ColorGreen, ColorReset)

	// 1. Get Trigger
	trigger, err := rb.getTrigger()
	if err != nil {
		return nil, err
	}
	r.Trigger = *trigger

	// 2. Get Conditions
	conditions, err := rb.getConditions()
	if err != nil {
		return nil, err
	}
	r.Conditions = conditions

	// 3. Get Action
	action, err := rb.getAction()
	if err != nil {
		return nil, err
	}
	r.Action = *action

	// Marshal the final rule to YAML with custom indentation
	ruleList := []rule.Rule{r}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Set indentation to 2 spaces

	if err := encoder.Encode(ruleList); err != nil {
		return nil, fmt.Errorf("failed to marshal rule to YAML: %w", err)
	}

	yamlBytes := buf.Bytes()

	// Add header comment
	header := fmt.Sprintf("# Rule file created on %s\n#\n", time.Now().Format(time.RFC1123))
	return append([]byte(header), yamlBytes...), nil
}

func (rb *RuleBuilder) getTrigger() (*rule.Trigger, error) {
	fmt.Printf("\n%s1. Trigger (What starts the rule?)%s\n", ColorBlue, ColorReset)
	choice, err := rb.prompter.Select("Select Trigger Type:", []string{"NATS (Message Bus)", "HTTP (Webhook)"})
	if err != nil {
		return nil, err
	}

	var trigger rule.Trigger
	if choice == 0 { // NATS
		subject, _ := rb.prompter.Ask("Enter NATS Trigger Subject (e.g., 'sensors.temp.>'):")
		trigger.NATS = &rule.NATSTigger{Subject: subject}
	} else { // HTTP
		path, _ := rb.prompter.Ask("Enter HTTP Trigger Path (e.g., '/webhooks/github'):")
		method, _ := rb.prompter.AskWithDefault("Enter HTTP Method (e.g., 'POST', press Enter for all):", "")
		trigger.HTTP = &rule.HTTPTrigger{Path: path, Method: strings.ToUpper(method)}
	}
	return &trigger, nil
}

func (rb *RuleBuilder) getConditions() (*rule.Conditions, error) {
	fmt.Printf("\n%s2. Conditions (When should the rule run?)%s\n", ColorBlue, ColorReset)
	addConditions, err := rb.prompter.Confirm("Add conditions to this rule?")
	if err != nil || !addConditions {
		return nil, err
	}

	fmt.Println(ColorBlue + ContextHelp + ColorReset)
	return rb.getConditionsRecursive("  ")
}

func (rb *RuleBuilder) getConditionsRecursive(indent string) (*rule.Conditions, error) {
	op, _ := rb.prompter.AskWithDefault(indent+"Logical operator for this group:", "and")
	conds := &rule.Conditions{Operator: op}

	fmt.Println(indent + "Enter conditions for this group (press Enter on 'Field' to finish):")
	for {
		field, err := rb.prompter.Ask(indent + "  - Field:")
		if err != nil || field == "" {
			break
		}

		var operator string
		for {
			operator, _ = rb.prompter.Ask(indent + "  - Operator:")
			if IsValidOperator(operator) {
				break
			}
			fmt.Println("    Invalid operator. Please try again.")
		}

		item := rule.Condition{Field: field, Operator: operator}

		// --- FIX: Changed 'else if' to a mutually exclusive 'else' block ---
		if operator == "any" || operator == "all" || operator == "none" {
			// Handle array operators
			fmt.Println(indent + "    Defining nested conditions for the array operator...")
			nested, err := rb.getConditionsRecursive(indent + "      ")
			if err != nil {
				return nil, err
			}
			item.Conditions = nested
		} else {
			// Handle all other (non-array) operators
			if operator != "exists" {
				valueStr, _ := rb.prompter.Ask(indent + "  - Value:")
				if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
					item.Value = f
				} else if b, err := strconv.ParseBool(valueStr); err == nil {
					item.Value = b
				} else {
					item.Value = valueStr
				}
			}
		}
		// --- END FIX ---

		conds.Items = append(conds.Items, item)
	}

	addGroup, _ := rb.prompter.Confirm(indent + "Add a nested condition group?")
	if addGroup {
		nestedGroup, err := rb.getConditionsRecursive(indent + "  ")
		if err != nil {
			return nil, err
		}
		conds.Groups = append(conds.Groups, *nestedGroup)
	}

	return conds, nil
}

func (rb *RuleBuilder) getAction() (*rule.Action, error) {
	fmt.Printf("\n%s3. Action (What should the rule do?)%s\n", ColorBlue, ColorReset)
	choice, err := rb.prompter.Select("Select Action Type:", []string{"NATS (Publish Message)", "HTTP (Send Webhook)"})
	if err != nil {
		return nil, err
	}

	var action rule.Action
	if choice == 0 { // NATS
		natsAction, err := rb.getNATSAction()
		if err != nil {
			return nil, err
		}
		action.NATS = natsAction
	} else { // HTTP
		httpAction, err := rb.getHTTPAction()
		if err != nil {
			return nil, err
		}
		action.HTTP = httpAction
	}
	return &action, nil
}

func (rb *RuleBuilder) getNATSAction() (*rule.NATSAction, error) {
	cardinality, _ := rb.prompter.Select("Select Action Cardinality:", []string{"Single Action", "ForEach (Batch) Action"})
	if cardinality == 0 { // Single
		subject, _ := rb.prompter.Ask("Enter NATS Action Subject (e.g., 'alerts.high_temp'):")
		payload := `{
  "message": "Rule matched and processed.",
  "processed_at": "{@timestamp()}"
}`
		return &rule.NATSAction{Subject: subject, Payload: payload}, nil
	}

	// ForEach
	forEachField, _ := rb.prompter.Ask("Enter path to array field in message (e.g., 'notifications'):")
	subject, _ := rb.prompter.Ask("Enter NATS Action Subject (can use element fields, e.g., 'alerts.{id}'):")
	payload := `{
  "element_id": "{id}",
  "batch_id": "{@msg.batch_id}",
  "processed_at": "{@timestamp()}"
}`
	action := &rule.NATSAction{ForEach: forEachField, Subject: subject, Payload: payload}

	addFilter, _ := rb.prompter.Confirm("Add a filter to process only some elements?")
	if addFilter {
		filter, err := rb.getConditionsRecursive("    ")
		if err != nil {
			return nil, err
		}
		action.Filter = filter
	}
	return action, nil
}

func (rb *RuleBuilder) getHTTPAction() (*rule.HTTPAction, error) {
	cardinality, _ := rb.prompter.Select("Select Action Cardinality:", []string{"Single Action", "ForEach (Batch) Action"})
	method, _ := rb.prompter.AskWithDefault("Enter HTTP Method:", "POST")
	payload := `{
  "message": "Rule matched and processed.",
  "timestamp": "{@timestamp()}"
}`
	retry := &rule.RetryConfig{MaxAttempts: 3, InitialDelay: "1s"}

	if cardinality == 0 { // Single
		url, _ := rb.prompter.Ask("Enter HTTP Action URL (e.g., 'https://api.example.com/alerts'):")
		return &rule.HTTPAction{URL: url, Method: method, Payload: payload, Retry: retry}, nil
	}

	// ForEach
	forEachField, _ := rb.prompter.Ask("Enter path to array field in message (e.g., 'items'):")
	url, _ := rb.prompter.Ask("Enter HTTP Action URL (can use element fields, e.g., 'https://api.example.com/items/{id}'):")
	action := &rule.HTTPAction{ForEach: forEachField, URL: url, Method: method, Payload: payload, Retry: retry}

	addFilter, _ := rb.prompter.Confirm("Add a filter to process only some elements?")
	if addFilter {
		filter, err := rb.getConditionsRecursive("    ")
		if err != nil {
			return nil, err
		}
		action.Filter = filter
	}
	return action, nil
}

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
		trigger.NATS = &rule.NATSTrigger{Subject: subject}
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
		// Get field with validation
		field, err := rb.getConditionField(indent + "  - ")
		if err != nil {
			return nil, err
		}
		if field == "" {
			break // User pressed Enter to finish
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

		// Handle array operators vs regular operators
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
				// NEW: Get value with variable comparison support
				valueStr, valueType, err := rb.getConditionValue(indent+"  - ", field)
				if err != nil {
					return nil, err
				}
				
				// Store value based on type
				switch valueType {
				case "variable":
					// Variable comparison - store as template string
					item.Value = valueStr
				case "number":
					// Parse as number
					if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
						item.Value = f
					} else {
						return nil, fmt.Errorf("invalid number: %s", valueStr)
					}
				case "boolean":
					// Parse as boolean
					if b, err := strconv.ParseBool(valueStr); err == nil {
						item.Value = b
					} else {
						return nil, fmt.Errorf("invalid boolean: %s", valueStr)
					}
				case "string":
					// Store as string
					item.Value = valueStr
				}
			}
		}

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

// getConditionField prompts for a condition field with validation and auto-fix
func (rb *RuleBuilder) getConditionField(indent string) (string, error) {
	prompt := indent + "Field (use {braces}, e.g., {temperature} or {@time.hour}):"
	
	for {
		field, err := rb.prompter.Ask(prompt)
		if err != nil {
			return "", err
		}
		
		// Empty input means user wants to finish
		if strings.TrimSpace(field) == "" {
			return "", nil
		}
		
		// Check if field uses template syntax
		if !isTemplateVariable(field) {
			fmt.Printf("%s    ⚠️  Field must use template syntax with {braces}%s\n", ColorYellow, ColorReset)
			
			// Offer auto-fix
			suggested := fmt.Sprintf("{%s}", strings.TrimSpace(field))
			fmt.Printf("    Did you mean: %s? (Y/n): ", suggested)
			var response string
			fmt.Scanln(&response)
			response = strings.ToLower(strings.TrimSpace(response))
			
			if response == "" || response == "y" || response == "yes" {
				fmt.Printf("%s    ✓ Using: %s%s\n", ColorGreen, suggested, ColorReset)
				return suggested, nil
			}
			
			// User declined auto-fix, prompt again
			fmt.Println("    Please enter field with {braces} or press Enter to skip.")
			continue
		}
		
		return field, nil
	}
}

// getConditionValue prompts for a condition value with support for variable comparisons
// Returns: (value string, value type, error)
// Value types: "variable", "number", "boolean", "string"
func (rb *RuleBuilder) getConditionValue(indent, field string) (string, string, error) {
	// Show helpful context about variable comparisons
	fmt.Printf("\n%s    💡 Value Types:%s\n", ColorBlue, ColorReset)
	fmt.Println("       1. Variable comparison: {threshold}, {@kv.config:max}, {@time.hour}")
	fmt.Println("       2. Literal number: 30, 25.5, -10")
	fmt.Println("       3. Literal boolean: true, false")
	fmt.Println("       4. Literal string: active, \"hello world\"")
	fmt.Printf("%s    Tip: Use variables for dynamic comparisons!%s\n\n", ColorBlue, ColorReset)
	
	prompt := indent + "Value (variable or literal):"
	
	for {
		valueStr, err := rb.prompter.Ask(prompt)
		if err != nil {
			return "", "", err
		}
		
		valueStr = strings.TrimSpace(valueStr)
		if valueStr == "" {
			fmt.Println("    Value cannot be empty. Please try again.")
			continue
		}
		
		// Check if it's a template variable
		if isTemplateVariable(valueStr) {
			// Validate template syntax
			varName := extractInnerField(valueStr)
			if varName == "" {
				fmt.Printf("%s    ✖ Invalid template syntax (empty variable name)%s\n", ColorYellow, ColorReset)
				fmt.Println("    Please enter a valid template like {field} or {@system.var}")
				continue
			}
			
			// Show what we detected
			fmt.Printf("%s    ✓ Variable comparison detected: %s%s\n", ColorGreen, valueStr, ColorReset)
			
			// Offer example of what this means
			fieldName := extractInnerField(field)
			fmt.Printf("    This will compare %s against %s dynamically\n", fieldName, varName)
			
			return valueStr, "variable", nil
		}
		
		// Try to parse as number
		if _, err := strconv.ParseFloat(valueStr, 64); err == nil {
			fmt.Printf("%s    ✓ Number literal: %s%s\n", ColorGreen, valueStr, ColorReset)
			return valueStr, "number", nil
		}
		
		// Try to parse as boolean
		if valueStr == "true" || valueStr == "false" {
			fmt.Printf("%s    ✓ Boolean literal: %s%s\n", ColorGreen, valueStr, ColorReset)
			return valueStr, "boolean", nil
		}
		
		// Default to string
		// Remove quotes if present (user might have added them)
		valueStr = strings.Trim(valueStr, "\"")
		fmt.Printf("%s    ✓ String literal: \"%s\"%s\n", ColorGreen, valueStr, ColorReset)
		return valueStr, "string", nil
	}
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
		subject, _ := rb.prompter.Ask("Enter NATS Action Subject (e.g., 'alerts.high_temp.{device_id}'):")
		payload := `{
  "message": "Rule matched and processed.",
  "device_id": "{device_id}",
  "processed_at": "{@timestamp()}"
}`
		return &rule.NATSAction{Subject: subject, Payload: payload}, nil
	}

	// ForEach
	forEachField, err := rb.getForEachField()
	if err != nil {
		return nil, err
	}
	
	subject, _ := rb.prompter.Ask("Enter NATS Action Subject (can use element fields, e.g., 'alerts.{id}'):")
	payload := `{
  "element_id": "{id}",
  "batch_id": "{@msg.batch_id}",
  "processed_at": "{@timestamp()}"
}`
	action := &rule.NATSAction{ForEach: forEachField, Subject: subject, Payload: payload}

	addFilter, _ := rb.prompter.Confirm("Add a filter to process only some elements?")
	if addFilter {
		fmt.Println("\n" + ColorBlue + "Filter Conditions" + ColorReset)
		fmt.Println("These conditions evaluate against each array element.")
		fmt.Println("Use {field} for element fields, {@msg.field} for root message fields.")
		fmt.Println("\n" + ColorYellow + "💡 TIP: You can use variable comparisons in filters too!" + ColorReset)
		fmt.Println("Example: {value} > {@msg.threshold}")
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
		url, _ := rb.prompter.Ask("Enter HTTP Action URL (e.g., 'https://api.example.com/alerts/{device_id}'):")
		return &rule.HTTPAction{URL: url, Method: method, Payload: payload, Retry: retry}, nil
	}

	// ForEach
	forEachField, err := rb.getForEachField()
	if err != nil {
		return nil, err
	}
	
	url, _ := rb.prompter.Ask("Enter HTTP Action URL (can use element fields, e.g., 'https://api.example.com/items/{id}'):")
	action := &rule.HTTPAction{ForEach: forEachField, URL: url, Method: method, Payload: payload, Retry: retry}

	addFilter, _ := rb.prompter.Confirm("Add a filter to process only some elements?")
	if addFilter {
		fmt.Println("\n" + ColorBlue + "Filter Conditions" + ColorReset)
		fmt.Println("These conditions evaluate against each array element.")
		fmt.Println("Use {field} for element fields, {@msg.field} for root message fields.")
		fmt.Println("\n" + ColorYellow + "💡 TIP: You can use variable comparisons in filters too!" + ColorReset)
		fmt.Println("Example: {value} > {@msg.threshold}")
		filter, err := rb.getConditionsRecursive("    ")
		if err != nil {
			return nil, err
		}
		action.Filter = filter
	}
	return action, nil
}

// getForEachField prompts for a forEach field with validation and auto-fix
func (rb *RuleBuilder) getForEachField() (string, error) {
	prompt := "Enter path to array field (use {braces}, e.g., {notifications} or {data.items}):"
	
	for {
		field, err := rb.prompter.Ask(prompt)
		if err != nil {
			return "", err
		}
		
		field = strings.TrimSpace(field)
		if field == "" {
			return "", fmt.Errorf("forEach field cannot be empty")
		}
		
		// Check if field uses template syntax
		if !isTemplateVariable(field) {
			fmt.Printf("%s    ⚠️  ForEach field must use template syntax with {braces}%s\n", ColorYellow, ColorReset)
			
			// Offer auto-fix
			suggested := fmt.Sprintf("{%s}", field)
			fmt.Printf("    Did you mean: %s? (Y/n): ", suggested)
			var response string
			fmt.Scanln(&response)
			response = strings.ToLower(strings.TrimSpace(response))
			
			if response == "" || response == "y" || response == "yes" {
				fmt.Printf("%s    ✓ Using: %s%s\n", ColorGreen, suggested, ColorReset)
				return suggested, nil
			}
			
			// User declined auto-fix, prompt again
			fmt.Println("    Please enter field with {braces}.")
			continue
		}
		
		// Additional validation: forEach cannot contain wildcards
		innerField := extractInnerField(field)
		if strings.Contains(innerField, "*") || strings.Contains(innerField, ">") {
			fmt.Printf("%s    ✖ ForEach field cannot contain wildcards (* or >)%s\n", ColorYellow, ColorReset)
			fmt.Println("    Please enter a specific array path.")
			continue
		}
		
		return field, nil
	}
}

// isTemplateVariable checks if a string is a template variable with {braces}
func isTemplateVariable(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}

// extractInnerField extracts the content from {field} -> field
func extractInnerField(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return s[1 : len(s)-1]
	}
	return s
}

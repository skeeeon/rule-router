// file: internal/rule/template.go

package rule

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"rule-router/internal/logger"
)

// TemplateEngine processes rule template strings.
type TemplateEngine struct {
	logger *logger.Logger
}

// NewTemplateEngine creates a new TemplateEngine.
func NewTemplateEngine(log *logger.Logger) *TemplateEngine {
	return &TemplateEngine{logger: log}
}

// Execute renders a template string using the provided context.
// Optimized: Uses a single-pass recursive scanner instead of Regex.
func (te *TemplateEngine) Execute(template string, context *EvaluationContext) (string, error) {
	// Fast path: no variables
	if !strings.Contains(template, "{") {
		return template, nil
	}
	return te.parseRecursive(template, context, 0)
}

// parseRecursive scans the string and resolves variables, handling nesting via recursion.
// depth prevents infinite recursion (though logical loops shouldn't happen here).
func (te *TemplateEngine) parseRecursive(input string, context *EvaluationContext, depth int) (string, error) {
	if depth > 10 { // Grug safety check
		return input, fmt.Errorf("template nesting too deep")
	}

	var sb strings.Builder
	// Heuristic: Allocate slightly more than input to account for expansion
	sb.Grow(len(input) * 2)

	length := len(input)
	i := 0

	for i < length {
		char := input[i]

		if char == '{' {
			// Start of a variable? Find the BALANCING closing brace.
			end := -1
			balance := 1
			for j := i + 1; j < length; j++ {
				if input[j] == '{' {
					balance++
				} else if input[j] == '}' {
					balance--
				}

				if balance == 0 {
					end = j
					break
				}
			}

			if end != -1 {
				// We found a complete token: {content}
				// Extract "content" (without outer braces)
				rawContent := input[i+1 : end]

				// CHECK: Is this actually a variable template?
				// If it contains characters like quotes, spaces (except inside nested braces),
				// it is likely JSON structure, not a variable.
				if isValidTemplateStructure(rawContent) {
					// RECURSION STEP:
					// If the content itself contains braces (e.g. "@kv.{bucket}:key"),
					// we must resolve those INNER variables first.
					var resolvedVarName string
					if strings.Contains(rawContent, "{") {
						var err error
						resolvedVarName, err = te.parseRecursive(rawContent, context, depth+1)
						if err != nil {
							return "", err
						}
					} else {
						resolvedVarName = rawContent
					}

					// Now resolvedVarName is ready (e.g. "@kv.mybucket:key")
					// Resolve it against the context
					val, _ := context.ResolveValue(resolvedVarName)

					// System functions check (optimized to avoid regex)
					if strings.HasSuffix(resolvedVarName, "()") && strings.HasPrefix(resolvedVarName, "@") {
						sb.WriteString(te.processSystemFunction(resolvedVarName[1:])) // strip @
					} else {
						sb.WriteString(te.convertToString(val))
					}

					// Advance cursor past the closing '}'
					i = end + 1
					continue
				}
				// If not valid template structure (e.g. contains quotes/spaces), 
				// treat matching brace as literal and fall through to write char.
			}
		}

		// Just a normal character (or an unmatched/non-variable {), write it
		sb.WriteByte(char)
		i++
	}

	return sb.String(), nil
}

// isValidTemplateStructure checks if the string inside braces looks like a valid variable.
// It allows nested braces {...} but enforces valid variable characters elsewhere.
// Valid chars: alphanumeric, _, ., :, (, ), =, /, -, @
// Invalid chars: space, ", ', newline, etc.
func isValidTemplateStructure(s string) bool {
	length := len(s)
	if length == 0 {
		return false
	}

	for i := 0; i < length; i++ {
		c := s[i]
		if c == '{' {
			// Skip nested block - we assume inner blocks will be validated during recursion
			balance := 1
			for j := i + 1; j < length; j++ {
				if s[j] == '{' {
					balance++
				} else if s[j] == '}' {
					balance--
				}
				if balance == 0 {
					i = j // Advance i to the closing brace
					break
				}
			}
			if balance != 0 {
				return false // Unbalanced nested block
			}
		} else if c == '}' {
			return false // Should have been skipped by the loop above
		} else {
			if !isValidVariableChar(c) {
				return false
			}
		}
	}
	return true
}

func isValidVariableChar(c byte) bool {
	// [a-zA-Z0-9_.:()=/-] + @
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case '_', '.', ':', '(', ')', '=', '/', '-', '@':
		return true
	}
	return false
}

// processSystemFunction handles system functions: uuid7(), timestamp()
// Note: Input 'function' string should not have the leading '@'
func (te *TemplateEngine) processSystemFunction(function string) string {
	switch function {
	case "uuid4()":
		return uuid.New().String()
	case "uuid7()":
		id, err := uuid.NewV7()
		if err != nil {
			return ""
		}
		return id.String()
	case "timestamp()":
		return time.Now().UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

// convertToString converts an interface to its string representation for templating.
func (te *TemplateEngine) convertToString(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case map[string]interface{}, []interface{}:
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", v)
	}
}

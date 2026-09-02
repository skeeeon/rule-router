package rule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
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
		return input, errors.New("template nesting too deep")
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

					// System functions are checked first (without a regex): a
					// call like {@uuid7()} or {@random.int(1,100)} has nothing to
					// look up, so resolving it against the context beforehand was
					// wasted work.
					if strings.HasPrefix(resolvedVarName, "@") && strings.HasSuffix(resolvedVarName, ")") {
						// processSystemFunction reports whether it recognized the
						// name. An unrecognized one falls through to ordinary
						// variable resolution rather than being swallowed, so a
						// name that merely happens to end in ')' still resolves.
						if out, handled := te.processSystemFunction(resolvedVarName[1:]); handled { // strip @
							sb.WriteString(out)
							i = end + 1
							continue
						}
					}

					// Resolve it against the context
					val, found := context.ResolveValue(resolvedVarName)

					// A variable that doesn't resolve renders as an empty
					// string. That's intentional (templates stay lenient), but
					// it silently masks typos like {sensor.reeding}, so leave a
					// debug breadcrumb for operators chasing blank fields.
					if !found {
						te.logger.Debug("template variable did not resolve; rendering empty",
							"variable", resolvedVarName)
					}
					sb.WriteString(te.convertToString(val))

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
// Valid chars: alphanumeric, _, ., :, (, ), =, /, -, @, ,
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
	// [a-zA-Z0-9_.:()=/-,] + @
	//
	// The comma separates arguments in a parameterized function such as
	// {@random.int(1,100)}. Its absence here was an accident rather than a
	// design: '-' and '=' were already accepted, so {a-b} and {a=b} were already
	// treated as variables while {a,b} alone fell through as a literal. The web
	// builder's own template scanner (JsonTextarea.vue::substituteTemplates)
	// accepts any body without braces, quotes, or newlines, so it has always
	// allowed commas — this narrows a Go/JS divergence rather than opening one.
	//
	// The cost: {foo,bar} used to render literally and now renders as an empty
	// string, because it becomes a variable lookup that misses. Pinned by
	// TestTemplate_CommaMakesBracesAVariable.
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case '_', '.', ':', '(', ')', '=', '/', '-', '@', ',':
		return true
	}
	return false
}

// processSystemFunction handles system functions: uuid4(), uuid7(), timestamp(),
// and the parameterized random.* family.
//
// The second return reports whether the name was recognized as a function at
// all. False means "not a function", and the caller falls back to ordinary
// variable resolution — so a name that merely ends in ')' is never silently
// eaten, which is the failure mode this signature exists to prevent.
//
// Note: Input 'function' string should not have the leading '@'
func (te *TemplateEngine) processSystemFunction(function string) (string, bool) {
	switch function {
	case "uuid4()":
		return uuid.New().String(), true
	case "uuid7()":
		id, err := uuid.NewV7()
		if err != nil {
			return "", true
		}
		return id.String(), true
	case "timestamp()":
		return time.Now().UTC().Format(time.RFC3339), true
	}

	// Parameterized functions. Keyed off a known namespace rather than "takes
	// arguments" so the check stays closed: adding a family here is deliberate,
	// and an unrelated name cannot wander into it.
	if strings.HasPrefix(function, randomPrefix) {
		value, err := randomValue(function)
		if err != nil {
			// The loader rejects malformed calls at load time, so reaching here
			// means the arguments came from a runtime-resolved template. Warn
			// rather than Debug: the result is an empty substitution, which can
			// silently invalidate a JSON payload.
			te.logger.Warn("random function failed; rendering empty",
				"function", function,
				"error", err)
			return "", true
		}
		return value, true
	}

	return "", false
}

// convertToString converts an interface to its string representation for templating.
func (te *TemplateEngine) convertToString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case map[string]any, []any:
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", v)
	}
}

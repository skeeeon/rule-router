// file: internal/rule/template.go

package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"rule-router/internal/logger"
)

// OPTIMIZED: Single regex pattern to capture all variables in one pass
var (
	combinedVariablePattern = regexp.MustCompile(`\{(@?)([a-zA-Z0-9_.:()=/-]+)\}`)
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
func (te *TemplateEngine) Execute(template string, context *EvaluationContext) (string, error) {
	if !strings.Contains(template, "{") {
		// No variables - return as-is
		return template, nil
	}

	// PASS 1: Resolve all NON-KV variables first
	result := combinedVariablePattern.ReplaceAllStringFunc(template, func(match string) string {
		submatches := combinedVariablePattern.FindStringSubmatch(match)
		if len(submatches) != 3 {
			te.logger.Debug("unexpected regex match format in pass 1", "match", match)
			return match
		}

		isSystemVar := submatches[1] == "@"
		varContent := submatches[2]

		if isSystemVar && strings.HasPrefix(varContent, "kv.") {
			return match // Keep KV variables unchanged for pass 2
		}

		if isSystemVar {
			if strings.HasSuffix(varContent, "()") {
				return te.processSystemFunction(varContent)
			}
			val, _ := context.ResolveValue("@" + varContent)
			return te.convertToString(val)
		}

		val, _ := context.ResolveValue(varContent)
		return te.convertToString(val)
	})

	// PASS 2: Now resolve KV variables
	result = combinedVariablePattern.ReplaceAllStringFunc(result, func(match string) string {
		submatches := combinedVariablePattern.FindStringSubmatch(match)
		if len(submatches) != 3 {
			return ""
		}

		isSystemVar := submatches[1] == "@"
		varContent := submatches[2]

		if isSystemVar && strings.HasPrefix(varContent, "kv.") {
			val, _ := context.ResolveValue("@" + varContent)
			return te.convertToString(val)
		}

		// This should not happen if pass 1 worked, but as a fallback, return empty.
		if strings.Contains(match, "{") {
			return ""
		}
		return match // Return literals that were not variables
	})

	return result, nil
}

// processSystemFunction handles system functions: uuid7(), timestamp()
func (te *TemplateEngine) processSystemFunction(function string) string {
	switch function {
	case "uuid4()":
		id := uuid.New()
		te.logger.Debug("generated UUIDv4", "uuid", id.String())
		return id.String()
	case "uuid7()":
		id, err := uuid.NewV7()
		if err != nil {
			te.logger.Error("failed to generate UUIDv7", "error", err)
			return ""
		}
		te.logger.Debug("generated UUIDv7", "uuid", id.String())
		return id.String()
	case "timestamp()":
		timestamp := time.Now().UTC().Format(time.RFC3339)
		te.logger.Debug("generated timestamp", "timestamp", timestamp)
		return timestamp
	default:
		te.logger.Debug("unknown system function", "function", function)
		return ""
	}
}

// convertToString converts an interface to its string representation for templating.
func (te *TemplateEngine) convertToString(value interface{}) string {
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
	case nil:
		return ""
	case map[string]interface{}, []interface{}:
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			te.logger.Debug("failed to marshal complex value to JSON", "error", err)
			return ""
		}
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// file: internal/rule/path_matcher.go

package rule

import (
	"fmt"
	"strings"
)

// NewPathMatcher builds a PatternMatcher for an HTTP-style path with
// "/" segment separators. Wildcard semantics mirror NATS subjects:
//   - "*" matches exactly one path segment
//   - ">" matches one or more trailing segments and must be the last segment
//
// The leading "/" is required. Trailing slashes are tolerated.
func NewPathMatcher(path string) (*PatternMatcher, error) {
	if err := ValidatePathPattern(path); err != nil {
		return nil, fmt.Errorf("invalid path pattern: %w", err)
	}

	return newPatternMatcherFromTokens(path, pathTokens(path)), nil
}

// MatchPath reports whether the given request path matches the compiled
// pattern. The pattern matcher must have been built with NewPathMatcher.
func MatchPath(pm *PatternMatcher, requestPath string) bool {
	return pm.MatchTokens(pathTokens(requestPath))
}

// ValidatePathPattern enforces HTTP path pattern syntax.
func ValidatePathPattern(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path must start with '/': %s", path)
	}

	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}

	tokens := strings.Split(trimmed, "/")
	for i, token := range tokens {
		switch token {
		case "":
			return fmt.Errorf("empty segment at position %d", i)
		case ">":
			if i != len(tokens)-1 {
				return fmt.Errorf("'>' wildcard must be the last segment, found at position %d", i)
			}
		case "*":
			// valid anywhere
		default:
			if strings.Contains(token, "*") || strings.Contains(token, ">") {
				return fmt.Errorf("invalid wildcard usage in segment '%s' at position %d", token, i)
			}
		}
	}

	return nil
}

// PathContainsWildcards reports whether any segment of the path is a wildcard
// (exactly "*" or ">"). A bare "*" or ">" inside a longer segment is not a
// wildcard and is rejected at validation time.
func PathContainsWildcards(path string) bool {
	for _, token := range pathTokens(path) {
		if token == "*" || token == ">" {
			return true
		}
	}
	return false
}

// pathTokens splits an HTTP path into its segment tokens, stripping leading
// and trailing slashes. Returns an empty slice for "/" so a request to root
// can still be matched against patterns of length zero.
func pathTokens(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "/")
}

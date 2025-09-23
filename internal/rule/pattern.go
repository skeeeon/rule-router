//file: internal/rule/pattern.go

package rule

import (
	"fmt"
	"strings"
	"sync"
)

// WildcardType represents the type of token in a pattern
type WildcardType int

const (
	ExactToken WildcardType = iota
	SingleWildcard  // *
	GreedyWildcard  // >
)

// CompiledPattern represents a pre-compiled pattern for efficient matching
type CompiledPattern struct {
	exactTokens         []string      // Non-wildcard tokens
	wildcardMap         []WildcardType // Type of each token position
	hasTerminalWildcard bool          // Ends with >
	minTokens           int           // Minimum tokens required to match
	maxTokens           int           // Maximum tokens that can match (-1 for unlimited)
}

// PatternMatcher handles NATS-style subject pattern matching
type PatternMatcher struct {
	pattern  string
	tokens   []string
	compiled *CompiledPattern
	isPattern bool
}

// PatternCache provides optional caching for frequently matched subjects
type PatternCache struct {
	cache   map[string]bool
	mu      sync.RWMutex
	maxSize int
	enabled bool
}

// NewPatternMatcher creates a new pattern matcher for the given pattern
func NewPatternMatcher(pattern string) (*PatternMatcher, error) {
	if err := ValidatePattern(pattern); err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	pm := &PatternMatcher{
		pattern: pattern,
		tokens:  strings.Split(pattern, "."),
	}

	// Check if this is actually a pattern or just an exact match
	pm.isPattern = containsWildcards(pattern)

	// Pre-compile the pattern for efficient matching
	if pm.isPattern {
		pm.compiled = pm.compile()
	}

	return pm, nil
}

// IsPattern returns true if this contains wildcards
func (pm *PatternMatcher) IsPattern() bool {
	return pm.isPattern
}

// GetPattern returns the original pattern string
func (pm *PatternMatcher) GetPattern() string {
	return pm.pattern
}

// Match checks if the given subject matches this pattern
func (pm *PatternMatcher) Match(subject string) bool {
	// Fast path for exact matches
	if !pm.isPattern {
		return pm.pattern == subject
	}

	// Fast path for obvious mismatches
	if subject == "" {
		return pm.pattern == "" || pm.pattern == ">"
	}

	subjectTokens := strings.Split(subject, ".")
	return pm.matchTokens(subjectTokens, pm.compiled)
}

// compile pre-compiles the pattern for efficient matching
func (pm *PatternMatcher) compile() *CompiledPattern {
	compiled := &CompiledPattern{
		exactTokens: make([]string, len(pm.tokens)),
		wildcardMap: make([]WildcardType, len(pm.tokens)),
		minTokens:   0,
		maxTokens:   len(pm.tokens),
	}

	for i, token := range pm.tokens {
		switch token {
		case "*":
			compiled.wildcardMap[i] = SingleWildcard
			compiled.minTokens++
		case ">":
			compiled.wildcardMap[i] = GreedyWildcard
			compiled.hasTerminalWildcard = true
			compiled.maxTokens = -1 // Unlimited
			// > can match zero tokens, so don't increment minTokens
		default:
			compiled.wildcardMap[i] = ExactToken
			compiled.exactTokens[i] = token
			compiled.minTokens++
		}
	}

	// Adjust calculations for terminal wildcard
	if compiled.hasTerminalWildcard {
		compiled.minTokens = len(pm.tokens) - 1 // > can match zero tokens
	}

	return compiled
}

// matchTokens performs the actual pattern matching against subject tokens
func (pm *PatternMatcher) matchTokens(subjectTokens []string, compiled *CompiledPattern) bool {
	patternLen := len(compiled.wildcardMap)
	subjectLen := len(subjectTokens)

	// Quick length checks
	if compiled.hasTerminalWildcard {
		// Must have at least minTokens
		if subjectLen < compiled.minTokens {
			return false
		}
	} else {
		// Must match exactly
		if subjectLen != patternLen {
			return false
		}
	}

	// Recursive matching with early termination
	return pm.matchRecursive(subjectTokens, 0, compiled, 0)
}

// matchRecursive performs recursive token matching with optimization
func (pm *PatternMatcher) matchRecursive(subjectTokens []string, subjectIdx int, compiled *CompiledPattern, patternIdx int) bool {
	patternLen := len(compiled.wildcardMap)
	subjectLen := len(subjectTokens)

	// End of pattern reached
	if patternIdx >= patternLen {
		return subjectIdx >= subjectLen // Both should be exhausted
	}

	// End of subject but more pattern remains
	if subjectIdx >= subjectLen {
		// Only valid if remaining pattern is just a terminal >
		return patternIdx == patternLen-1 && compiled.wildcardMap[patternIdx] == GreedyWildcard
	}

	currentWildcard := compiled.wildcardMap[patternIdx]

	switch currentWildcard {
	case ExactToken:
		// Must match exactly
		if subjectTokens[subjectIdx] != compiled.exactTokens[patternIdx] {
			return false
		}
		return pm.matchRecursive(subjectTokens, subjectIdx+1, compiled, patternIdx+1)

	case SingleWildcard:
		// * matches exactly one token
		return pm.matchRecursive(subjectTokens, subjectIdx+1, compiled, patternIdx+1)

	case GreedyWildcard:
		// > matches zero or more tokens
		// This should be the last token in the pattern
		return true // > matches everything remaining

	default:
		return false
	}
}

// ValidatePattern validates NATS pattern syntax
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}

	tokens := strings.Split(pattern, ".")

	for i, token := range tokens {
		switch token {
		case "":
			return fmt.Errorf("empty token at position %d", i)
		case ">":
			// > must be the last token
			if i != len(tokens)-1 {
				return fmt.Errorf("'>' wildcard must be the last token, found at position %d", i)
			}
		case "*":
			// * is valid anywhere
		default:
			// Regular token - check for invalid wildcard usage
			if strings.Contains(token, "*") || strings.Contains(token, ">") {
				return fmt.Errorf("invalid wildcard usage in token '%s' at position %d", token, i)
			}
			// Could add more validation here (e.g., valid characters)
		}
	}

	return nil
}

// containsWildcards checks if a pattern contains any wildcards
func containsWildcards(pattern string) bool {
	return strings.Contains(pattern, "*") || strings.Contains(pattern, ">")
}

// NewPatternCache creates a new pattern cache
func NewPatternCache(maxSize int) *PatternCache {
	return &PatternCache{
		cache:   make(map[string]bool),
		maxSize: maxSize,
		enabled: maxSize > 0,
	}
}

// Get retrieves a cached result
func (pc *PatternCache) Get(subject string) (bool, bool) {
	if !pc.enabled {
		return false, false
	}

	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result, exists := pc.cache[subject]
	return result, exists
}

// Set stores a result in the cache
func (pc *PatternCache) Set(subject string, matches bool) {
	if !pc.enabled {
		return
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()

	// Simple eviction: clear cache when full
	if len(pc.cache) >= pc.maxSize {
		pc.cache = make(map[string]bool)
	}

	pc.cache[subject] = matches
}

// Clear empties the cache
func (pc *PatternCache) Clear() {
	if !pc.enabled {
		return
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache = make(map[string]bool)
}

// Stats returns cache statistics
func (pc *PatternCache) Stats() map[string]int {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	return map[string]int{
		"size":     len(pc.cache),
		"maxSize":  pc.maxSize,
		"enabled":  boolToInt(pc.enabled),
	}
}

// boolToInt converts bool to int for stats
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

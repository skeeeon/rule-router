// file: internal/rule/index.go

package rule

import (
	"sync"
	"sync/atomic"
	"time"

	"rule-router/internal/logger"
)

// RuleIndex provides efficient rule lookup for NATS subjects
// Supports both exact matches (O(1)) and wildcard patterns (O(n))
type RuleIndex struct {
	exactMatches map[string][]*Rule // exact subject -> rules
	patternRules []*PatternRule     // rules with wildcards
	stats        IndexStats
	mu           sync.RWMutex
	logger       *logger.Logger
}

// PatternRule wraps a rule with its compiled pattern matcher
type PatternRule struct {
	Rule    *Rule
	Matcher *PatternMatcher
}

// IndexStats tracks index performance metrics
type IndexStats struct {
	exactLookups   uint64
	patternChecks  uint64
	matches        uint64
	lastUpdated    time.Time
}

// NewRuleIndex creates a new rule index
func NewRuleIndex(log *logger.Logger) *RuleIndex {
	return &RuleIndex{
		exactMatches: make(map[string][]*Rule),
		patternRules: make([]*PatternRule, 0),
		logger:       log,
	}
}

// Add indexes a NATS-triggered rule for efficient lookup
// HTTP-triggered rules are ignored (they're stored separately in Processor)
func (idx *RuleIndex) Add(rule *Rule) {
	if rule == nil {
		idx.logger.Error("attempted to add nil rule to index")
		return
	}

	// Only index NATS-triggered rules
	if rule.Trigger.NATS == nil {
		idx.logger.Debug("skipping non-NATS rule in index")
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	subject := rule.Trigger.NATS.Subject

	if containsWildcards(subject) {
		// Pattern rule - needs matcher
		matcher, err := NewPatternMatcher(subject)
		if err != nil {
			idx.logger.Error("failed to create pattern matcher",
				"subject", subject,
				"error", err)
			return
		}

		patternRule := &PatternRule{
			Rule:    rule,
			Matcher: matcher,
		}
		idx.patternRules = append(idx.patternRules, patternRule)

		idx.logger.Debug("added pattern rule to index",
			"subject", subject,
			"totalPatternRules", len(idx.patternRules))
	} else {
		// Exact match rule
		idx.exactMatches[subject] = append(idx.exactMatches[subject], rule)

		idx.logger.Debug("added exact rule to index",
			"subject", subject,
			"existingRules", len(idx.exactMatches[subject]))
	}

	idx.stats.lastUpdated = time.Now()

	idx.logger.Info("rule added to index",
		"subject", subject,
		"isPattern", containsWildcards(subject),
		"totalExactSubjects", len(idx.exactMatches),
		"totalPatternRules", len(idx.patternRules))
}

// FindAllMatching returns all rules that match the given NATS subject
// First checks exact matches (O(1)), then pattern matches (O(n))
func (idx *RuleIndex) FindAllMatching(subject string) []*Rule {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	allMatches := make([]*Rule, 0)

	// Check exact matches first (O(1))
	atomic.AddUint64(&idx.stats.exactLookups, 1)
	if exactRules, found := idx.exactMatches[subject]; found {
		allMatches = append(allMatches, exactRules...)

		idx.logger.Debug("found exact rule matches",
			"subject", subject,
			"exactMatches", len(exactRules))
	}

	// Then check pattern matches (O(n))
	var patternMatches int
	for _, patternRule := range idx.patternRules {
		atomic.AddUint64(&idx.stats.patternChecks, 1)

		if patternRule.Matcher.Match(subject) {
			allMatches = append(allMatches, patternRule.Rule)
			patternMatches++

			idx.logger.Debug("pattern rule matched",
				"subject", subject,
				"pattern", patternRule.Rule.Trigger.NATS.Subject)
		}
	}

	if len(allMatches) > 0 {
		atomic.AddUint64(&idx.stats.matches, 1)
	}

	idx.logger.Debug("completed rule matching",
		"subject", subject,
		"totalMatches", len(allMatches),
		"exactMatches", len(allMatches)-patternMatches,
		"patternMatches", patternMatches)

	return allMatches
}

// GetSubjects returns all unique NATS subjects (both exact and patterns)
func (idx *RuleIndex) GetSubjects() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	totalCapacity := len(idx.exactMatches) + len(idx.patternRules)
	subjects := make([]string, 0, totalCapacity)

	// Add exact match subjects
	for subject := range idx.exactMatches {
		subjects = append(subjects, subject)
	}

	// Add pattern subjects
	for _, patternRule := range idx.patternRules {
		subjects = append(subjects, patternRule.Rule.Trigger.NATS.Subject)
	}

	idx.logger.Debug("retrieved all subjects",
		"exactSubjects", len(idx.exactMatches),
		"patternSubjects", len(idx.patternRules),
		"totalSubjects", len(subjects))

	return subjects
}

// GetSubscriptionSubjects returns subjects that should be used for JetStream subscriptions
// Converts wildcard patterns to NATS subscription format
func (idx *RuleIndex) GetSubscriptionSubjects() []string {
	subjects := idx.GetSubjects()

	idx.logger.Info("returning subscription subjects",
		"count", len(subjects),
		"subjects", subjects)

	return subjects
}

// Clear removes all indexed rules
func (idx *RuleIndex) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.exactMatches = make(map[string][]*Rule)
	idx.patternRules = make([]*PatternRule, 0)
	idx.stats = IndexStats{}

	idx.logger.Info("index cleared")
}

// GetRuleCounts returns the number of exact and pattern rules
func (idx *RuleIndex) GetRuleCounts() (exactCount, patternCount int) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	exactCount = len(idx.exactMatches)
	patternCount = len(idx.patternRules)
	return
}

// GetStats returns index statistics
func (idx *RuleIndex) GetStats() IndexStats {
	return IndexStats{
		exactLookups:  atomic.LoadUint64(&idx.stats.exactLookups),
		patternChecks: atomic.LoadUint64(&idx.stats.patternChecks),
		matches:       atomic.LoadUint64(&idx.stats.matches),
		lastUpdated:   idx.stats.lastUpdated,
	}
}

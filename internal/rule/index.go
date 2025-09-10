//file: internal/rule/index.go

package rule

import (
    "sync"
    "sync/atomic"
    "time"

    "rule-router/internal/logger"
)

type RuleIndex struct {
    exactMatches   map[string][]*Rule     // exact topic → rules
    patternRules   []*PatternRule         // all wildcard pattern rules
    mu             sync.RWMutex
    stats          IndexStats
    logger         *logger.Logger
}

// PatternRule wraps a rule with its compiled pattern matcher
type PatternRule struct {
    Rule    *Rule
    Matcher *PatternMatcher
}

type IndexStats struct {
    lookups       uint64
    matches       uint64
    exactMatches  uint64
    patternChecks uint64
    lastUpdated   time.Time
    mu            sync.RWMutex
}

func NewRuleIndex(log *logger.Logger) *RuleIndex {
    return &RuleIndex{
        exactMatches: make(map[string][]*Rule),
        patternRules: make([]*PatternRule, 0),
        stats: IndexStats{
            lastUpdated: time.Now(),
        },
        logger: log,
    }
}

func (idx *RuleIndex) Add(rule *Rule) {
    if rule == nil {
        idx.logger.Error("attempted to add nil rule to index")
        return
    }

    idx.mu.Lock()
    defer idx.mu.Unlock()

    // Determine if this is a pattern or exact match
    if containsWildcards(rule.Topic) {
        // It's a pattern rule
        matcher, err := NewPatternMatcher(rule.Topic)
        if err != nil {
            idx.logger.Error("failed to create pattern matcher for rule",
                "topic", rule.Topic,
                "error", err)
            return
        }

        patternRule := &PatternRule{
            Rule:    rule,
            Matcher: matcher,
        }
        idx.patternRules = append(idx.patternRules, patternRule)

        idx.logger.Debug("added pattern rule to index",
            "topic", rule.Topic,
            "totalPatternRules", len(idx.patternRules))
    } else {
        // It's an exact match rule
        idx.exactMatches[rule.Topic] = append(idx.exactMatches[rule.Topic], rule)

        idx.logger.Debug("added exact rule to index",
            "topic", rule.Topic,
            "existingRules", len(idx.exactMatches[rule.Topic]))
    }

    idx.stats.lastUpdated = time.Now()

    idx.logger.Info("rule added to index",
        "topic", rule.Topic,
        "isPattern", containsWildcards(rule.Topic),
        "totalExactTopics", len(idx.exactMatches),
        "totalPatternRules", len(idx.patternRules))
}

// Find returns rules matching the exact topic (legacy method for backward compatibility)
func (idx *RuleIndex) Find(topic string) []*Rule {
    idx.mu.RLock()
    defer idx.mu.RUnlock()

    atomic.AddUint64(&idx.stats.lookups, 1)
    rules := idx.exactMatches[topic]

    idx.logger.Debug("exact topic lookup",
        "topic", topic,
        "rulesFound", len(rules))

    if len(rules) > 0 {
        atomic.AddUint64(&idx.stats.matches, 1)
        atomic.AddUint64(&idx.stats.exactMatches, 1)
    }
    
    return rules
}

// FindAllMatching returns ALL rules that match the given subject (exact + patterns)
func (idx *RuleIndex) FindAllMatching(subject string) []*Rule {
    idx.mu.RLock()
    defer idx.mu.RUnlock()

    atomic.AddUint64(&idx.stats.lookups, 1)

    var allMatches []*Rule

    // First check exact matches (fastest)
    if exactRules := idx.exactMatches[subject]; exactRules != nil {
        allMatches = append(allMatches, exactRules...)
        atomic.AddUint64(&idx.stats.exactMatches, 1)
        
        idx.logger.Debug("found exact matches",
            "subject", subject,
            "exactMatches", len(exactRules))
    }

    // Then check pattern matches
    var patternMatches int
    for _, patternRule := range idx.patternRules {
        atomic.AddUint64(&idx.stats.patternChecks, 1)
        
        if patternRule.Matcher.Match(subject) {
            allMatches = append(allMatches, patternRule.Rule)
            patternMatches++
            
            idx.logger.Debug("pattern rule matched",
                "subject", subject,
                "pattern", patternRule.Rule.Topic)
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

// GetTopics returns all unique topics (both exact and patterns)
func (idx *RuleIndex) GetTopics() []string {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    
    // Calculate total capacity needed
    totalCapacity := len(idx.exactMatches) + len(idx.patternRules)
    topics := make([]string, 0, totalCapacity)
    
    // Add exact match topics
    for topic := range idx.exactMatches {
        topics = append(topics, topic)
    }
    
    // Add pattern topics
    for _, patternRule := range idx.patternRules {
        topics = append(topics, patternRule.Rule.Topic)
    }

    idx.logger.Debug("retrieved all topics",
        "exactTopics", len(idx.exactMatches),
        "patternTopics", len(idx.patternRules),
        "totalTopics", len(topics))

    return topics
}

// GetSubscriptionTopics returns topics that should be subscribed to in NATS
// This is the simple 1:1 mapping approach (no consolidation)
func (idx *RuleIndex) GetSubscriptionTopics() []string {
    return idx.GetTopics() // Simple: subscribe to exactly what's in rules
}

func (idx *RuleIndex) Clear() {
    idx.mu.Lock()
    defer idx.mu.Unlock()

    previousExactCount := len(idx.exactMatches)
    previousPatternCount := len(idx.patternRules)
    
    idx.exactMatches = make(map[string][]*Rule)
    idx.patternRules = make([]*PatternRule, 0)
    idx.stats.lastUpdated = time.Now()

    idx.logger.Info("index cleared",
        "previousExactRules", previousExactCount,
        "previousPatternRules", previousPatternCount,
        "timestamp", idx.stats.lastUpdated)
}

func (idx *RuleIndex) GetStats() IndexStats {
    idx.stats.mu.RLock()
    defer idx.stats.mu.RUnlock()

    stats := IndexStats{
        lookups:       atomic.LoadUint64(&idx.stats.lookups),
        matches:       atomic.LoadUint64(&idx.stats.matches),
        exactMatches:  atomic.LoadUint64(&idx.stats.exactMatches),
        patternChecks: atomic.LoadUint64(&idx.stats.patternChecks),
        lastUpdated:   idx.stats.lastUpdated,
    }

    idx.logger.Debug("index stats retrieved",
        "lookups", stats.lookups,
        "matches", stats.matches,
        "exactMatches", stats.exactMatches,
        "patternChecks", stats.patternChecks,
        "lastUpdated", stats.lastUpdated)

    return stats
}

// GetRuleCounts returns counts for monitoring/metrics
func (idx *RuleIndex) GetRuleCounts() (exactRules, patternRules int) {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    
    exactCount := 0
    for _, rules := range idx.exactMatches {
        exactCount += len(rules)
    }
    
    return exactCount, len(idx.patternRules)
}

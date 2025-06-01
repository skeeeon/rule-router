//file: internal/rule/index.go

package rule

import (
    "sync"
    "sync/atomic"
    "time"

    "rule-router/internal/logger"
)

type RuleIndex struct {
    exactMatches map[string][]*Rule
    mu           sync.RWMutex
    stats        IndexStats
    logger       *logger.Logger
}

type IndexStats struct {
    lookups     uint64
    matches     uint64
    lastUpdated time.Time
    mu          sync.RWMutex
}

func NewRuleIndex(log *logger.Logger) *RuleIndex {
    return &RuleIndex{
        exactMatches: make(map[string][]*Rule),
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

    idx.logger.Debug("adding rule to index",
        "topic", rule.Topic,
        "existingRules", len(idx.exactMatches[rule.Topic]))

    idx.exactMatches[rule.Topic] = append(idx.exactMatches[rule.Topic], rule)
    idx.stats.lastUpdated = time.Now()

    idx.logger.Info("rule added to index",
        "topic", rule.Topic,
        "totalRulesForTopic", len(idx.exactMatches[rule.Topic]),
        "totalTopics", len(idx.exactMatches))
}

func (idx *RuleIndex) Find(topic string) []*Rule {
    idx.mu.RLock()
    defer idx.mu.RUnlock()

    atomic.AddUint64(&idx.stats.lookups, 1)
    rules := idx.exactMatches[topic]

    idx.logger.Debug("looking up rules for topic",
        "topic", topic,
        "rulesFound", len(rules))

    if len(rules) > 0 {
        atomic.AddUint64(&idx.stats.matches, 1)
    }
    
    return rules
}

func (idx *RuleIndex) GetTopics() []string {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    
    topics := make([]string, 0, len(idx.exactMatches))
    for topic := range idx.exactMatches {
        topics = append(topics, topic)
    }

    idx.logger.Debug("retrieved topic list",
        "topicCount", len(topics))

    return topics
}

func (idx *RuleIndex) Clear() {
    idx.mu.Lock()
    defer idx.mu.Unlock()

    previousCount := len(idx.exactMatches)
    idx.exactMatches = make(map[string][]*Rule)
    idx.stats.lastUpdated = time.Now()

    idx.logger.Info("index cleared",
        "previousRuleCount", previousCount,
        "timestamp", idx.stats.lastUpdated)
}

func (idx *RuleIndex) GetStats() IndexStats {
    idx.stats.mu.RLock()
    defer idx.stats.mu.RUnlock()

    stats := IndexStats{
        lookups:     atomic.LoadUint64(&idx.stats.lookups),
        matches:     atomic.LoadUint64(&idx.stats.matches),
        lastUpdated: idx.stats.lastUpdated,
    }

    idx.logger.Debug("index stats retrieved",
        "lookups", stats.lookups,
        "matches", stats.matches,
        "lastUpdated", stats.lastUpdated)

    return stats
}

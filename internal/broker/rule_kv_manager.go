// file: internal/broker/rule_kv_manager.go

package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// RuleKVManager watches a NATS KV bucket for rule definitions and hot-reloads
// them into the Processor. It also dynamically creates and removes JetStream
// consumers and subscriptions as trigger subjects change.
type RuleKVManager struct {
	kvBucket            string
	autoProvision       bool
	processor           *rule.Processor
	broker              *NATSBroker
	rulesLoader         *rule.RulesLoader
	logger              *logger.Logger
	currentRules        map[string][]rule.Rule // KV key → parsed rules
	scheduleRebuildFunc func([]*rule.Rule)     // optional: called when schedule rules change
	mu                  sync.Mutex
	wg                  sync.WaitGroup
	ready               chan struct{}
	readyOnce           sync.Once
	watcher             jetstream.KeyWatcher
	watchOnce           sync.Once
	watchErr            error
}

// NewRuleKVManager creates a new rule KV manager.
func NewRuleKVManager(
	kvBucket string,
	autoProvision bool,
	processor *rule.Processor,
	broker *NATSBroker,
	rulesLoader *rule.RulesLoader,
	log *logger.Logger,
) *RuleKVManager {
	return &RuleKVManager{
		kvBucket:      kvBucket,
		autoProvision: autoProvision,
		processor:     processor,
		broker:        broker,
		rulesLoader:   rulesLoader,
		logger:        log.With("component", "rule-kv-manager"),
		currentRules:  make(map[string][]rule.Rule),
		ready:         make(chan struct{}),
	}
}

// Watch opens the KV bucket and starts watching for rule changes.
// Only runs once (via sync.Once). Returns immediately; the watcher runs in a goroutine.
func (m *RuleKVManager) Watch(ctx context.Context) error {
	m.watchOnce.Do(func() {
		js := m.broker.GetJetStream()

		store, err := js.KeyValue(ctx, m.kvBucket)
		if err != nil {
			if errors.Is(err, jetstream.ErrBucketNotFound) && m.autoProvision {
				store, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: m.kvBucket})
				if err != nil {
					m.watchErr = fmt.Errorf("failed to auto-create rules KV bucket %q: %w", m.kvBucket, err)
					return
				}
				m.logger.Info("auto-provisioned rules KV bucket", "bucket", m.kvBucket)
			} else {
				m.watchErr = fmt.Errorf("failed to open KV bucket %q: %w", m.kvBucket, err)
				return
			}
		}

		watcher, err := store.WatchAll(ctx)
		if err != nil {
			m.watchErr = fmt.Errorf("failed to create watcher for bucket %q: %w", m.kvBucket, err)
			return
		}

		m.mu.Lock()
		m.watcher = watcher
		m.mu.Unlock()

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.processWatchUpdates(ctx, watcher)
		}()

		m.logger.Info("rule KV watcher started", "bucket", m.kvBucket)
	})
	return m.watchErr
}

// WaitReady blocks until the initial KV sync completes (nil sentinel received)
// or the context is cancelled.
func (m *RuleKVManager) WaitReady(ctx context.Context) error {
	select {
	case <-m.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetScheduleRebuildFunc registers a callback that is invoked whenever the set of
// schedule-triggered rules changes. The SchedulerApp uses this to rebuild cron jobs
// without restarting the process.
func (m *RuleKVManager) SetScheduleRebuildFunc(f func([]*rule.Rule)) {
	m.mu.Lock()
	m.scheduleRebuildFunc = f
	m.mu.Unlock()
}

// Stop stops the watcher and waits for the goroutine to exit.
func (m *RuleKVManager) Stop() {
	m.mu.Lock()
	watcher := m.watcher
	m.mu.Unlock()

	if watcher != nil {
		if err := watcher.Stop(); err != nil {
			m.logger.Error("failed to stop rule KV watcher", "error", err)
		}
	}
	m.wg.Wait()
}

// processWatchUpdates processes updates from the KV watcher.
func (m *RuleKVManager) processWatchUpdates(ctx context.Context, watcher jetstream.KeyWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-watcher.Updates():
			if entry == nil {
				// Initial sync complete — all existing keys have been delivered.
				m.readyOnce.Do(func() { close(m.ready) })
				continue
			}

			switch entry.Operation() {
			case jetstream.KeyValuePut:
				m.handleRulePut(entry.Key(), entry.Value(), entry.Revision())
			case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
				m.handleRuleDelete(entry.Key())
			}
		}
	}
}

// handleRulePut processes a KV put event (rule created or updated).
func (m *RuleKVManager) handleRulePut(key string, value []byte, revision uint64) {
	// Parse and validate the YAML rule definition
	rules, err := m.rulesLoader.ParseAndValidateYAML(value, key)
	if err != nil {
		m.logger.Error("failed to parse rules from KV, keeping previous rules for this key",
			"key", key, "revision", revision, "error", err)
		return
	}

	// Refresh streams to pick up any newly created JetStream streams
	if err := m.broker.RefreshStreams(); err != nil {
		m.logger.Warn("failed to refresh stream list before validating rules",
			"key", key, "error", err)
	}

	// Validate that all rule subjects have corresponding streams
	resolver := m.broker.GetStreamResolver()
	if streamErrs := resolver.ValidateRulesHaveStreams(rules); len(streamErrs) > 0 {
		for _, e := range streamErrs {
			m.logger.Error("stream validation failed, rejecting all rules for this key",
				"key", key, "revision", revision, "error", e)
		}
		return
	}

	// Atomically update the rule set
	m.mu.Lock()

	previousSubjects := m.collectNATSTriggerSubjects(m.currentRules[key])
	m.currentRules[key] = rules
	scheduleRules := m.pushRulesToProcessor()

	newSubjects := m.collectNATSTriggerSubjects(rules)
	rebuildFunc := m.scheduleRebuildFunc

	m.mu.Unlock()

	// Notify scheduler to rebuild cron jobs if schedule rules changed
	if rebuildFunc != nil && len(scheduleRules) > 0 {
		rebuildFunc(scheduleRules)
	}

	// Start subscriptions for newly added NATS trigger subjects (outside lock)
	for subject := range newSubjects {
		if !previousSubjects[subject] {
			if err := m.broker.AddAndStartSubscription(subject); err != nil {
				if errors.Is(err, ErrNoStreamFound) {
					m.logger.Warn("rule references subject with no JetStream stream, skipping subscription",
						"key", key, "subject", subject,
						"hint", "create a stream covering this subject or remove the rule")
				} else {
					m.logger.Error("failed to start subscription for new subject",
						"key", key, "subject", subject, "error", err)
				}
			}
		}
	}

	m.logger.Info("KV rules updated",
		"key", key, "ruleCount", len(rules), "revision", revision)
}

// handleRuleDelete processes a KV delete event (rule removed).
func (m *RuleKVManager) handleRuleDelete(key string) {
	m.mu.Lock()

	oldRules, existed := m.currentRules[key]
	if !existed {
		m.mu.Unlock()
		return
	}

	oldSubjects := m.collectNATSTriggerSubjects(oldRules)
	oldHadScheduleRules := m.hasScheduleRules(oldRules)
	delete(m.currentRules, key)
	scheduleRules := m.pushRulesToProcessor()

	// Compute which subjects are still needed by other keys
	stillNeeded := make(map[string]bool)
	for _, rules := range m.currentRules {
		for subject := range m.collectNATSTriggerSubjects(rules) {
			stillNeeded[subject] = true
		}
	}
	rebuildFunc := m.scheduleRebuildFunc

	m.mu.Unlock()

	// Rebuild cron jobs only if the deleted key actually had schedule rules
	if rebuildFunc != nil && oldHadScheduleRules {
		rebuildFunc(scheduleRules)
	}

	// Remove subscriptions for subjects no longer needed (outside lock)
	for subject := range oldSubjects {
		if !stillNeeded[subject] {
			m.broker.RemoveSubscription(subject)
		}
	}

	m.logger.Info("KV rules deleted", "key", key)
}

// pushRulesToProcessor aggregates all current rules and atomically swaps them
// in the Processor. Must be called while holding m.mu.
// Returns the collected schedule rules so the caller can invoke the rebuild callback.
func (m *RuleKVManager) pushRulesToProcessor() []*rule.Rule {
	natsRules := make(map[string][]*rule.Rule)
	httpRules := make(map[string][]*rule.Rule)
	var scheduleRules []*rule.Rule

	for _, rules := range m.currentRules {
		for i := range rules {
			r := &rules[i]
			if r.Trigger.NATS != nil {
				natsRules[r.Trigger.NATS.Subject] = append(natsRules[r.Trigger.NATS.Subject], r)
			}
			if r.Trigger.HTTP != nil {
				httpRules[r.Trigger.HTTP.Path] = append(httpRules[r.Trigger.HTTP.Path], r)
			}
			if r.Trigger.Schedule != nil {
				scheduleRules = append(scheduleRules, r)
			}
		}
	}

	m.processor.ReplaceRules(natsRules)
	m.processor.ReplaceHTTPRules(httpRules)
	m.processor.ReplaceScheduleRules(scheduleRules)
	return scheduleRules
}

// hasScheduleRules returns true if any rule in the slice has a schedule trigger.
func (m *RuleKVManager) hasScheduleRules(rules []rule.Rule) bool {
	for _, r := range rules {
		if r.Trigger.Schedule != nil {
			return true
		}
	}
	return false
}

// collectNATSTriggerSubjects returns the set of trigger subjects for all rules with NATS triggers,
// regardless of action type (NATS or HTTP). Used for subscription management.
func (m *RuleKVManager) collectNATSTriggerSubjects(rules []rule.Rule) map[string]bool {
	subjects := make(map[string]bool)
	for _, r := range rules {
		if r.Trigger.NATS != nil {
			subjects[r.Trigger.NATS.Subject] = true
		}
	}
	return subjects
}

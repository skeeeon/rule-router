// file: internal/broker/rule_kv_manager.go

package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// KV watch re-establishment backoff bounds, shared by RuleKVManager and
// NATSBroker. Declared as vars (not consts) so tests can shorten them.
var (
	kvWatchRetryBaseDelay = 500 * time.Millisecond
	kvWatchRetryMaxDelay  = 30 * time.Second
)

// nextKVWatchBackoff doubles the current delay, capped at kvWatchRetryMaxDelay.
func nextKVWatchBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > kvWatchRetryMaxDelay {
		return kvWatchRetryMaxDelay
	}
	return d
}

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
	coreRebuildFunc     func([]*rule.Rule)     // optional: called when core-transport (reply/mode:core) rules change
	mu                  sync.Mutex
	wg                  sync.WaitGroup
	ready               chan struct{}
	readyOnce           sync.Once
	watcher             jetstream.KeyWatcher
	cancel              context.CancelFunc // cancels the watch goroutine's context
	// newWatcher (re)creates the bucket watcher. Set once in Watch (closing over
	// the KV store) and reused by the self-heal loop to re-establish the watcher
	// after an unexpected close. Injectable so tests can supply a fake.
	newWatcher func(context.Context) (jetstream.KeyWatcher, error)
	watchOnce  sync.Once
	watchErr   error
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

		// Derive a cancellable context so Stop() can unblock the watch
		// goroutine even when the parent context is still alive (e.g. a
		// targeted shutdown). Without this, closing the watcher's Updates()
		// channel would be the only escape, and Stop() could deadlock.
		watchCtx, cancel := context.WithCancel(ctx)

		// Factory used for both the initial watch and self-healing re-creation
		// after an unexpected close. WatchAll re-delivers the current value (or
		// delete marker) for every key on each (re)subscribe, so a recreate
		// performs a full state re-sync rather than risking missed updates.
		m.newWatcher = func(c context.Context) (jetstream.KeyWatcher, error) {
			return store.WatchAll(c)
		}

		watcher, err := m.newWatcher(watchCtx)
		if err != nil {
			cancel()
			m.watchErr = fmt.Errorf("failed to create watcher for bucket %q: %w", m.kvBucket, err)
			return
		}

		m.mu.Lock()
		m.watcher = watcher
		m.cancel = cancel
		m.mu.Unlock()

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runWatch(watchCtx, watcher)
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

// SetCoreRebuildFunc registers a callback invoked whenever the set of
// core-transport NATS-triggered rules (reply:true or mode:core) changes. The
// RouterApp uses this to rebuild its core-NATS responder subscriptions without
// restarting the process.
func (m *RuleKVManager) SetCoreRebuildFunc(f func([]*rule.Rule)) {
	m.mu.Lock()
	m.coreRebuildFunc = f
	m.mu.Unlock()
}

// Stop stops the watcher and waits for the goroutine to exit.
func (m *RuleKVManager) Stop() {
	m.mu.Lock()
	watcher := m.watcher
	cancel := m.cancel
	m.mu.Unlock()

	// Cancel first so the watch goroutine exits via its ctx.Done() case even
	// if Stop() races with the watcher's channel-close handler.
	if cancel != nil {
		cancel()
	}
	if watcher != nil {
		if err := watcher.Stop(); err != nil {
			m.logger.Error("failed to stop rule KV watcher", "error", err)
		}
	}
	m.wg.Wait()
}

// runWatch consumes KV updates and self-heals across unexpected watcher
// closures. If the Updates() channel closes while the context is still alive
// (connection loss, server-side consumer deletion), it re-creates the watcher
// with capped exponential backoff and resumes. It returns only on context
// cancellation (shutdown / Stop).
//
// Re-sync correctness: WatchAll re-delivers the latest value or delete marker
// for every key on each subscribe, so handleRulePut/handleRuleDelete rebuild
// the full rule set. Puts are idempotent (overwrite by key) and deletes arrive
// as markers, so the rule set is not duplicated or silently left stale. The one
// gap is a key whose delete marker was already compacted away during the
// outage; such a stale entry would persist until the next explicit change.
func (m *RuleKVManager) runWatch(ctx context.Context, watcher jetstream.KeyWatcher) {
	backoff := kvWatchRetryBaseDelay
	for {
		// Consume until the channel closes.
		if m.consumeUpdates(ctx, watcher) {
			return // clean shutdown
		}
		if ctx.Err() != nil {
			return
		}

		// Unexpected close: re-establish the watcher with backoff.
		m.logger.Error("rule KV watcher channel closed unexpectedly; re-establishing",
			"bucket", m.kvBucket)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			newWatcher, err := m.newWatcher(ctx)
			if err != nil {
				m.logger.Error("failed to re-establish rule KV watcher, will retry",
					"bucket", m.kvBucket, "retryIn", backoff, "error", err)
				backoff = nextKVWatchBackoff(backoff)
				continue
			}
			// If we were cancelled while creating the watcher, don't leak it.
			if ctx.Err() != nil {
				_ = newWatcher.Stop()
				return
			}

			watcher = newWatcher
			m.mu.Lock()
			m.watcher = watcher
			m.mu.Unlock()
			backoff = kvWatchRetryBaseDelay
			m.logger.Info("rule KV watcher re-established", "bucket", m.kvBucket)
			break
		}
	}
}

// consumeUpdates reads from the watcher until its Updates() channel closes.
// Returns true if the loop should terminate permanently (context cancelled),
// false if the channel closed unexpectedly and the watcher should be
// re-established by the caller.
func (m *RuleKVManager) consumeUpdates(ctx context.Context, watcher jetstream.KeyWatcher) bool {
	for {
		select {
		case <-ctx.Done():
			return true
		case entry, ok := <-watcher.Updates():
			if !ok {
				// Updates() channel closed. A closed channel yields (nil, false)
				// forever, so we MUST stop reading it here — otherwise this loop
				// spins at 100% CPU. Clean shutdown if the context is gone;
				// otherwise signal the caller to re-establish.
				if ctx.Err() != nil {
					m.logger.Debug("rule KV watcher stopped", "bucket", m.kvBucket)
					return true
				}
				return false
			}
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
	prevHadCore := m.hasCoreRules(m.currentRules[key])
	m.currentRules[key] = rules
	scheduleRules, coreRules := m.pushRulesToProcessor()

	newSubjects := m.collectNATSTriggerSubjects(rules)
	rebuildFunc := m.scheduleRebuildFunc
	coreRebuildFunc := m.coreRebuildFunc

	m.mu.Unlock()

	// Notify scheduler to rebuild cron jobs if schedule rules changed
	if rebuildFunc != nil && len(scheduleRules) > 0 {
		rebuildFunc(scheduleRules)
	}

	// Rebuild core subscriptions if this key added or removed core-transport rules
	if coreRebuildFunc != nil && (prevHadCore || m.hasCoreRules(rules)) {
		coreRebuildFunc(coreRules)
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
	oldHadCoreRules := m.hasCoreRules(oldRules)
	delete(m.currentRules, key)
	scheduleRules, coreRules := m.pushRulesToProcessor()

	// Compute which subjects are still needed by other keys
	stillNeeded := make(map[string]bool)
	for _, rules := range m.currentRules {
		for subject := range m.collectNATSTriggerSubjects(rules) {
			stillNeeded[subject] = true
		}
	}
	rebuildFunc := m.scheduleRebuildFunc
	coreRebuildFunc := m.coreRebuildFunc

	m.mu.Unlock()

	// Rebuild cron jobs only if the deleted key actually had schedule rules
	if rebuildFunc != nil && oldHadScheduleRules {
		rebuildFunc(scheduleRules)
	}

	// Rebuild core subscriptions only if the deleted key had core-transport rules
	if coreRebuildFunc != nil && oldHadCoreRules {
		coreRebuildFunc(coreRules)
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
// Returns the collected schedule rules and core-transport rules so the caller
// can invoke the corresponding rebuild callbacks.
func (m *RuleKVManager) pushRulesToProcessor() (scheduleRules, coreRules []*rule.Rule) {
	natsRules := make(map[string][]*rule.Rule)
	httpRules := &rule.HTTPKVRuleSet{
		Exact:    make(map[string][]*rule.Rule),
		Patterns: nil,
	}

	for _, rules := range m.currentRules {
		for i := range rules {
			r := &rules[i]
			if r.Trigger.NATS != nil {
				natsRules[r.Trigger.NATS.Subject] = append(natsRules[r.Trigger.NATS.Subject], r)
				if r.Trigger.NATS.IsCore() {
					coreRules = append(coreRules, r)
				}
			}
			if r.Trigger.HTTP != nil {
				path := r.Trigger.HTTP.Path
				if rule.PathContainsWildcards(path) {
					matcher, err := rule.NewPathMatcher(path)
					if err != nil {
						m.logger.Error("failed to compile KV HTTP path pattern, skipping rule",
							"path", path,
							"error", err)
					} else {
						httpRules.Patterns = append(httpRules.Patterns, &rule.HTTPPatternRule{
							Rule:    r,
							Matcher: matcher,
						})
					}
				} else {
					httpRules.Exact[path] = append(httpRules.Exact[path], r)
				}
			}
			if r.Trigger.Schedule != nil {
				scheduleRules = append(scheduleRules, r)
			}
		}
	}

	m.processor.ReplaceKVRuleSet(&rule.KVRuleSet{
		NATS:     natsRules,
		HTTP:     httpRules,
		Schedule: scheduleRules,
	})
	return scheduleRules, coreRules
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
// regardless of action type (NATS or HTTP). Used for JetStream subscription management.
// Core-transport subjects (reply:true or mode:core) are excluded — they are
// served by the core-NATS responder, not JetStream.
func (m *RuleKVManager) collectNATSTriggerSubjects(rules []rule.Rule) map[string]bool {
	subjects := make(map[string]bool)
	for _, r := range rules {
		if r.Trigger.NATS != nil && !r.Trigger.NATS.IsCore() {
			subjects[r.Trigger.NATS.Subject] = true
		}
	}
	return subjects
}

// hasCoreRules returns true if any rule in the slice has a core-transport NATS
// trigger (reply:true or mode:core).
func (m *RuleKVManager) hasCoreRules(rules []rule.Rule) bool {
	for _, r := range rules {
		if r.Trigger.NATS != nil && r.Trigger.NATS.IsCore() {
			return true
		}
	}
	return false
}

// GetCoreRules returns all currently-loaded rules with a core-transport NATS
// trigger (reply:true or mode:core). Used by RouterApp to deterministically
// bootstrap responder subscriptions after initial KV sync, independent of the
// per-put rebuild callback's timing.
func (m *RuleKVManager) GetCoreRules() []*rule.Rule {
	m.mu.Lock()
	defer m.mu.Unlock()

	var coreRules []*rule.Rule
	for _, rules := range m.currentRules {
		for i := range rules {
			r := &rules[i]
			if r.Trigger.NATS != nil && r.Trigger.NATS.IsCore() {
				coreRules = append(coreRules, r)
			}
		}
	}
	return coreRules
}

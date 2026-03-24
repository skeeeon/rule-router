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

// OutboundSubscriber is implemented by components that manage outbound subscriptions
// for rules with NATS triggers and HTTP actions (e.g., the http-gateway's OutboundClient).
type OutboundSubscriber interface {
	AddAndStartSubscription(ctx context.Context, streamName, consumerName, subject string, workers int) error
	RemoveSubscription(subject string)
}

// RuleKVManager watches a NATS KV bucket for rule definitions and hot-reloads
// them into the Processor. It also dynamically creates and removes JetStream
// consumers and subscriptions as trigger subjects change.
type RuleKVManager struct {
	kvBucket           string
	autoProvision      bool
	processor          *rule.Processor
	broker             *NATSBroker
	rulesLoader        *rule.RulesLoader
	logger             *logger.Logger
	currentRules       map[string][]rule.Rule // KV key → parsed rules
	outboundSubscriber OutboundSubscriber
	mu                 sync.Mutex
	wg                 sync.WaitGroup
	ready              chan struct{}
	readyOnce          sync.Once
	watcher            jetstream.KeyWatcher
	watchOnce          sync.Once
	watchErr           error
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

		m.watcher = watcher
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

// Stop stops the watcher and waits for the goroutine to exit.
func (m *RuleKVManager) Stop() {
	if m.watcher != nil {
		if err := m.watcher.Stop(); err != nil {
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

	previousNATSSubjects := m.collectNATSSubjects(m.currentRules[key])
	previousHTTPSubjects := m.collectHTTPActionSubjects(m.currentRules[key])
	m.currentRules[key] = rules
	m.pushRulesToProcessor()

	newNATSSubjects := m.collectNATSSubjects(rules)
	newHTTPSubjects := m.collectHTTPActionSubjects(rules)
	outbound := m.outboundSubscriber

	m.mu.Unlock()

	// Start inbound subscriptions for newly added NATS trigger subjects (outside lock)
	for subject := range newNATSSubjects {
		if !previousNATSSubjects[subject] {
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

	// Start outbound subscriptions for NATS trigger + HTTP action rules
	for subject := range newHTTPSubjects {
		if !previousHTTPSubjects[subject] {
			m.addOutboundSubscription(outbound, key, subject)
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

	oldNATSSubjects := m.collectNATSSubjects(oldRules)
	oldHTTPSubjects := m.collectHTTPActionSubjects(oldRules)
	delete(m.currentRules, key)
	m.pushRulesToProcessor()

	// Compute which subjects are still needed by other keys
	stillNeededNATS := make(map[string]bool)
	stillNeededHTTP := make(map[string]bool)
	for _, rules := range m.currentRules {
		for subject := range m.collectNATSSubjects(rules) {
			stillNeededNATS[subject] = true
		}
		for subject := range m.collectHTTPActionSubjects(rules) {
			stillNeededHTTP[subject] = true
		}
	}
	outbound := m.outboundSubscriber

	m.mu.Unlock()

	// Remove inbound subscriptions for subjects no longer needed (outside lock)
	for subject := range oldNATSSubjects {
		if !stillNeededNATS[subject] {
			m.broker.RemoveSubscription(subject)
		}
	}

	// Remove outbound subscriptions for HTTP action subjects no longer needed
	for subject := range oldHTTPSubjects {
		if !stillNeededHTTP[subject] {
			if outbound != nil {
				outbound.RemoveSubscription(subject)
			}
		}
	}

	m.logger.Info("KV rules deleted", "key", key)
}

// pushRulesToProcessor aggregates all current rules and atomically swaps them
// in the Processor. Must be called while holding m.mu.
func (m *RuleKVManager) pushRulesToProcessor() {
	natsRules := make(map[string][]*rule.Rule)
	httpRules := make(map[string][]*rule.Rule)

	for _, rules := range m.currentRules {
		for i := range rules {
			r := &rules[i]
			if r.Trigger.NATS != nil {
				natsRules[r.Trigger.NATS.Subject] = append(natsRules[r.Trigger.NATS.Subject], r)
			}
			if r.Trigger.HTTP != nil {
				httpRules[r.Trigger.HTTP.Path] = append(httpRules[r.Trigger.HTTP.Path], r)
			}
		}
	}

	m.processor.ReplaceRules(natsRules)
	m.processor.ReplaceHTTPRules(httpRules)
}

// SetOutboundSubscriber registers an outbound subscriber (e.g., http-gateway's OutboundClient)
// for managing NATS trigger + HTTP action subscriptions. It retroactively subscribes to
// any HTTP action subjects already loaded from KV.
func (m *RuleKVManager) SetOutboundSubscriber(sub OutboundSubscriber) {
	m.mu.Lock()
	m.outboundSubscriber = sub

	// Collect all HTTP action subjects from already-loaded rules
	var httpSubjects []string
	for _, rules := range m.currentRules {
		for subject := range m.collectHTTPActionSubjects(rules) {
			httpSubjects = append(httpSubjects, subject)
		}
	}
	m.mu.Unlock()

	// Retroactively subscribe to already-loaded HTTP action subjects
	for _, subject := range httpSubjects {
		m.addOutboundSubscription(sub, "", subject)
	}
}

// addOutboundSubscription creates a consumer and starts an outbound subscription.
func (m *RuleKVManager) addOutboundSubscription(sub OutboundSubscriber, key, subject string) {
	if sub == nil {
		m.logger.Debug("no outbound subscriber set, skipping HTTP action subscription",
			"subject", subject)
		return
	}

	streamName, consumerName, err := m.broker.CreateOutboundConsumer(subject)
	if err != nil {
		m.logger.Error("failed to create outbound consumer",
			"key", key, "subject", subject, "error", err)
		return
	}

	workers := m.broker.config.NATS.Consumers.WorkerCount
	if err := sub.AddAndStartSubscription(m.broker.ctx, streamName, consumerName, subject, workers); err != nil {
		m.logger.Error("failed to start outbound subscription",
			"key", key, "subject", subject, "error", err)
	}
}

// collectNATSSubjects returns the set of trigger subjects for rules with NATS actions
// (NATS trigger → NATS action). Used for inbound subscription management.
func (m *RuleKVManager) collectNATSSubjects(rules []rule.Rule) map[string]bool {
	subjects := make(map[string]bool)
	for _, r := range rules {
		if r.Trigger.NATS != nil && r.Action.NATS != nil {
			subjects[r.Trigger.NATS.Subject] = true
		}
	}
	return subjects
}

// collectHTTPActionSubjects returns the set of trigger subjects for rules with HTTP actions
// (NATS trigger → HTTP action). Used for outbound subscription management.
func (m *RuleKVManager) collectHTTPActionSubjects(rules []rule.Rule) map[string]bool {
	subjects := make(map[string]bool)
	for _, r := range rules {
		if r.Trigger.NATS != nil && r.Action.HTTP != nil {
			subjects[r.Trigger.NATS.Subject] = true
		}
	}
	return subjects
}

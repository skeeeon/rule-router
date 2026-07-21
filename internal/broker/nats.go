// file: internal/broker/nats.go

package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout and retry constants for NATS broker operations
const (
	// kvOperationTimeout is the maximum time for KV store operations
	kvOperationTimeout = 10 * time.Second

	// reconnectJitterMin is the minimum jitter for NATS reconnection attempts
	reconnectJitterMin = 100 * time.Millisecond

	// reconnectJitterMax is the maximum jitter for NATS reconnection attempts
	reconnectJitterMax = 1 * time.Second

	// watcherShutdownTimeout is the maximum time to wait for KV watchers to stop
	watcherShutdownTimeout = 5 * time.Second

	// drainTimeout bounds how long the NATS connection will wait for in-flight
	// publishes to drain before forcibly closing on shutdown. Without this,
	// Drain() can block indefinitely when the server is unreachable.
	drainTimeout = 10 * time.Second

	// initialConnectMaxAttempts / initialConnectRetryWait bound the retry loop
	// around the *initial* NATS connect, so a server that is slow to come up
	// (e.g. started alongside rule-router under docker-compose) doesn't crash the
	// process on the first failure. Deliberately fixed rather than derived from
	// Connection.MaxReconnects (default -1 = infinite, which would hang startup)
	// or Connection.ReconnectWait (default 50ms, too short to bridge startup).
	// ~18s of grace, then fail fast so a genuine misconfiguration still surfaces.
	initialConnectMaxAttempts = 10
	initialConnectRetryWait   = 2 * time.Second
)

// NATSBroker connects to external NATS JetStream servers with KV support and local caching
type NATSBroker struct {
	logger  *logger.Logger
	metrics *metrics.Metrics
	config  *config.Config

	// NATS connection and JetStream interface
	natsConn  *nats.Conn
	jetStream jetstream.JetStream
	kvStores  map[string]jetstream.KeyValue

	// Local KV cache for performance optimization
	localKVCache *rule.LocalKVCache
	kvWatchersMu sync.Mutex // protects kvWatchers across reconnect/shutdown
	kvWatchers   []*kvWatchHandle
	kvWatcherWg  sync.WaitGroup // Tracks KV watcher goroutines for graceful shutdown

	// Stream resolver for JetStream stream discovery
	streamResolver *StreamResolver

	// Subscription manager for message processing
	subscriptionMgr *SubscriptionManager

	// HTTP action executor shared by the JetStream and core-NATS transports.
	// Set by the gateway feature after broker startup, so reads go through
	// GetHTTPExecutor which takes the mutex (traffic may already be flowing).
	httpExecMu   sync.RWMutex
	httpExecutor HTTPActionExecutor

	// Consumer management - track created consumers
	consumersMu sync.RWMutex
	consumers   map[string]string // subject -> consumer name

	// Context for managing watchers and long-running operations
	ctx    context.Context
	cancel context.CancelFunc
}

// NewNATSBroker creates a new NATS broker that connects to external NATS servers
func NewNATSBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*NATSBroker, error) {
	// Create cancellable context for broker lifecycle management
	ctx, cancel := context.WithCancel(context.Background())

	broker := &NATSBroker{
		logger:       log.With("component", "broker"),
		metrics:      metrics,
		config:       cfg,
		kvStores:     make(map[string]jetstream.KeyValue),
		localKVCache: rule.NewLocalKVCache(log),
		kvWatchers:   make([]*kvWatchHandle, 0),
		consumers:    make(map[string]string),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Attach metrics to the KV cache so hit/miss/size are instrumented
	// (no-op in builds/tests where metrics is nil).
	broker.localKVCache.SetMetrics(metrics)

	// Initialize NATS connection
	if err := broker.initializeNATSConnection(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize NATS connection: %w", err)
	}

	// Initialize stream resolver for JetStream stream discovery
	broker.streamResolver = NewStreamResolver(broker.jetStream, log)

	// Discover streams immediately
	if err := broker.streamResolver.Discover(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to discover JetStream streams: %w", err)
	}

	// Initialize KV stores if enabled
	if cfg.KV.Enabled {
		if err := broker.initializeKVStores(ctx); err != nil {
			cancel()
			return nil, fmt.Errorf("failed to initialize KV stores: %w", err)
		}
	}

	return broker, nil
}

// InitializeSubscriptionManager creates the subscription manager
// Must be called after rule processor is initialized
func (b *NATSBroker) InitializeSubscriptionManager(processor *rule.Processor) {
	b.subscriptionMgr = NewSubscriptionManager(
		b.natsConn,
		b.jetStream,
		processor,
		b.logger,
		b.metrics,
		&b.config.NATS.Consumers,
		&b.config.NATS.Publish,
	)
	b.logger.Info("subscription manager initialized",
		"workerCount", b.config.NATS.Consumers.WorkerCount,
		"fetchBatchSize", b.config.NATS.Consumers.FetchBatchSize,
		"fetchTimeout", b.config.NATS.Consumers.FetchTimeout,
		"publishMode", b.config.NATS.Publish.Mode)
}

// CreateConsumerForSubject creates a durable consumer for the given subject
func (b *NATSBroker) CreateConsumerForSubject(subject string) error {
	b.logger.Debug("creating consumer for subject", "subject", subject)

	// Check if consumer already exists for this subject
	b.consumersMu.RLock()
	existingConsumer, exists := b.consumers[subject]
	b.consumersMu.RUnlock()
	if exists {
		b.logger.Debug("consumer already exists for subject",
			"subject", subject,
			"consumer", existingConsumer)
		return nil
	}

	// Find which stream handles this subject
	streamName, err := b.streamResolver.FindStreamForSubject(subject)
	if err != nil {
		return fmt.Errorf("cannot create consumer for subject '%s': %w", subject, err)
	}

	// Generate a valid consumer name
	consumerName := b.generateConsumerName(subject)

	// Map deliver policy from config string to JetStream constant
	deliverPolicy, err := b.parseDeliverPolicy(b.config.NATS.Consumers.DeliverPolicy)
	if err != nil {
		return fmt.Errorf("invalid deliver policy: %w", err)
	}

	// Map replay policy from config string to JetStream constant
	replayPolicy, err := b.parseReplayPolicy(b.config.NATS.Consumers.ReplayPolicy)
	if err != nil {
		return fmt.Errorf("invalid replay policy: %w", err)
	}

	// Create consumer configuration
	consumerConfig := jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       b.config.NATS.Consumers.AckWaitTimeout,
		MaxDeliver:    b.config.NATS.Consumers.MaxDeliver,
		MaxAckPending: b.config.NATS.Consumers.MaxAckPending,
		DeliverPolicy: deliverPolicy,
		ReplayPolicy:  replayPolicy,
	}

	// Use CreateOrUpdateConsumer for idempotent behavior
	ctx, cancel := context.WithTimeout(b.ctx, kvOperationTimeout)
	defer cancel()

	consumer, err := b.jetStream.CreateOrUpdateConsumer(ctx, streamName, consumerConfig)
	if err != nil {
		return fmt.Errorf("failed to create consumer '%s' on stream '%s' for subject '%s': %w",
			consumerName, streamName, subject, err)
	}

	// Track the consumer
	b.consumersMu.Lock()
	b.consumers[subject] = consumerName
	b.consumersMu.Unlock()

	// Get consumer info for logging
	info, infoErr := consumer.Info(ctx)
	if infoErr != nil {
		b.logger.Warn("failed to get consumer info after creation",
			"consumer", consumerName,
			"error", infoErr)
	} else if info != nil {
		b.logger.Info("created/updated durable consumer",
			"stream", streamName,
			"consumer", consumerName,
			"subject", subject,
			"pending", info.NumPending,
			"ackWait", info.Config.AckWait,
			"maxDeliver", info.Config.MaxDeliver,
			"maxAckPending", info.Config.MaxAckPending,
			"deliverPolicy", b.config.NATS.Consumers.DeliverPolicy,
			"replayPolicy", b.config.NATS.Consumers.ReplayPolicy)
	}

	return nil
}

// parseDeliverPolicy converts config string to JetStream DeliverPolicy constant
func (b *NATSBroker) parseDeliverPolicy(policy string) (jetstream.DeliverPolicy, error) {
	switch policy {
	case "all":
		return jetstream.DeliverAllPolicy, nil
	case "new":
		return jetstream.DeliverNewPolicy, nil
	case "last":
		return jetstream.DeliverLastPolicy, nil
	case "by_start_time":
		return jetstream.DeliverByStartTimePolicy, nil
	case "by_start_sequence":
		return jetstream.DeliverByStartSequencePolicy, nil
	default:
		return jetstream.DeliverAllPolicy, fmt.Errorf("unknown deliver policy: %s", policy)
	}
}

// parseReplayPolicy converts config string to JetStream ReplayPolicy constant
func (b *NATSBroker) parseReplayPolicy(policy string) (jetstream.ReplayPolicy, error) {
	switch policy {
	case "instant":
		return jetstream.ReplayInstantPolicy, nil
	case "original":
		return jetstream.ReplayOriginalPolicy, nil
	default:
		return jetstream.ReplayInstantPolicy, fmt.Errorf("unknown replay policy: %s", policy)
	}
}

// AddSubscription creates a pull subscription for a subject.
// It uses the broker's internal context for cancellation.
func (b *NATSBroker) AddSubscription(subject string) error {
	if b.subscriptionMgr == nil {
		return fmt.Errorf("subscription manager not initialized")
	}

	// Get consumer name
	b.consumersMu.RLock()
	consumerName, exists := b.consumers[subject]
	b.consumersMu.RUnlock()
	if !exists {
		return fmt.Errorf("consumer not created for subject '%s'", subject)
	}

	// Get stream name
	streamName, err := b.streamResolver.FindStreamForSubject(subject)
	if err != nil {
		return fmt.Errorf("cannot find stream for subject '%s': %w", subject, err)
	}

	// Add subscription with configured worker count, using broker's context
	workers := b.config.NATS.Consumers.WorkerCount
	if err := b.subscriptionMgr.AddSubscription(b.ctx, streamName, consumerName, subject, workers); err != nil {
		return fmt.Errorf("failed to add subscription for '%s': %w", subject, err)
	}

	return nil
}

// generateConsumerName creates a valid NATS consumer name from a subject
func (b *NATSBroker) generateConsumerName(subject string) string {
	sanitized := subject
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	sanitized = strings.ReplaceAll(sanitized, "*", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, ">", "multi-wildcard")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")

	// Use the configured prefix to avoid collisions
	return fmt.Sprintf("%s-%s", b.config.NATS.Consumers.ConsumerPrefix, sanitized)
}

// ValidateSubjects checks if all subjects can be mapped to streams
func (b *NATSBroker) ValidateSubjects(subjects []string) error {
	return b.streamResolver.ValidateSubjects(subjects)
}

// GetStreamResolver returns the stream resolver
func (b *NATSBroker) GetStreamResolver() *StreamResolver {
	return b.streamResolver
}

// FindStreamForSubject finds the stream name that handles the given subject.
// This is a convenience method that wraps GetStreamResolver().FindStreamForSubject().
func (b *NATSBroker) FindStreamForSubject(subject string) (string, error) {
	return b.streamResolver.FindStreamForSubject(subject)
}

// GetSubscriptionManager returns the subscription manager
func (b *NATSBroker) GetSubscriptionManager() *SubscriptionManager {
	return b.subscriptionMgr
}

// SetHTTPExecutor registers the executor for HTTP actions on NATS-triggered
// rules. It serves both the JetStream SubscriptionManager and the core-NATS
// Responder. Called by the gateway feature when enabled.
func (b *NATSBroker) SetHTTPExecutor(exec HTTPActionExecutor) {
	b.httpExecMu.Lock()
	b.httpExecutor = exec
	b.httpExecMu.Unlock()
	if b.subscriptionMgr != nil {
		b.subscriptionMgr.SetHTTPExecutor(exec)
	}
}

// GetHTTPExecutor returns the registered HTTP action executor, or nil when the
// gateway feature is disabled.
func (b *NATSBroker) GetHTTPExecutor() HTTPActionExecutor {
	b.httpExecMu.RLock()
	defer b.httpExecMu.RUnlock()
	return b.httpExecutor
}

// InitializeKVCache populates the local cache and subscribes to changes using Watch API
func (b *NATSBroker) InitializeKVCache() error {
	if !b.config.KV.Enabled {
		b.logger.Info("KV not enabled, skipping cache initialization")
		return nil
	}

	if !b.config.KV.LocalCache.Enabled {
		b.logger.Info("local KV cache disabled in configuration")
		b.localKVCache.SetEnabled(false)
		return nil
	}

	b.logger.Info("initializing local KV cache", "buckets", b.config.KV.BucketNames())

	// Subscribe to KV changes using the new Watch API
	if err := b.subscribeToKVChanges(); err != nil {
		b.logger.Error("failed to subscribe to KV changes", "error", err)
		return nil
	}

	b.logger.Info("local KV cache initialized successfully",
		"cacheEnabled", b.localKVCache.IsEnabled(),
		"stats", b.localKVCache.GetStats())

	return nil
}

// subscribeToKVChanges subscribes to KV change streams using the new Watch API
func (b *NATSBroker) subscribeToKVChanges() error {
	total := len(b.config.KV.Buckets)
	b.logger.Info("subscribing to KV changes using Watch API", "buckets", total)

	var failedBuckets []string
	for _, bucket := range b.config.KV.Buckets {
		if err := b.watchKVBucket(bucket.Name, bucket.KeyFilter); err != nil {
			b.logger.Error("failed to watch KV bucket", "bucket", bucket.Name, "error", err)
			failedBuckets = append(failedBuckets, bucket.Name)
		}
	}

	if len(failedBuckets) > 0 {
		b.logger.Warn("KV watch subscriptions partially established",
			"watching", len(b.kvWatchers),
			"failed", len(failedBuckets),
			"total", total,
			"failedBuckets", failedBuckets)
	} else {
		b.logger.Info("KV watch subscriptions established", "watchers", len(b.kvWatchers))
	}
	return nil
}

// kvWatchHandle owns a single bucket watcher and the data needed to re-create
// it. The owning goroutine (runKVWatch) is the sole creator of watchers: it
// swaps in a fresh one when self-healing after a close. External callers
// (reconnect restart, shutdown) only ever Stop the current watcher — which the
// goroutine then either rebuilds (if b.ctx is alive) or exits on (if cancelled).
// They read the current watcher under the handle's lock.
type kvWatchHandle struct {
	bucket    string
	keyFilter string
	mu        sync.Mutex
	watcher   jetstream.KeyWatcher
}

func (h *kvWatchHandle) current() jetstream.KeyWatcher {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.watcher
}

func (h *kvWatchHandle) set(w jetstream.KeyWatcher) {
	h.mu.Lock()
	h.watcher = w
	h.mu.Unlock()
}

// watchKVBucket creates a watcher for a specific KV bucket using the Watch API.
// The keyFilter controls which keys are watched (e.g. ">" for all, "bldg-a.>" for a subset).
func (b *NATSBroker) watchKVBucket(bucketName string, keyFilter string) error {
	store, exists := b.kvStores[bucketName]
	if !exists {
		return fmt.Errorf("bucket not found: %s", bucketName)
	}

	// Watch keys matching the filter pattern
	// Context from broker ensures watcher stops when broker closes
	watcher, err := store.Watch(b.ctx, keyFilter)
	if err != nil {
		return fmt.Errorf("failed to create watcher for bucket %s (filter %q): %w", bucketName, keyFilter, err)
	}

	h := &kvWatchHandle{bucket: bucketName, keyFilter: keyFilter, watcher: watcher}

	b.kvWatchersMu.Lock()
	b.kvWatchers = append(b.kvWatchers, h)
	b.kvWatchersMu.Unlock()

	// Start goroutine to process watch updates with WaitGroup tracking
	b.kvWatcherWg.Add(1)
	go func() {
		defer b.kvWatcherWg.Done()
		b.runKVWatch(h)
	}()

	b.logger.Info("created KV watcher",
		"bucket", bucketName,
		"keyFilter", keyFilter)

	return nil
}

// restartKVWatchers forces every KV bucket watcher to re-establish after a
// reconnect. Called from the NATS ReconnectHandler. A reconnect can leave a
// watcher's server-side consumer stale without closing its Updates() channel,
// so we Stop each watcher to close the channel; the owning runKVWatch goroutine
// then rebuilds it (b.ctx is still alive). Safe no-op when KV is disabled or no
// watchers were started yet.
func (b *NATSBroker) restartKVWatchers() {
	if !b.config.KV.Enabled || !b.config.KV.LocalCache.Enabled {
		return
	}
	// Bail out if the broker is shutting down — Close() has already cancelled
	// the context and is stopping the watchers itself.
	if b.ctx.Err() != nil {
		return
	}

	b.kvWatchersMu.Lock()
	handles := append([]*kvWatchHandle(nil), b.kvWatchers...)
	b.kvWatchersMu.Unlock()

	if len(handles) == 0 {
		return
	}

	b.logger.Info("re-establishing KV watchers after reconnect", "count", len(handles))

	for _, h := range handles {
		// Stopping closes the watcher's Updates() channel; runKVWatch detects
		// the close and re-creates the watcher in place.
		if err := h.current().Stop(); err != nil {
			b.logger.Warn("failed to stop stale KV watcher during reconnect", "bucket", h.bucket, "error", err)
		}
	}
}

// runKVWatch consumes updates for a bucket and self-heals across watcher
// closures. It re-creates the watcher with capped exponential backoff whenever
// the channel closes while b.ctx is still alive — whether from a reconnect
// (forced by restartKVWatchers) or a spontaneous drop (server-side consumer
// deletion). It returns only when b.ctx is cancelled (shutdown). The fresh
// watcher re-delivers the current value/delete marker for every key, so the
// local cache re-syncs.
func (b *NATSBroker) runKVWatch(h *kvWatchHandle) {
	b.logger.Debug("starting KV watch processor", "bucket", h.bucket)

	backoff := kvWatchRetryBaseDelay
	for {
		if b.consumeKVWatch(h.bucket, h.current()) {
			return // clean shutdown (b.ctx cancelled)
		}

		// Channel closed while connected: re-establish with backoff.
		b.logger.Error("KV watcher channel closed; re-establishing", "bucket", h.bucket)
		store, exists := b.kvStores[h.bucket]
		if !exists {
			b.logger.Error("KV store missing; cannot re-establish watcher", "bucket", h.bucket)
			return
		}
		for {
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(backoff):
			}

			w, err := store.Watch(b.ctx, h.keyFilter)
			if err != nil {
				b.logger.Error("failed to re-establish KV watcher, will retry",
					"bucket", h.bucket, "retryIn", backoff, "error", err)
				backoff = nextKVWatchBackoff(backoff)
				continue
			}
			// Don't leak the new watcher if we lost the race with shutdown.
			if b.ctx.Err() != nil {
				_ = w.Stop()
				return
			}

			h.set(w)
			backoff = kvWatchRetryBaseDelay
			b.logger.Info("KV watcher re-established", "bucket", h.bucket)
			break
		}
	}
}

// consumeKVWatch reads from the watcher until its Updates() channel closes.
// Returns true if the loop should terminate permanently (context cancelled),
// false if the channel closed unexpectedly and the watcher should be
// re-established by the caller.
func (b *NATSBroker) consumeKVWatch(bucketName string, watcher jetstream.KeyWatcher) bool {
	for {
		select {
		case <-b.ctx.Done():
			b.logger.Debug("stopping KV watch processor", "bucket", bucketName)
			return true

		case entry, ok := <-watcher.Updates():
			if !ok {
				// Updates() channel closed. A closed channel yields (nil, false)
				// forever, so we MUST stop reading it here — otherwise this loop
				// spins at 100% CPU. Clean shutdown if the context is gone;
				// otherwise signal the caller to re-establish.
				if b.ctx.Err() != nil {
					return true
				}
				return false
			}
			if entry == nil {
				// Initial values sent, now receiving live updates
				b.logger.Info("KV watcher initial sync complete", "bucket", bucketName)
				continue
			}

			b.handleKVUpdate(bucketName, entry)
		}
	}
}

// handleKVUpdate processes a single KV update from the watcher
func (b *NATSBroker) handleKVUpdate(bucketName string, entry jetstream.KeyValueEntry) {
	key := entry.Key()

	// Check for delete operation
	if entry.Operation() == jetstream.KeyValueDelete || entry.Operation() == jetstream.KeyValuePurge {
		b.localKVCache.Delete(bucketName, key)
		b.logger.Debug("deleted from KV cache", "bucket", bucketName, "key", key, "operation", entry.Operation())
		return
	}

	// Process normal update/create.
	// UseNumber() preserves numeric precision by decoding numbers as json.Number
	// instead of float64, preventing silent data corruption on large integers.
	var parsedValue interface{}
	rawValue := entry.Value()

	if len(rawValue) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rawValue))
		dec.UseNumber()
		if err := dec.Decode(&parsedValue); err != nil {
			parsedValue = string(rawValue)
			b.logger.Debug("stored non-JSON KV update as string",
				"bucket", bucketName, "key", key, "error", err)
		}
	} else {
		parsedValue = nil
	}

	b.localKVCache.Set(bucketName, key, parsedValue)
	b.logger.Debug("updated KV cache from watcher",
		"bucket", bucketName,
		"key", key,
		"revision", entry.Revision())
}

// GetLocalKVCache returns the local KV cache instance
func (b *NATSBroker) GetLocalKVCache() *rule.LocalKVCache {
	return b.localKVCache
}

// initializeNATSConnection establishes the core NATS connection and JetStream context
func (b *NATSBroker) initializeNATSConnection() error {
	b.logger.Info("establishing NATS connection", "urls", b.config.NATS.URLs)

	natsOptions, err := b.buildNATSOptions()
	if err != nil {
		return fmt.Errorf("failed to build NATS options: %w", err)
	}

	urlString := strings.Join(b.config.NATS.URLs, ",")

	b.logger.Debug("connecting to NATS with failover URLs",
		"urlCount", len(b.config.NATS.URLs),
		"urlString", urlString)

	// Retry the initial connect a bounded number of times before giving up, so a
	// NATS server that is slow to start doesn't crash-loop the process. Downstream
	// setup (JetStream, ConnectedUrl) still sees a fully established connection.
	for attempt := 1; ; attempt++ {
		b.natsConn, err = nats.Connect(urlString, natsOptions...)
		if err == nil {
			break
		}
		if attempt >= initialConnectMaxAttempts {
			return fmt.Errorf("failed to connect to NATS after %d attempts: urls=%v, authMethod=%s, tlsEnabled=%v, reconnectWait=%v: %w",
				attempt,
				b.config.NATS.URLs,
				b.getAuthMethod(),
				b.config.NATS.TLS.Enable,
				b.config.NATS.Connection.ReconnectWait,
				err)
		}
		b.logger.Warn("initial NATS connect failed, retrying",
			"attempt", attempt,
			"maxAttempts", initialConnectMaxAttempts,
			"retryWait", initialConnectRetryWait,
			"error", err)
		time.Sleep(initialConnectRetryWait)
	}

	connectedURL := b.natsConn.ConnectedUrl()
	b.logger.Info("NATS connection established successfully",
		"connectedURL", connectedURL,
		"availableURLs", len(b.config.NATS.URLs))

	// Configure JetStream for Async Publishing ---
	jsOpts := []jetstream.JetStreamOpt{
		// Set a limit on the number of outstanding async publishes. This is crucial for backpressure.
		jetstream.WithPublishAsyncMaxPending(2048),
		// Set up a handler to log any errors that occur in the background.
		jetstream.WithPublishAsyncErrHandler(func(js jetstream.JetStream, msg *nats.Msg, err error) {
			b.logger.Error("asynchronous publish failed", "subject", msg.Subject, "error", err)
			if b.metrics != nil {
				// This metric tracks failures that happen after the initial send.
				b.metrics.IncActionPublishFailures()
			}
		}),
	}

	b.jetStream, err = jetstream.New(b.natsConn, jsOpts...)
	if err != nil {
		return fmt.Errorf("failed to create JetStream interface: %w", err)
	}

	b.logger.Info("JetStream interface created successfully with async publishing enabled")
	return nil
}

// initializeKVStores connects to configured KV buckets
func (b *NATSBroker) initializeKVStores(ctx context.Context) error {
	// Change the log message to reflect the new behavior
	b.logger.Info("validating configured KV stores are available", "buckets", b.config.KV.BucketNames())

	for _, bucket := range b.config.KV.Buckets {
		b.logger.Debug("connecting to KV bucket", "bucket", bucket.Name)

		kvCtx, cancel := context.WithTimeout(ctx, kvOperationTimeout)
		kv, err := b.jetStream.KeyValue(kvCtx, bucket.Name)
		cancel()

		if err != nil {
			if errors.Is(err, jetstream.ErrBucketNotFound) {
				// FAIL FAST: The bucket does not exist. Return a user-friendly error.
				return fmt.Errorf(
					"configured KV bucket not found: '%s'. Please create it before starting the application using 'nats kv add %s'",
					bucket.Name,
					bucket.Name,
				)
			}
			// For all other errors (permissions, connection issues), fail as before.
			return fmt.Errorf("failed to access KV bucket '%s': %w", bucket.Name, err)
		}

		b.kvStores[bucket.Name] = kv
		b.logger.Debug("successfully connected to KV bucket", "bucket", bucket.Name)
	}

	b.logger.Info("all configured KV stores validated successfully", "bucketCount", len(b.kvStores))
	return nil
}

// buildNATSOptions creates NATS connection options with proper authentication and TLS
func (b *NATSBroker) buildNATSOptions() ([]nats.Option, error) {
	var natsOptions []nats.Option

	natsOptions = append(natsOptions,
		nats.ReconnectWait(b.config.NATS.Connection.ReconnectWait),
		nats.MaxReconnects(b.config.NATS.Connection.MaxReconnects),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			b.logger.Warn("NATS client disconnected", "error", err)
			if b.metrics != nil {
				b.metrics.SetNATSConnectionStatus(false)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			b.logger.Info("NATS client reconnected", "url", nc.ConnectedUrl())
			if b.metrics != nil {
				b.metrics.SetNATSConnectionStatus(true)
				b.metrics.IncNATSReconnects()
			}
			// KV watchers hold consumer handles that become stale across a
			// reconnect — silently stop delivering updates. Re-establish them
			// so the local KV cache keeps up with bucket changes.
			b.restartKVWatchers()
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			b.logger.Error("NATS connection permanently closed", "error", nc.LastError())
			if b.metrics != nil {
				b.metrics.SetNATSConnectionStatus(false)
			}
		}),
		nats.ReconnectJitter(reconnectJitterMin, reconnectJitterMax),
		nats.DrainTimeout(drainTimeout),
	)

	if b.config.NATS.CredsFile != "" {
		b.logger.Info("using NATS JWT authentication with creds file", "credsFile", b.config.NATS.CredsFile)
		natsOptions = append(natsOptions, nats.UserCredentials(b.config.NATS.CredsFile))
	} else if b.config.NATS.NKeySeedFile != "" {
		b.logger.Info("using NATS NKey authentication", "seedFile", b.config.NATS.NKeySeedFile)
		nkeyOpt, err := nats.NkeyOptionFromSeed(b.config.NATS.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load NKey seed file: %w", err)
		}
		natsOptions = append(natsOptions, nkeyOpt)
	} else if b.config.NATS.Token != "" {
		b.logger.Info("using NATS token authentication")
		natsOptions = append(natsOptions, nats.Token(b.config.NATS.Token))
	} else if b.config.NATS.Username != "" {
		b.logger.Info("using NATS username/password authentication", "username", b.config.NATS.Username)
		natsOptions = append(natsOptions, nats.UserInfo(b.config.NATS.Username, b.config.NATS.Password))
	}

	if b.config.NATS.TLS.Enable {
		b.logger.Info("enabling TLS for NATS connection", "insecure", b.config.NATS.TLS.Insecure)

		tlsConfig := &tls.Config{
			InsecureSkipVerify: b.config.NATS.TLS.Insecure,
		}

		if b.config.NATS.TLS.CertFile != "" && b.config.NATS.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(b.config.NATS.TLS.CertFile, b.config.NATS.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load NATS TLS client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
			b.logger.Info("loaded NATS TLS client certificate", "certFile", b.config.NATS.TLS.CertFile)
		}

		if b.config.NATS.TLS.CAFile != "" {
			natsOptions = append(natsOptions, nats.RootCAs(b.config.NATS.TLS.CAFile))
			b.logger.Info("loaded NATS TLS CA certificate", "caFile", b.config.NATS.TLS.CAFile)
		}

		natsOptions = append(natsOptions, nats.Secure(tlsConfig))
	}

	return natsOptions, nil
}

// getAuthMethod returns a human-readable string describing the configured auth method
func (b *NATSBroker) getAuthMethod() string {
	if b.config.NATS.CredsFile != "" {
		return "creds_file"
	}
	if b.config.NATS.NKeySeedFile != "" {
		return "nkey"
	}
	if b.config.NATS.Token != "" {
		return "token"
	}
	if b.config.NATS.Username != "" {
		return "username/password"
	}
	return "none"
}

// GetKVStores returns a copy of the KV stores map
func (b *NATSBroker) GetKVStores() map[string]jetstream.KeyValue {
	stores := make(map[string]jetstream.KeyValue)
	for name, store := range b.kvStores {
		stores[name] = store
	}
	return stores
}

// Helper
// GetJetStream returns the JetStream interface
func (b *NATSBroker) GetJetStream() jetstream.JetStream {
	return b.jetStream
}

// GetNATSConn returns the NATS connection
func (b *NATSBroker) GetNATSConn() *nats.Conn {
	return b.natsConn
}

// GetConsumerName returns the consumer name for a subject
func (b *NATSBroker) GetConsumerName(subject string) string {
	b.consumersMu.RLock()
	name, exists := b.consumers[subject]
	b.consumersMu.RUnlock()
	if exists {
		return name
	}
	return b.generateConsumerName(subject)
}

// newActionMsg builds the NATS message for a rule action: the raw inbound
// payload on passthrough, the evaluated payload otherwise, plus any headers.
func newActionMsg(action *rule.NATSAction) *nats.Msg {
	msg := nats.NewMsg(action.Subject)
	if action.Passthrough {
		msg.Data = action.RawPayload
	} else {
		msg.Data = []byte(action.Payload)
	}
	if len(action.Headers) > 0 {
		msg.Header = make(nats.Header)
		for key, value := range action.Headers {
			msg.Header.Set(key, value)
		}
	}
	return msg
}

// Publish publishes a NATS action using the action's publish mode when set,
// falling back to the configured global mode (jetstream or core).
// This is used by the scheduler to publish without a SubscriptionManager.
func (b *NATSBroker) Publish(ctx context.Context, action *rule.NATSAction) error {
	msg := newActionMsg(action)

	if effectivePublishMode(action, b.config.NATS.Publish.Mode) == "core" {
		if len(msg.Header) == 0 {
			return b.natsConn.Publish(msg.Subject, msg.Data)
		}
		return b.natsConn.PublishMsg(msg)
	}

	// JetStream async publish
	ackF, err := b.jetStream.PublishMsgAsync(msg)
	if err != nil {
		return fmt.Errorf("jetstream async publish failed: %w", err)
	}

	pubCtx, cancel := context.WithTimeout(ctx, b.config.NATS.Publish.AckTimeout)
	defer cancel()

	select {
	case <-ackF.Ok():
		return nil
	case err := <-ackF.Err():
		return fmt.Errorf("jetstream publish ack failed: %w", err)
	case <-pubCtx.Done():
		return fmt.Errorf("timeout waiting for publish ack: %w", pubCtx.Err())
	}
}

// AddAndStartSubscription creates a consumer and immediately starts a pull subscription.
// Used by RuleKVManager to dynamically add subscriptions at runtime.
func (b *NATSBroker) AddAndStartSubscription(subject string) error {
	if b.subscriptionMgr == nil {
		return fmt.Errorf("subscription manager not initialized")
	}

	if err := b.CreateConsumerForSubject(subject); err != nil {
		return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
	}

	b.consumersMu.RLock()
	consumerName := b.consumers[subject]
	b.consumersMu.RUnlock()

	streamName, err := b.streamResolver.FindStreamForSubject(subject)
	if err != nil {
		return fmt.Errorf("failed to find stream for subject '%s': %w", subject, err)
	}

	workers := b.config.NATS.Consumers.WorkerCount
	return b.subscriptionMgr.AddAndStartSubscription(b.ctx, streamName, consumerName, subject, workers)
}

// RemoveSubscription stops and removes a subscription for the given subject.
func (b *NATSBroker) RemoveSubscription(subject string) {
	if b.subscriptionMgr != nil {
		b.subscriptionMgr.RemoveSubscription(subject)
	}
}

// RefreshStreams re-discovers all JetStream streams.
// Used to pick up streams created after initial startup.
func (b *NATSBroker) RefreshStreams() error {
	return b.streamResolver.Refresh(b.ctx)
}

// Close shuts down the broker connections
func (b *NATSBroker) Close() error {
	b.logger.Info("closing NATS broker connections")

	var errors []error

	// Cancel context to stop all watchers and long-running operations
	b.cancel()

	// Stop subscription manager
	if b.subscriptionMgr != nil {
		if err := b.subscriptionMgr.Stop(); err != nil {
			errors = append(errors, fmt.Errorf("failed to stop subscription manager: %w", err))
		}
	}

	// Stop all KV watchers explicitly (belt and suspenders with context cancellation).
	// b.cancel() above already cancelled b.ctx, so the goroutines exit instead of
	// re-creating once their channels close.
	b.kvWatchersMu.Lock()
	handles := b.kvWatchers
	b.kvWatchers = nil
	b.kvWatchersMu.Unlock()
	for i, h := range handles {
		if err := h.current().Stop(); err != nil {
			errors = append(errors, fmt.Errorf("failed to stop KV watcher %d (bucket %s): %w", i, h.bucket, err))
		}
	}

	// Wait for all KV watcher goroutines to finish with a timeout
	kvWatcherDone := make(chan struct{})
	go func() {
		b.kvWatcherWg.Wait()
		close(kvWatcherDone)
	}()

	select {
	case <-kvWatcherDone:
		b.logger.Debug("all KV watcher goroutines stopped")
	case <-time.After(watcherShutdownTimeout):
		b.logger.Warn("timeout waiting for KV watcher goroutines to stop")
	}

	b.consumersMu.RLock()
	consumerCount := len(b.consumers)
	b.consumersMu.RUnlock()
	b.logger.Info("durable consumers remain in NATS for next startup", "consumerCount", consumerCount)

	if b.natsConn != nil {
		b.logger.Info("draining NATS connection (publishing pending messages)")
		if err := b.natsConn.Drain(); err != nil {
			errors = append(errors, fmt.Errorf("failed to drain NATS connection: %w", err))
		} else {
			b.logger.Debug("NATS connection drained successfully")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during NATS broker shutdown: %v", errors)
	}

	b.logger.Info("successfully closed all NATS broker connections")
	return nil
}

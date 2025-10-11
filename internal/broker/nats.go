// file: internal/broker/nats.go

package broker

import (
	json "github.com/goccy/go-json"
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// NATSBroker connects to external NATS JetStream servers with KV support and local caching
type NATSBroker struct {
	logger          *logger.Logger
	metrics         *metrics.Metrics
	config          *config.Config
	
	// NATS connection and JetStream interface
	natsConn        *nats.Conn
	jetStream       jetstream.JetStream
	kvStores        map[string]jetstream.KeyValue
	
	// Local KV cache for performance optimization
	localKVCache    *rule.LocalKVCache
	kvWatchers      []jetstream.KeyWatcher
	
	// Stream resolver for JetStream stream discovery
	streamResolver  *StreamResolver
	
	// Subscription manager for message processing
	subscriptionMgr *SubscriptionManager
	
	// Consumer management - track created consumers
	consumers       map[string]string  // subject -> consumer name
	
	// Context for managing watchers and long-running operations
	ctx             context.Context
	cancel          context.CancelFunc
}

// NewNATSBroker creates a new NATS broker that connects to external NATS servers
func NewNATSBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*NATSBroker, error) {
	// Create cancellable context for broker lifecycle management
	ctx, cancel := context.WithCancel(context.Background())
	
	broker := &NATSBroker{
		logger:          log,
		metrics:         metrics,
		config:          cfg,
		kvStores:        make(map[string]jetstream.KeyValue),
		localKVCache:    rule.NewLocalKVCache(log),
		kvWatchers:      make([]jetstream.KeyWatcher, 0),
		consumers:       make(map[string]string),
		ctx:             ctx,
		cancel:          cancel,
	}

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
		"subscriberCount", b.config.NATS.Consumers.SubscriberCount,
		"fetchBatchSize", b.config.NATS.Consumers.FetchBatchSize,
		"fetchTimeout", b.config.NATS.Consumers.FetchTimeout,
		"publishMode", b.config.NATS.Publish.Mode)
}

// CreateConsumerForSubject creates a durable consumer for the given subject
func (b *NATSBroker) CreateConsumerForSubject(subject string) error {
	b.logger.Debug("creating consumer for subject", "subject", subject)

	// Check if consumer already exists for this subject
	if existingConsumer, exists := b.consumers[subject]; exists {
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
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()
	
	consumer, err := b.jetStream.CreateOrUpdateConsumer(ctx, streamName, consumerConfig)
	if err != nil {
		return fmt.Errorf("failed to create consumer '%s' on stream '%s' for subject '%s': %w",
			consumerName, streamName, subject, err)
	}

	// Track the consumer
	b.consumers[subject] = consumerName

	// Get consumer info for logging
	info, _ := consumer.Info(ctx)
	if info != nil {
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

// AddSubscription creates a pull subscription for a subject
func (b *NATSBroker) AddSubscription(subject string) error {
	if b.subscriptionMgr == nil {
		return fmt.Errorf("subscription manager not initialized")
	}

	// Get consumer name
	consumerName, exists := b.consumers[subject]
	if !exists {
		return fmt.Errorf("consumer not created for subject '%s'", subject)
	}

	// Get stream name
	streamName, err := b.streamResolver.FindStreamForSubject(subject)
	if err != nil {
		return fmt.Errorf("cannot find stream for subject '%s': %w", subject, err)
	}

	// Add subscription with configured worker count
	workers := b.config.NATS.Consumers.SubscriberCount
	if err := b.subscriptionMgr.AddSubscription(streamName, consumerName, subject, workers); err != nil {
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
	
	return fmt.Sprintf("rule-router-%s", sanitized)
}

// ValidateSubjects checks if all subjects can be mapped to streams
func (b *NATSBroker) ValidateSubjects(subjects []string) error {
	return b.streamResolver.ValidateSubjects(subjects)
}

// GetStreamResolver returns the stream resolver
func (b *NATSBroker) GetStreamResolver() *StreamResolver {
	return b.streamResolver
}

// GetSubscriptionManager returns the subscription manager
func (b *NATSBroker) GetSubscriptionManager() *SubscriptionManager {
	return b.subscriptionMgr
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

	b.logger.Info("initializing local KV cache", "buckets", b.config.KV.Buckets)

	// Populate cache with existing data
	if err := b.populateKVCache(); err != nil {
		b.logger.Error("failed to populate KV cache", "error", err)
		b.localKVCache.SetEnabled(false)
		return nil
	}

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

// populateKVCache loads all existing KV data into the local cache
func (b *NATSBroker) populateKVCache() error {
	b.logger.Info("populating local KV cache", "buckets", b.config.KV.Buckets)
	
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	
	totalLoaded := 0
	for _, bucketName := range b.config.KV.Buckets {
		loaded, err := b.loadBucketIntoCache(ctx, bucketName)
		if err != nil {
			b.logger.Warn("failed to load bucket into cache", "bucket", bucketName, "error", err)
			continue
		}
		totalLoaded += loaded
	}
	
	b.logger.Info("KV cache population complete", 
		"totalBuckets", len(b.config.KV.Buckets),
		"totalKeysLoaded", totalLoaded)
	
	return nil
}

// loadBucketIntoCache loads all keys from a specific bucket into the cache
func (b *NATSBroker) loadBucketIntoCache(ctx context.Context, bucketName string) (int, error) {
	store, exists := b.kvStores[bucketName]
	if !exists {
		return 0, fmt.Errorf("bucket not found: %s", bucketName)
	}

	// Use ListKeys to get all keys in the bucket
	lister, err := store.ListKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list keys in bucket %s: %w", bucketName, err)
	}

	loadedCount := 0
	for key := range lister.Keys() {
		entry, err := store.Get(ctx, key)
		if err != nil {
			b.logger.Debug("failed to get key during cache population", 
				"bucket", bucketName, "key", key, "error", err)
			continue
		}

		var parsedValue interface{}
		rawValue := entry.Value()
		if len(rawValue) > 0 {
			if err := json.Unmarshal(rawValue, &parsedValue); err != nil {
				parsedValue = string(rawValue)
				b.logger.Debug("stored non-JSON value as string", 
					"bucket", bucketName, "key", key)
			}
		} else {
			parsedValue = ""
		}

		b.localKVCache.Set(bucketName, key, parsedValue)
		loadedCount++

		if loadedCount%100 == 0 {
			b.logger.Debug("cache loading progress", 
				"bucket", bucketName, "loaded", loadedCount)
		}
	}

	b.logger.Info("loaded bucket into cache", 
		"bucket", bucketName, 
		"loadedKeys", loadedCount)
	
	return loadedCount, nil
}

// subscribeToKVChanges subscribes to KV change streams using the new Watch API
func (b *NATSBroker) subscribeToKVChanges() error {
	b.logger.Info("subscribing to KV changes using Watch API", "buckets", len(b.config.KV.Buckets))
	
	for _, bucketName := range b.config.KV.Buckets {
		if err := b.watchKVBucket(bucketName); err != nil {
			b.logger.Error("failed to watch KV bucket", "bucket", bucketName, "error", err)
			// Continue with other buckets
		}
	}
	
	b.logger.Info("KV watch subscriptions established", "watchers", len(b.kvWatchers))
	return nil
}

// watchKVBucket creates a watcher for a specific KV bucket using the new Watch API
func (b *NATSBroker) watchKVBucket(bucketName string) error {
	store, exists := b.kvStores[bucketName]
	if !exists {
		return fmt.Errorf("bucket not found: %s", bucketName)
	}
	
	// Watch all keys in the bucket using ">" pattern
	// Context from broker ensures watcher stops when broker closes
	watcher, err := store.Watch(b.ctx, ">")
	if err != nil {
		return fmt.Errorf("failed to create watcher for bucket %s: %w", bucketName, err)
	}
	
	b.kvWatchers = append(b.kvWatchers, watcher)
	
	// Start goroutine to process watch updates
	go b.processKVWatchUpdates(bucketName, watcher)
	
	b.logger.Info("created KV watcher", 
		"bucket", bucketName)
	
	return nil
}

// processKVWatchUpdates processes updates from a KV watcher
func (b *NATSBroker) processKVWatchUpdates(bucketName string, watcher jetstream.KeyWatcher) {
	b.logger.Debug("starting KV watch processor", "bucket", bucketName)
	
	for {
		select {
		case <-b.ctx.Done():
			b.logger.Debug("stopping KV watch processor", "bucket", bucketName)
			return
			
		case entry := <-watcher.Updates():
			if entry == nil {
				// Initial values sent, now receiving updates
				b.logger.Debug("KV watcher initial sync complete", "bucket", bucketName)
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
	
	// Process normal update/create
	var parsedValue interface{}
	rawValue := entry.Value()
	
	if len(rawValue) > 0 {
		if err := json.Unmarshal(rawValue, &parsedValue); err != nil {
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

	// FIXED: Pass ALL URLs for proper failover support
	// The NATS client will automatically try servers in order and handle reconnection
	urlString := strings.Join(b.config.NATS.URLs, ",")
	
	b.logger.Debug("connecting to NATS with failover URLs", 
		"urlCount", len(b.config.NATS.URLs),
		"urlString", urlString)

	b.natsConn, err = nats.Connect(urlString, natsOptions...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS (tried %d URLs): %w", len(b.config.NATS.URLs), err)
	}

	// Log which server we actually connected to
	connectedURL := b.natsConn.ConnectedUrl()
	b.logger.Info("NATS connection established successfully", 
		"connectedURL", connectedURL,
		"availableURLs", len(b.config.NATS.URLs))

	// Create JetStream interface using the new API
	b.jetStream, err = jetstream.New(b.natsConn)
	if err != nil {
		return fmt.Errorf("failed to create JetStream interface: %w", err)
	}

	b.logger.Info("JetStream interface created successfully")
	return nil
}

// initializeKVStores connects to configured KV buckets
func (b *NATSBroker) initializeKVStores(ctx context.Context) error {
	b.logger.Info("initializing KV stores", "buckets", b.config.KV.Buckets)

	for _, bucketName := range b.config.KV.Buckets {
		b.logger.Debug("connecting to KV bucket", "bucket", bucketName)
		
		// Use context with timeout for KV operations
		kvCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		
		kv, err := b.jetStream.KeyValue(kvCtx, bucketName)
		cancel()
		
		if err != nil {
			if err == jetstream.ErrBucketNotFound {
				b.logger.Info("KV bucket not found, creating", "bucket", bucketName)
				
				createCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				kv, err = b.jetStream.CreateKeyValue(createCtx, jetstream.KeyValueConfig{
					Bucket:      bucketName,
					Description: fmt.Sprintf("Rule-router KV bucket: %s", bucketName),
				})
				cancel()
				
				if err != nil {
					return fmt.Errorf("failed to create KV bucket '%s': %w", bucketName, err)
				}
				
				b.logger.Info("created KV bucket successfully", "bucket", bucketName)
			} else {
				return fmt.Errorf("failed to access KV bucket '%s': %w", bucketName, err)
			}
		}

		b.kvStores[bucketName] = kv
		b.logger.Debug("connected to KV bucket", "bucket", bucketName)
	}

	b.logger.Info("all KV stores initialized successfully", "bucketCount", len(b.kvStores))
	return nil
}

// buildNATSOptions creates NATS connection options with proper authentication and TLS
func (b *NATSBroker) buildNATSOptions() ([]nats.Option, error) {
	var natsOptions []nats.Option

	natsOptions = append(natsOptions,
		nats.ReconnectWait(b.config.NATS.Connection.ReconnectWait),
		nats.MaxReconnects(b.config.NATS.Connection.MaxReconnects),
	)

	if b.config.NATS.CredsFile != "" {
		b.logger.Info("using NATS JWT authentication with creds file", "credsFile", b.config.NATS.CredsFile)
		natsOptions = append(natsOptions, nats.UserCredentials(b.config.NATS.CredsFile))
	} else if b.config.NATS.NKey != "" {
		b.logger.Info("using NATS NKey authentication")
		natsOptions = append(natsOptions, nats.Nkey(b.config.NATS.NKey, nil))
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

// GetKVStores returns a copy of the KV stores map
func (b *NATSBroker) GetKVStores() map[string]jetstream.KeyValue {
	stores := make(map[string]jetstream.KeyValue)
	for name, store := range b.kvStores {
		stores[name] = store
	}
	return stores
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

	// Stop all KV watchers (they should stop via context, but ensure cleanup)
	for i, watcher := range b.kvWatchers {
		if err := watcher.Stop(); err != nil {
			errors = append(errors, fmt.Errorf("failed to stop KV watcher %d: %w", i, err))
		}
	}

	b.logger.Info("durable consumers remain in NATS for next startup", "consumerCount", len(b.consumers))

	// Close NATS connection
	if b.natsConn != nil {
		b.natsConn.Close()
		b.logger.Debug("closed NATS connection")
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during NATS broker shutdown: %v", errors)
	}

	b.logger.Info("successfully closed all NATS broker connections")
	return nil
}

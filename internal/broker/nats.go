//file: internal/broker/nats.go

package broker

import (
	json "github.com/goccy/go-json"
	"crypto/tls"
	"fmt"
	"strings"

	watermillNats "github.com/nats-io/nats.go"
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
	
	// NATS connection and JetStream context
	natsConn        *watermillNats.Conn
	jetStreamCtx    watermillNats.JetStreamContext
	kvStores        map[string]watermillNats.KeyValue
	
	// NATS options for authentication
	natsOptions     []watermillNats.Option
	
	// Local KV cache for performance optimization
	localKVCache    *rule.LocalKVCache
	kvSubscriptions []*watermillNats.Subscription
	
	// Stream resolver for JetStream stream discovery
	streamResolver  *StreamResolver
	
	// Subscription manager for direct NATS subscriptions
	subscriptionMgr *SubscriptionManager
	
	// Consumer management - track created consumers
	consumers       map[string]string  // subject -> consumer name
}

// NewNATSBroker creates a new NATS broker that connects to external NATS servers
func NewNATSBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*NATSBroker, error) {
	broker := &NATSBroker{
		logger:          log,
		metrics:         metrics,
		config:          cfg,
		kvStores:        make(map[string]watermillNats.KeyValue),
		localKVCache:    rule.NewLocalKVCache(log),
		kvSubscriptions: make([]*watermillNats.Subscription, 0),
		consumers:       make(map[string]string),
	}

	// Initialize NATS connection
	if err := broker.initializeNATSConnection(); err != nil {
		return nil, fmt.Errorf("failed to initialize NATS connection: %w", err)
	}

	// Initialize stream resolver for JetStream stream discovery
	broker.streamResolver = NewStreamResolver(broker.jetStreamCtx, log)
	
	// Discover streams immediately
	if err := broker.streamResolver.Discover(); err != nil {
		return nil, fmt.Errorf("failed to discover JetStream streams: %w", err)
	}

	// Initialize KV stores if enabled
	if cfg.KV.Enabled {
		if err := broker.initializeKVStores(); err != nil {
			return nil, fmt.Errorf("failed to initialize KV stores: %w", err)
		}
	}

	return broker, nil
}

// InitializeSubscriptionManager creates the subscription manager
// Must be called after rule processor is initialized
func (b *NATSBroker) InitializeSubscriptionManager(processor *rule.Processor) {
	b.subscriptionMgr = NewSubscriptionManager(
		b.jetStreamCtx,
		processor,
		b.logger,
		b.metrics,
	)
	b.logger.Info("subscription manager initialized")
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

	// Create consumer configuration
	consumerConfig := &watermillNats.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: subject,
		AckPolicy:     watermillNats.AckExplicitPolicy,
		AckWait:       b.config.Watermill.NATS.AckWaitTimeout,
		MaxDeliver:    b.config.Watermill.NATS.MaxDeliver,
		DeliverPolicy: watermillNats.DeliverAllPolicy,
		ReplayPolicy:  watermillNats.ReplayInstantPolicy,
	}

	// Check if consumer already exists in NATS
	existingConsumer, err := b.jetStreamCtx.ConsumerInfo(streamName, consumerName)
	if err == nil && existingConsumer != nil {
		b.logger.Info("reusing existing durable consumer",
			"stream", streamName,
			"consumer", consumerName,
			"subject", subject,
			"pending", existingConsumer.NumPending,
			"delivered", existingConsumer.Delivered.Stream)
		b.consumers[subject] = consumerName
		return nil
	}

	// Create the consumer
	consumerInfo, err := b.jetStreamCtx.AddConsumer(streamName, consumerConfig)
	if err != nil {
		return fmt.Errorf("failed to create consumer '%s' on stream '%s' for subject '%s': %w",
			consumerName, streamName, subject, err)
	}

	// Track the consumer
	b.consumers[subject] = consumerName

	b.logger.Info("created durable consumer",
		"stream", streamName,
		"consumer", consumerName,
		"subject", subject,
		"filterSubject", consumerInfo.Config.FilterSubject,
		"ackWait", consumerInfo.Config.AckWait)

	return nil
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
	workers := b.config.Watermill.NATS.SubscriberCount
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

// InitializeKVCache populates the local cache and subscribes to changes
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

	// Subscribe to KV changes for real-time updates
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
	
	totalLoaded := 0
	for _, bucketName := range b.config.KV.Buckets {
		loaded, err := b.loadBucketIntoCache(bucketName)
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
func (b *NATSBroker) loadBucketIntoCache(bucketName string) (int, error) {
	store, exists := b.kvStores[bucketName]
	if !exists {
		return 0, fmt.Errorf("bucket not found: %s", bucketName)
	}

	keys, err := store.Keys()
	if err != nil {
		if err == watermillNats.ErrNoKeysFound {
			b.logger.Debug("no keys found in bucket", "bucket", bucketName)
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get keys from bucket %s: %w", bucketName, err)
	}

	loadedCount := 0
	for _, key := range keys {
		entry, err := store.Get(key)
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
				"bucket", bucketName, "loaded", loadedCount, "total", len(keys))
		}
	}

	b.logger.Info("loaded bucket into cache", 
		"bucket", bucketName, 
		"totalKeys", len(keys),
		"loadedKeys", loadedCount)
	
	return loadedCount, nil
}

// subscribeToKVChanges subscribes to KV change streams for real-time cache updates
func (b *NATSBroker) subscribeToKVChanges() error {
	for _, bucketName := range b.config.KV.Buckets {
		if err := b.subscribeToKVBucket(bucketName); err != nil {
			b.logger.Error("failed to subscribe to KV bucket", "bucket", bucketName, "error", err)
		}
	}
	
	b.logger.Info("KV change subscriptions established", "buckets", len(b.kvSubscriptions))
	return nil
}

// subscribeToKVBucket subscribes to changes for a specific KV bucket
func (b *NATSBroker) subscribeToKVBucket(bucketName string) error {
	subject := fmt.Sprintf("$KV.%s.>", bucketName)
	
	sub, err := b.jetStreamCtx.Subscribe(subject, b.handleKVChange, 
		watermillNats.DeliverAll(),
		watermillNats.Durable(fmt.Sprintf("rule-router-kv-cache-%s", bucketName)),
		watermillNats.ManualAck(),
	)
	
	if err != nil {
		return fmt.Errorf("failed to subscribe to KV changes for bucket %s: %w", bucketName, err)
	}
	
	b.kvSubscriptions = append(b.kvSubscriptions, sub)
	
	b.logger.Info("subscribed to KV changes", 
		"bucket", bucketName, 
		"subject", subject,
		"durable", fmt.Sprintf("rule-router-kv-cache-%s", bucketName))
	
	return nil
}

// handleKVChange processes KV change notifications and updates the local cache
func (b *NATSBroker) handleKVChange(msg *watermillNats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 3 {
		b.logger.Debug("invalid KV change subject format", "subject", msg.Subject)
		msg.Ack()
		return
	}
	
	bucketName := parts[1]
	key := strings.Join(parts[2:], ".")
	
	b.logger.Debug("processing KV change", 
		"bucket", bucketName, "key", key, "dataLen", len(msg.Data))
	
	var parsedValue interface{}
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &parsedValue); err != nil {
			parsedValue = string(msg.Data)
			b.logger.Debug("stored non-JSON KV update as string", 
				"bucket", bucketName, "key", key, "error", err)
		}
	} else {
		parsedValue = nil
	}
	
	if parsedValue != nil {
		b.localKVCache.Set(bucketName, key, parsedValue)
		b.logger.Debug("updated KV cache from stream", "bucket", bucketName, "key", key)
	} else {
		b.localKVCache.Delete(bucketName, key)
		b.logger.Debug("deleted from KV cache from stream", "bucket", bucketName, "key", key)
	}
	
	msg.Ack()
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

	b.natsOptions = natsOptions

	b.natsConn, err = watermillNats.Connect(b.getFirstNATSURL(), natsOptions...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	b.jetStreamCtx, err = b.natsConn.JetStream()
	if err != nil {
		return fmt.Errorf("failed to create JetStream context: %w", err)
	}

	b.logger.Info("NATS connection established successfully")
	return nil
}

// initializeKVStores connects to configured KV buckets
func (b *NATSBroker) initializeKVStores() error {
	b.logger.Info("initializing KV stores", "buckets", b.config.KV.Buckets)

	for _, bucketName := range b.config.KV.Buckets {
		b.logger.Debug("connecting to KV bucket", "bucket", bucketName)
		
		kv, err := b.jetStreamCtx.KeyValue(bucketName)
		if err != nil {
			if err == watermillNats.ErrBucketNotFound {
				b.logger.Info("KV bucket not found, creating", "bucket", bucketName)
				
				kv, err = b.jetStreamCtx.CreateKeyValue(&watermillNats.KeyValueConfig{
					Bucket:      bucketName,
					Description: fmt.Sprintf("Rule-router KV bucket: %s", bucketName),
					MaxBytes:    1024 * 1024,
					TTL:         0,
					MaxValueSize: 1024,
				})
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
func (b *NATSBroker) buildNATSOptions() ([]watermillNats.Option, error) {
	var natsOptions []watermillNats.Option

	natsOptions = append(natsOptions,
		watermillNats.ReconnectWait(b.config.Watermill.NATS.ReconnectWait),
		watermillNats.MaxReconnects(b.config.Watermill.NATS.MaxReconnects),
	)

	if b.config.NATS.CredsFile != "" {
		b.logger.Info("using NATS JWT authentication with creds file", "credsFile", b.config.NATS.CredsFile)
		natsOptions = append(natsOptions, watermillNats.UserCredentials(b.config.NATS.CredsFile))
	} else if b.config.NATS.NKey != "" {
		b.logger.Info("using NATS NKey authentication")
		natsOptions = append(natsOptions, watermillNats.Nkey(b.config.NATS.NKey, nil))
	} else if b.config.NATS.Token != "" {
		b.logger.Info("using NATS token authentication")
		natsOptions = append(natsOptions, watermillNats.Token(b.config.NATS.Token))
	} else if b.config.NATS.Username != "" {
		b.logger.Info("using NATS username/password authentication", "username", b.config.NATS.Username)
		natsOptions = append(natsOptions, watermillNats.UserInfo(b.config.NATS.Username, b.config.NATS.Password))
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
			natsOptions = append(natsOptions, watermillNats.RootCAs(b.config.NATS.TLS.CAFile))
			b.logger.Info("loaded NATS TLS CA certificate", "caFile", b.config.NATS.TLS.CAFile)
		}

		natsOptions = append(natsOptions, watermillNats.Secure(tlsConfig))
	}

	return natsOptions, nil
}

// GetKVStores returns a copy of the KV stores map
func (b *NATSBroker) GetKVStores() map[string]watermillNats.KeyValue {
	stores := make(map[string]watermillNats.KeyValue)
	for name, store := range b.kvStores {
		stores[name] = store
	}
	return stores
}

// Close shuts down the broker connections
func (b *NATSBroker) Close() error {
	b.logger.Info("closing NATS broker connections")

	var errors []error

	// Stop subscription manager
	if b.subscriptionMgr != nil {
		if err := b.subscriptionMgr.Stop(); err != nil {
			errors = append(errors, fmt.Errorf("failed to stop subscription manager: %w", err))
		}
	}

	// Close KV change subscriptions
	for i, sub := range b.kvSubscriptions {
		if err := sub.Unsubscribe(); err != nil {
			errors = append(errors, fmt.Errorf("failed to unsubscribe from KV changes %d: %w", i, err))
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

// getFirstNATSURL returns the first NATS URL or a default
func (b *NATSBroker) getFirstNATSURL() string {
	if len(b.config.NATS.URLs) > 0 {
		return b.config.NATS.URLs[0]
	}
	return "nats://localhost:4222"
}

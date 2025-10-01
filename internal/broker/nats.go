//file: internal/broker/nats.go

package broker

import (
	json "github.com/goccy/go-json"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	watermillNats "github.com/nats-io/nats.go"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// NATSBroker connects to external NATS JetStream servers with KV support and local caching
type NATSBroker struct {
	publisher       message.Publisher
	subscriber      message.Subscriber
	router          *message.Router
	logger          *logger.Logger
	metrics         *metrics.Metrics
	config          *config.Config
	watermillLogger watermill.LoggerAdapter
	
	// NATS connection and JetStream context for KV access
	natsConn        *watermillNats.Conn
	jetStreamCtx    watermillNats.JetStreamContext
	kvStores        map[string]watermillNats.KeyValue  // bucket name -> KV store
	
	// FIXED: Store NATS options for reuse in publisher/subscriber creation
	// Watermill creates separate connections for publisher/subscriber, so they need auth options too
	natsOptions     []watermillNats.Option
	
	// Local KV cache for performance optimization
	localKVCache    *rule.LocalKVCache
	kvSubscriptions []*watermillNats.Subscription     // KV change stream subscriptions
}

// NewNATSBroker creates a new NATS broker that connects to external NATS servers
func NewNATSBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*NATSBroker, error) {
	// Create logger adapter
	watermillLogger := NewLoggerAdapter(log)

	broker := &NATSBroker{
		logger:          log,
		metrics:         metrics,
		config:          cfg,
		watermillLogger: watermillLogger,
		kvStores:        make(map[string]watermillNats.KeyValue),
		localKVCache:    rule.NewLocalKVCache(log),
		kvSubscriptions: make([]*watermillNats.Subscription, 0),
	}

	// Initialize NATS connection first (needed for both pub/sub and KV)
	if err := broker.initializeNATSConnection(); err != nil {
		return nil, fmt.Errorf("failed to initialize NATS connection: %w", err)
	}

	// Initialize KV stores if enabled
	if cfg.KV.Enabled {
		if err := broker.initializeKVStores(); err != nil {
			return nil, fmt.Errorf("failed to initialize KV stores: %w", err)
		}
	}

	// Initialize publisher and subscriber that use the stored NATS options
	if err := broker.initializePublisher(); err != nil {
		return nil, fmt.Errorf("failed to initialize NATS publisher: %w", err)
	}

	if err := broker.initializeSubscriber(); err != nil {
		return nil, fmt.Errorf("failed to initialize NATS subscriber: %w", err)
	}

	if err := broker.initializeRouter(); err != nil {
		return nil, fmt.Errorf("failed to initialize router: %w", err)
	}

	return broker, nil
}

// InitializeKVCache populates the local cache and subscribes to changes
// Call this after broker initialization to enable local KV caching
func (b *NATSBroker) InitializeKVCache() error {
	if !b.config.KV.Enabled {
		b.logger.Info("KV not enabled, skipping cache initialization")
		return nil
	}

	// Check if local cache is enabled in config
	if !b.config.KV.LocalCache.Enabled {
		b.logger.Info("local KV cache disabled in configuration")
		b.localKVCache.SetEnabled(false)
		return nil
	}

	b.logger.Info("initializing local KV cache", "buckets", b.config.KV.Buckets)

	// Populate cache with existing data
	if err := b.populateKVCache(); err != nil {
		b.logger.Error("failed to populate KV cache", "error", err)
		// Continue without cache - degrade gracefully
		b.localKVCache.SetEnabled(false)
		return nil
	}

	// Subscribe to KV changes for real-time updates
	if err := b.subscribeToKVChanges(); err != nil {
		b.logger.Error("failed to subscribe to KV changes", "error", err)
		// Continue without real-time updates - cache will be stale
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
			// Don't fail cache initialization for individual bucket failures
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

	// Get all keys in bucket (NATS KV feature)
	keys, err := store.Keys()
	if err != nil {
		// Handle case where bucket exists but has no keys
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
			continue // Skip bad keys, don't fail entire bucket
		}

		// Parse JSON once and store parsed result for maximum performance
		var parsedValue interface{}
		rawValue := entry.Value()
		if len(rawValue) > 0 {
			if err := json.Unmarshal(rawValue, &parsedValue); err != nil {
				// Store as string if JSON parsing fails (backward compatibility)
				parsedValue = string(rawValue)
				b.logger.Debug("stored non-JSON value as string", 
					"bucket", bucketName, "key", key)
			}
		} else {
			// Empty value
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
			// Continue with other buckets - partial functionality is better than none
		}
	}
	
	b.logger.Info("KV change subscriptions established", "buckets", len(b.kvSubscriptions))
	return nil
}

// subscribeToKVBucket subscribes to changes for a specific KV bucket
func (b *NATSBroker) subscribeToKVBucket(bucketName string) error {
	// KV change stream subject: $KV.bucket_name.>
	subject := fmt.Sprintf("$KV.%s.>", bucketName)
	
	// Create durable subscription for KV changes
	sub, err := b.jetStreamCtx.Subscribe(subject, b.handleKVChange, 
		watermillNats.DeliverAll(),                          // Get all changes from the beginning
		watermillNats.Durable(fmt.Sprintf("rule-router-kv-cache-%s", bucketName)), // Survive restarts
		watermillNats.ManualAck(),                           // Manual ack for reliability
	)
	
	if err != nil {
		return fmt.Errorf("failed to subscribe to KV changes for bucket %s: %w", bucketName, err)
	}
	
	// Store subscription for cleanup
	b.kvSubscriptions = append(b.kvSubscriptions, sub)
	
	b.logger.Info("subscribed to KV changes", 
		"bucket", bucketName, 
		"subject", subject,
		"durable", fmt.Sprintf("rule-router-kv-cache-%s", bucketName))
	
	return nil
}

// handleKVChange processes KV change notifications and updates the local cache
func (b *NATSBroker) handleKVChange(msg *watermillNats.Msg) {
	// Extract bucket and key from subject: $KV.customer_data.cust123
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 3 {
		b.logger.Debug("invalid KV change subject format", "subject", msg.Subject)
		msg.Ack() // Ack to avoid redelivery of bad messages
		return
	}
	
	bucketName := parts[1]
	key := strings.Join(parts[2:], ".") // Handle keys with dots in them
	
	b.logger.Debug("processing KV change", 
		"bucket", bucketName, "key", key, "dataLen", len(msg.Data))
	
	// Parse the new value
	var parsedValue interface{}
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &parsedValue); err != nil {
			// Store as string if JSON parsing fails
			parsedValue = string(msg.Data)
			b.logger.Debug("stored non-JSON KV update as string", 
				"bucket", bucketName, "key", key, "error", err)
		}
	} else {
		// Empty data typically means key was deleted
		parsedValue = nil
	}
	
	// Update local cache
	if parsedValue != nil {
		b.localKVCache.Set(bucketName, key, parsedValue)
		b.logger.Debug("updated KV cache from stream", "bucket", bucketName, "key", key)
	} else {
		b.localKVCache.Delete(bucketName, key)
		b.logger.Debug("deleted from KV cache from stream", "bucket", bucketName, "key", key)
	}
	
	// Acknowledge the message
	msg.Ack()
}

// GetLocalKVCache returns the local KV cache instance
func (b *NATSBroker) GetLocalKVCache() *rule.LocalKVCache {
	return b.localKVCache
}

// initializeNATSConnection establishes the core NATS connection and JetStream context
func (b *NATSBroker) initializeNATSConnection() error {
	b.logger.Info("establishing NATS connection", "urls", b.config.NATS.URLs)

	// Configure NATS connection options WITH authentication
	natsOptions, err := b.buildNATSOptions()
	if err != nil {
		return fmt.Errorf("failed to build NATS options: %w", err)
	}

	// FIXED: Store the options for reuse in publisher/subscriber creation
	// This ensures all Watermill components use the same authentication
	b.natsOptions = natsOptions

	// Connect to NATS with authenticated options
	b.natsConn, err = watermillNats.Connect(b.getFirstNATSURL(), natsOptions...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Create JetStream context for KV operations
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
		
		// Try to access existing bucket
		kv, err := b.jetStreamCtx.KeyValue(bucketName)
		if err != nil {
			// If bucket doesn't exist, create it with sensible defaults
			if err == watermillNats.ErrBucketNotFound {
				b.logger.Info("KV bucket not found, creating", "bucket", bucketName)
				
				kv, err = b.jetStreamCtx.CreateKeyValue(&watermillNats.KeyValueConfig{
					Bucket:      bucketName,
					Description: fmt.Sprintf("Rule-router KV bucket: %s", bucketName),
					MaxBytes:    1024 * 1024,          // 1MB default
					TTL:         0,                    // No expiration by default
					MaxValueSize: 1024,               // 1KB per value default
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

// initializePublisher creates a publisher using the stored NATS authentication options
func (b *NATSBroker) initializePublisher() error {
	b.logger.Info("initializing NATS publisher")

	// Configure JetStream for high performance
	jsConfig := nats.JetStreamConfig{
		Disabled:      false,
		AutoProvision: true,
		ConnectOptions: []watermillNats.JSOpt{
			watermillNats.PublishAsyncMaxPending(b.config.Watermill.NATS.MaxPendingAsync),
		},
		AckAsync: b.config.Watermill.NATS.PublishAsync,
	}

	publisherConfig := nats.PublisherConfig{
		URL:         b.getFirstNATSURL(),
		NatsOptions: b.natsOptions, // FIXED: Use stored auth options
		JetStream:   jsConfig,
		Marshaler:   &nats.NATSMarshaler{},
	}

	var err error
	b.publisher, err = nats.NewPublisher(publisherConfig, b.watermillLogger)
	if err != nil {
		return fmt.Errorf("failed to create NATS publisher: %w", err)
	}

	b.logger.Info("NATS publisher initialized successfully")
	return nil
}

// initializeSubscriber creates a subscriber using the stored NATS authentication options
func (b *NATSBroker) initializeSubscriber() error {
	b.logger.Info("initializing NATS subscriber", "subscriberCount", b.config.Watermill.NATS.SubscriberCount)

	// Configure JetStream consumer with improved naming
	jsConfig := nats.JetStreamConfig{
		AutoProvision: true,
		DurablePrefix: "rule-router-consumer",  // More descriptive naming
		SubscribeOptions: []watermillNats.SubOpt{
			watermillNats.AckWait(b.config.Watermill.NATS.AckWaitTimeout),
			watermillNats.MaxDeliver(b.config.Watermill.NATS.MaxDeliver),
			watermillNats.DeliverAll(),
			watermillNats.MaxAckPending(1000),
		},
	}

	subscriberConfig := nats.SubscriberConfig{
		URL:              b.getFirstNATSURL(),
		QueueGroupPrefix: "rule-router-workers",  // Consistent naming
		SubscribersCount: b.config.Watermill.NATS.SubscriberCount,
		AckWaitTimeout:   b.config.Watermill.NATS.AckWaitTimeout,
		NatsOptions:      b.natsOptions, // FIXED: Use stored auth options
		JetStream:        jsConfig,
		Unmarshaler:      &nats.NATSMarshaler{},
	}

	var err error
	b.subscriber, err = nats.NewSubscriber(subscriberConfig, b.watermillLogger)
	if err != nil {
		return fmt.Errorf("failed to create NATS subscriber: %w", err)
	}

	b.logger.Info("NATS subscriber initialized successfully")
	return nil
}

// buildNATSOptions creates NATS connection options with proper authentication and TLS
func (b *NATSBroker) buildNATSOptions() ([]watermillNats.Option, error) {
	var natsOptions []watermillNats.Option

	// Connection behavior options
	natsOptions = append(natsOptions,
		watermillNats.ReconnectWait(b.config.Watermill.NATS.ReconnectWait),
		watermillNats.MaxReconnects(b.config.Watermill.NATS.MaxReconnects),
	)

	// Authentication options (mutually exclusive)
	if b.config.NATS.CredsFile != "" {
		// JWT authentication with .creds file
		b.logger.Info("using NATS JWT authentication with creds file", "credsFile", b.config.NATS.CredsFile)
		natsOptions = append(natsOptions, watermillNats.UserCredentials(b.config.NATS.CredsFile))
	} else if b.config.NATS.NKey != "" {
		// NKey authentication
		b.logger.Info("using NATS NKey authentication")
		natsOptions = append(natsOptions, watermillNats.Nkey(b.config.NATS.NKey, nil))
	} else if b.config.NATS.Token != "" {
		// Token authentication
		b.logger.Info("using NATS token authentication")
		natsOptions = append(natsOptions, watermillNats.Token(b.config.NATS.Token))
	} else if b.config.NATS.Username != "" {
		// Username/password authentication
		b.logger.Info("using NATS username/password authentication", "username", b.config.NATS.Username)
		natsOptions = append(natsOptions, watermillNats.UserInfo(b.config.NATS.Username, b.config.NATS.Password))
	}

	// TLS configuration
	if b.config.NATS.TLS.Enable {
		b.logger.Info("enabling TLS for NATS connection", "insecure", b.config.NATS.TLS.Insecure)
		
		tlsConfig := &tls.Config{
			InsecureSkipVerify: b.config.NATS.TLS.Insecure,
		}

		// Load client certificates if provided
		if b.config.NATS.TLS.CertFile != "" && b.config.NATS.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(b.config.NATS.TLS.CertFile, b.config.NATS.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load NATS TLS client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
			b.logger.Info("loaded NATS TLS client certificate", "certFile", b.config.NATS.TLS.CertFile)
		}

		// Load CA certificate if provided
		if b.config.NATS.TLS.CAFile != "" {
			natsOptions = append(natsOptions, watermillNats.RootCAs(b.config.NATS.TLS.CAFile))
			b.logger.Info("loaded NATS TLS CA certificate", "caFile", b.config.NATS.TLS.CAFile)
		}

		natsOptions = append(natsOptions, watermillNats.Secure(tlsConfig))
	}

	return natsOptions, nil
}

// initializeRouter creates the Watermill router
func (b *NATSBroker) initializeRouter() error {
	b.logger.Info("initializing Watermill router")

	var err error
	b.router, err = message.NewRouter(message.RouterConfig{
		CloseTimeout: b.config.Watermill.Router.CloseTimeout,
	}, b.watermillLogger)
	if err != nil {
		return fmt.Errorf("failed to create router: %w", err)
	}

	b.logger.Info("Watermill router initialized successfully")
	return nil
}

// GetKVStore safely retrieves a KV store by bucket name
func (b *NATSBroker) GetKVStore(bucketName string) (watermillNats.KeyValue, bool) {
	store, exists := b.kvStores[bucketName]
	return store, exists
}

// GetKVStores returns a copy of the KV stores map for initialization
func (b *NATSBroker) GetKVStores() map[string]watermillNats.KeyValue {
	stores := make(map[string]watermillNats.KeyValue)
	for name, store := range b.kvStores {
		stores[name] = store
	}
	return stores
}

// GetPublisher returns the NATS publisher
func (b *NATSBroker) GetPublisher() message.Publisher {
	return b.publisher
}

// GetSubscriber returns the NATS subscriber
func (b *NATSBroker) GetSubscriber() message.Subscriber {
	return b.subscriber
}

// GetRouter returns the Watermill router
func (b *NATSBroker) GetRouter() *message.Router {
	return b.router
}

// Close shuts down the broker connections
func (b *NATSBroker) Close() error {
	b.logger.Info("closing NATS broker connections")

	var errors []error

	// Close KV change subscriptions first
	for i, sub := range b.kvSubscriptions {
		if err := sub.Unsubscribe(); err != nil {
			errors = append(errors, fmt.Errorf("failed to unsubscribe from KV changes %d: %w", i, err))
		}
	}

	if b.router != nil {
		if err := b.router.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close router: %w", err))
		}
	}

	if b.subscriber != nil {
		if err := b.subscriber.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close NATS subscriber: %w", err))
		}
	}

	if b.publisher != nil {
		if err := b.publisher.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close NATS publisher: %w", err))
		}
	}

	// Close NATS connection (this will also close KV stores)
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

// LoggerAdapter adapts our logger to Watermill's interface
type LoggerAdapter struct {
	logger *logger.Logger
}

// NewLoggerAdapter creates a new logger adapter
func NewLoggerAdapter(logger *logger.Logger) *LoggerAdapter {
	return &LoggerAdapter{logger: logger}
}

func (l *LoggerAdapter) Error(msg string, err error, fields watermill.LogFields) {
	args := []interface{}{"error", err}
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Error(msg, args...)
}

func (l *LoggerAdapter) Info(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Info(msg, args...)
}

func (l *LoggerAdapter) Debug(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Debug(msg, args...)
}

func (l *LoggerAdapter) Trace(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Debug(msg, args...) // Map Trace to Debug
}

func (l *LoggerAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	// For simplicity, return the same logger
	return l
}

// getFirstNATSURL returns the first NATS URL or a default
func (b *NATSBroker) getFirstNATSURL() string {
	if len(b.config.NATS.URLs) > 0 {
		return b.config.NATS.URLs[0]
	}
	return "nats://localhost:4222"
}

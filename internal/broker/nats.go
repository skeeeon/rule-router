//file: internal/broker/nats.go

package broker

import (
	"crypto/tls"
	"fmt"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	watermillNats "github.com/nats-io/nats.go"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// NATSBroker connects to external NATS JetStream servers with KV support
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

	// Initialize publisher and subscriber that use the existing connection
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

// initializeNATSConnection establishes the core NATS connection and JetStream context
func (b *NATSBroker) initializeNATSConnection() error {
	b.logger.Info("establishing NATS connection", "urls", b.config.NATS.URLs)

	// Configure NATS connection options
	natsOptions, err := b.buildNATSOptions()
	if err != nil {
		return fmt.Errorf("failed to build NATS options: %w", err)
	}

	// Connect to NATS
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

// initializePublisher creates a publisher using the existing NATS connection
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
		NatsOptions: []watermillNats.Option{}, // Options already applied to connection
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

// initializeSubscriber creates a subscriber using the existing NATS connection
func (b *NATSBroker) initializeSubscriber() error {
	b.logger.Info("initializing NATS subscriber", "subscriberCount", b.config.Watermill.NATS.SubscriberCount)

	// Configure JetStream consumer
	jsConfig := nats.JetStreamConfig{
		AutoProvision: true,
		DurablePrefix: "watermill-consumer",
		SubscribeOptions: []watermillNats.SubOpt{
			watermillNats.AckWait(b.config.Watermill.NATS.AckWaitTimeout),
			watermillNats.MaxDeliver(b.config.Watermill.NATS.MaxDeliver),
			watermillNats.DeliverAll(),
			watermillNats.MaxAckPending(1000),
		},
	}

	subscriberConfig := nats.SubscriberConfig{
		URL:              b.getFirstNATSURL(),
		QueueGroupPrefix: "rule-engine-workers",
		SubscribersCount: b.config.Watermill.NATS.SubscriberCount,
		AckWaitTimeout:   b.config.Watermill.NATS.AckWaitTimeout,
		NatsOptions:      []watermillNats.Option{}, // Options already applied to connection
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

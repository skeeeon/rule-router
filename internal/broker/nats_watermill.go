//file: internal/broker/nats_watermill.go

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

// WatermillNATSBroker connects to external NATS JetStream servers
type WatermillNATSBroker struct {
	publisher   message.Publisher
	subscriber  message.Subscriber
	router      *message.Router
	logger      *logger.Logger
	metrics     *metrics.Metrics
	config      *config.Config
	watermillLogger watermill.LoggerAdapter
}

// NewWatermillNATSBroker creates a new Watermill NATS broker that connects to external NATS servers
func NewWatermillNATSBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*WatermillNATSBroker, error) {
	if cfg.BrokerType != "nats" {
		return nil, fmt.Errorf("broker type must be 'nats' for NATS broker")
	}

	// Create Watermill logger adapter
	watermillLogger := NewWatermillLoggerAdapter(log)

	broker := &WatermillNATSBroker{
		logger:          log,
		metrics:         metrics,
		config:          cfg,
		watermillLogger: watermillLogger,
	}

	// Initialize publisher and subscriber that connect to external NATS
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

// initializePublisher creates a publisher that connects to external NATS JetStream
func (b *WatermillNATSBroker) initializePublisher() error {
	b.logger.Info("connecting to external NATS JetStream server for publishing",
		"urls", b.config.NATS.URLs)

	// Configure JetStream for high performance
	jsConfig := nats.JetStreamConfig{
		Disabled:      false,
		AutoProvision: true,
		ConnectOptions: []watermillNats.JSOpt{
			watermillNats.PublishAsyncMaxPending(b.config.Watermill.NATS.MaxPendingAsync),
		},
		AckAsync: b.config.Watermill.NATS.PublishAsync,
	}

	// Configure NATS connection options
	natsOptions, err := b.buildNATSOptions()
	if err != nil {
		return fmt.Errorf("failed to build NATS options: %w", err)
	}

	publisherConfig := nats.PublisherConfig{
		URL:         b.getFirstNATSURL(), // Use helper to get first URL
		NatsOptions: natsOptions,
		JetStream:   jsConfig,
		Marshaler:   &nats.NATSMarshaler{},
	}

	b.publisher, err = nats.NewPublisher(publisherConfig, b.watermillLogger)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS server for publishing: %w", err)
	}

	b.logger.Info("successfully connected to NATS JetStream for publishing")
	return nil
}

// initializeSubscriber creates a subscriber that connects to external NATS JetStream
func (b *WatermillNATSBroker) initializeSubscriber() error {
	b.logger.Info("connecting to external NATS JetStream server for subscription",
		"urls", b.config.NATS.URLs,
		"subscriberCount", b.config.Watermill.NATS.SubscriberCount)

	// Configure JetStream consumer
	jsConfig := nats.JetStreamConfig{
		AutoProvision: true,
		DurablePrefix: "watermill-consumer", // Durable consumers for reliability
		SubscribeOptions: []watermillNats.SubOpt{
			watermillNats.AckWait(b.config.Watermill.NATS.AckWaitTimeout),
			watermillNats.MaxDeliver(b.config.Watermill.NATS.MaxDeliver),
			watermillNats.DeliverAll(), // Process all available messages
			watermillNats.MaxAckPending(1000), // This is a SubOpt, not JSOpt
		},
	}

	// Configure NATS connection options
	natsOptions, err := b.buildNATSOptions()
	if err != nil {
		return fmt.Errorf("failed to build NATS options: %w", err)
	}

	subscriberConfig := nats.SubscriberConfig{
		URL:              b.getFirstNATSURL(),
		QueueGroupPrefix: "rule-engine-workers", // Load balancing across instances
		SubscribersCount: b.config.Watermill.NATS.SubscriberCount, // Parallel consumers
		AckWaitTimeout:   b.config.Watermill.NATS.AckWaitTimeout,
		NatsOptions:      natsOptions,
		JetStream:        jsConfig,
		Unmarshaler:      &nats.NATSMarshaler{},
	}

	b.subscriber, err = nats.NewSubscriber(subscriberConfig, b.watermillLogger)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS server for subscription: %w", err)
	}

	b.logger.Info("successfully connected to NATS JetStream for subscription",
		"parallelConsumers", b.config.Watermill.NATS.SubscriberCount)
	return nil
}

// buildNATSOptions creates NATS connection options with proper authentication and TLS
func (b *WatermillNATSBroker) buildNATSOptions() ([]watermillNats.Option, error) {
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
func (b *WatermillNATSBroker) initializeRouter() error {
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

// GetPublisher returns the NATS publisher
func (b *WatermillNATSBroker) GetPublisher() message.Publisher {
	return b.publisher
}

// GetSubscriber returns the NATS subscriber
func (b *WatermillNATSBroker) GetSubscriber() message.Subscriber {
	return b.subscriber
}

// GetRouter returns the Watermill router
func (b *WatermillNATSBroker) GetRouter() *message.Router {
	return b.router
}

// Close shuts down the broker connections
func (b *WatermillNATSBroker) Close() error {
	b.logger.Info("closing connections to NATS JetStream server")

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

	if len(errors) > 0 {
		return fmt.Errorf("errors during NATS broker shutdown: %v", errors)
	}

	b.logger.Info("successfully closed all connections to NATS JetStream server")
	return nil
}

// WatermillLoggerAdapter adapts our logger to Watermill's interface
type WatermillLoggerAdapter struct {
	logger *logger.Logger
}

// NewWatermillLoggerAdapter creates a new logger adapter
func NewWatermillLoggerAdapter(logger *logger.Logger) *WatermillLoggerAdapter {
	return &WatermillLoggerAdapter{logger: logger}
}

func (l *WatermillLoggerAdapter) Error(msg string, err error, fields watermill.LogFields) {
	args := []interface{}{"error", err}
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Error(msg, args...)
}

func (l *WatermillLoggerAdapter) Info(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Info(msg, args...)
}

func (l *WatermillLoggerAdapter) Debug(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Debug(msg, args...)
}

func (l *WatermillLoggerAdapter) Trace(msg string, fields watermill.LogFields) {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	l.logger.Debug(msg, args...) // Map Trace to Debug
}

func (l *WatermillLoggerAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	// For simplicity, return the same logger
	return l
}

// getFirstNATSURL returns the first NATS URL or a default
func (b *WatermillNATSBroker) getFirstNATSURL() string {
	if len(b.config.NATS.URLs) > 0 {
		return b.config.NATS.URLs[0]
	}
	return "nats://localhost:4222"
}

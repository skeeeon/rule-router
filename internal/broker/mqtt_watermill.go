//file: internal/broker/mqtt_watermill.go

package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// WatermillMQTTBroker connects to external MQTT brokers
type WatermillMQTTBroker struct {
	publisher   *MQTTPublisher
	subscriber  *MQTTSubscriber
	router      *message.Router
	logger      *logger.Logger
	metrics     *metrics.Metrics
	config      *config.Config
	watermillLogger watermill.LoggerAdapter
}

// NewWatermillMQTTBroker creates a new Watermill MQTT broker that connects to external MQTT servers
func NewWatermillMQTTBroker(cfg *config.Config, log *logger.Logger, metrics *metrics.Metrics) (*WatermillMQTTBroker, error) {
	if cfg.BrokerType != "mqtt" {
		return nil, fmt.Errorf("broker type must be 'mqtt' for MQTT broker")
	}

	// Create Watermill logger adapter
	watermillLogger := NewWatermillLoggerAdapter(log)

	broker := &WatermillMQTTBroker{
		logger:          log,
		metrics:         metrics,
		config:          cfg,
		watermillLogger: watermillLogger,
	}

	// Initialize publisher and subscriber that connect to external MQTT brokers
	if err := broker.initializePublisher(); err != nil {
		return nil, fmt.Errorf("failed to initialize MQTT publisher: %w", err)
	}

	if err := broker.initializeSubscriber(); err != nil {
		return nil, fmt.Errorf("failed to initialize MQTT subscriber: %w", err)
	}

	if err := broker.initializeRouter(); err != nil {
		return nil, fmt.Errorf("failed to initialize router: %w", err)
	}

	return broker, nil
}

// initializePublisher creates a publisher that connects to external MQTT broker
func (b *WatermillMQTTBroker) initializePublisher() error {
	b.logger.Info("connecting to external MQTT broker for publishing",
		"broker", b.config.MQTT.Broker,
		"clientId", b.config.MQTT.ClientID+"-pub")

	var err error
	b.publisher, err = NewMQTTPublisher(b.config, b.logger, "-pub")
	if err != nil {
		return fmt.Errorf("failed to connect to MQTT broker for publishing: %w", err)
	}

	b.logger.Info("successfully connected to MQTT broker for publishing")
	return nil
}

// initializeSubscriber creates a subscriber that connects to external MQTT broker
func (b *WatermillMQTTBroker) initializeSubscriber() error {
	b.logger.Info("connecting to external MQTT broker for subscription",
		"broker", b.config.MQTT.Broker,
		"clientId", b.config.MQTT.ClientID+"-sub")

	var err error
	b.subscriber, err = NewMQTTSubscriber(b.config, b.logger, "-sub")
	if err != nil {
		return fmt.Errorf("failed to connect to MQTT broker for subscription: %w", err)
	}

	b.logger.Info("successfully connected to MQTT broker for subscription")
	return nil
}

// initializeRouter creates the Watermill router
func (b *WatermillMQTTBroker) initializeRouter() error {
	b.logger.Info("initializing Watermill router for MQTT")

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

// GetPublisher returns the MQTT publisher
func (b *WatermillMQTTBroker) GetPublisher() message.Publisher {
	return b.publisher
}

// GetSubscriber returns the MQTT subscriber  
func (b *WatermillMQTTBroker) GetSubscriber() message.Subscriber {
	return b.subscriber
}

// GetRouter returns the Watermill router
func (b *WatermillMQTTBroker) GetRouter() *message.Router {
	return b.router
}

// Close shuts down the broker connections
func (b *WatermillMQTTBroker) Close() error {
	b.logger.Info("closing connections to MQTT broker")

	var errors []error

	if b.router != nil {
		if err := b.router.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close router: %w", err))
		}
	}

	if b.subscriber != nil {
		if err := b.subscriber.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close MQTT subscriber: %w", err))
		}
	}

	if b.publisher != nil {
		if err := b.publisher.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close MQTT publisher: %w", err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during MQTT broker shutdown: %v", errors)
	}

	b.logger.Info("successfully closed all connections to MQTT broker")
	return nil
}

// MQTTPublisher implements Watermill Publisher interface for external MQTT brokers
type MQTTPublisher struct {
	client mqtt.Client
	config *config.Config
	logger *logger.Logger
	mu     sync.RWMutex
}

// NewMQTTPublisher creates a publisher that connects to external MQTT broker
func NewMQTTPublisher(cfg *config.Config, logger *logger.Logger, clientSuffix string) (*MQTTPublisher, error) {
	clientID := cfg.MQTT.ClientID + clientSuffix
	
	// Create MQTT client options with full authentication and TLS support
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTT.Broker).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * 1000) // 5 seconds

	// Authentication
	if cfg.MQTT.Username != "" {
		opts.SetUsername(cfg.MQTT.Username)
		logger.Info("using MQTT username authentication", "username", cfg.MQTT.Username)
	}
	if cfg.MQTT.Password != "" {
		opts.SetPassword(cfg.MQTT.Password)
	}

	// TLS configuration
	if cfg.MQTT.TLS.Enable {
		logger.Info("enabling TLS for MQTT connection", "insecure", cfg.MQTT.TLS.Insecure)
		
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.MQTT.TLS.Insecure,
		}

		// Load client certificates if provided
		if cfg.MQTT.TLS.CertFile != "" && cfg.MQTT.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.MQTT.TLS.CertFile, cfg.MQTT.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load MQTT TLS client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
			logger.Info("loaded MQTT TLS client certificate", "certFile", cfg.MQTT.TLS.CertFile)
		}

		// Load CA certificate if provided
		if cfg.MQTT.TLS.CAFile != "" {
			caCert, err := os.ReadFile(cfg.MQTT.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read MQTT CA certificate: %w", err)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse MQTT CA certificate")
			}
			tlsConfig.RootCAs = caCertPool
			logger.Info("loaded MQTT TLS CA certificate", "caFile", cfg.MQTT.TLS.CAFile)
		}

		opts.SetTLSConfig(tlsConfig)
	}

	// Connection callbacks
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		logger.Info("MQTT publisher connected", "broker", cfg.MQTT.Broker, "clientId", clientID)
	})

	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		logger.Error("MQTT publisher connection lost", "error", err, "clientId", clientID)
	})

	client := mqtt.NewClient(opts)
	
	// Connect to the broker
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("failed to connect MQTT publisher to %s: %w", cfg.MQTT.Broker, token.Error())
	}

	return &MQTTPublisher{
		client: client,
		config: cfg,
		logger: logger,
	}, nil
}

// Publish implements the Watermill Publisher interface
func (p *MQTTPublisher) Publish(topic string, messages ...*message.Message) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.client.IsConnected() {
		return fmt.Errorf("MQTT publisher not connected to broker")
	}

	for _, msg := range messages {
		// Use the topic from message metadata if available, otherwise use provided topic
		publishTopic := msg.Metadata.Get("topic")
		if publishTopic == "" {
			publishTopic = topic
		}

		token := p.client.Publish(publishTopic, p.config.MQTT.QoS, false, msg.Payload)
		if token.Wait() && token.Error() != nil {
			p.logger.Error("failed to publish MQTT message",
				"error", token.Error(),
				"topic", publishTopic,
				"uuid", msg.UUID,
				"broker", p.config.MQTT.Broker)
			return token.Error()
		}

		p.logger.Debug("published MQTT message",
			"topic", publishTopic,
			"uuid", msg.UUID,
			"payloadSize", len(msg.Payload),
			"qos", p.config.MQTT.QoS)
	}
	return nil
}

// Close closes the MQTT publisher connection
func (p *MQTTPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client.IsConnected() {
		p.client.Disconnect(250)
		p.logger.Info("MQTT publisher disconnected")
	}
	return nil
}

// MQTTSubscriber implements Watermill Subscriber interface for external MQTT brokers
type MQTTSubscriber struct {
	client     mqtt.Client
	config     *config.Config
	logger     *logger.Logger
	mu         sync.RWMutex
	subscriptions map[string]chan *message.Message
}

// NewMQTTSubscriber creates a subscriber that connects to external MQTT broker
func NewMQTTSubscriber(cfg *config.Config, logger *logger.Logger, clientSuffix string) (*MQTTSubscriber, error) {
	clientID := cfg.MQTT.ClientID + clientSuffix
	
	// Create MQTT client options with full authentication and TLS support
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTT.Broker).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * 1000) // 5 seconds

	// Authentication
	if cfg.MQTT.Username != "" {
		opts.SetUsername(cfg.MQTT.Username)
		logger.Info("using MQTT username authentication", "username", cfg.MQTT.Username)
	}
	if cfg.MQTT.Password != "" {
		opts.SetPassword(cfg.MQTT.Password)
	}

	// TLS configuration
	if cfg.MQTT.TLS.Enable {
		logger.Info("enabling TLS for MQTT connection", "insecure", cfg.MQTT.TLS.Insecure)
		
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.MQTT.TLS.Insecure,
		}

		// Load client certificates if provided
		if cfg.MQTT.TLS.CertFile != "" && cfg.MQTT.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.MQTT.TLS.CertFile, cfg.MQTT.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load MQTT TLS client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
			logger.Info("loaded MQTT TLS client certificate", "certFile", cfg.MQTT.TLS.CertFile)
		}

		// Load CA certificate if provided
		if cfg.MQTT.TLS.CAFile != "" {
			caCert, err := os.ReadFile(cfg.MQTT.TLS.CAFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read MQTT CA certificate: %w", err)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse MQTT CA certificate")
			}
			tlsConfig.RootCAs = caCertPool
			logger.Info("loaded MQTT TLS CA certificate", "caFile", cfg.MQTT.TLS.CAFile)
		}

		opts.SetTLSConfig(tlsConfig)
	}

	// Connection callbacks
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		logger.Info("MQTT subscriber connected", "broker", cfg.MQTT.Broker, "clientId", clientID)
	})

	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		logger.Error("MQTT subscriber connection lost", "error", err, "clientId", clientID)
	})

	client := mqtt.NewClient(opts)
	
	// Connect to the broker
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("failed to connect MQTT subscriber to %s: %w", cfg.MQTT.Broker, token.Error())
	}

	return &MQTTSubscriber{
		client:        client,
		config:        cfg,
		logger:        logger,
		subscriptions: make(map[string]chan *message.Message),
	}, nil
}

// Subscribe implements the Watermill Subscriber interface
func (s *MQTTSubscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.client.IsConnected() {
		return nil, fmt.Errorf("MQTT subscriber not connected to broker")
	}

	// Check if already subscribed to this topic
	if _, exists := s.subscriptions[topic]; exists {
		return nil, fmt.Errorf("already subscribed to topic: %s", topic)
	}

	output := make(chan *message.Message)
	s.subscriptions[topic] = output

	handler := func(client mqtt.Client, mqttMsg mqtt.Message) {
		msg := message.NewMessage(watermill.NewUUID(), mqttMsg.Payload())
		msg.SetContext(ctx)
		msg.Metadata.Set("topic", mqttMsg.Topic())
		msg.Metadata.Set("qos", fmt.Sprintf("%d", mqttMsg.Qos()))
		msg.Metadata.Set("retained", fmt.Sprintf("%t", mqttMsg.Retained()))

		select {
		case output <- msg:
			s.logger.Debug("delivered MQTT message to channel",
				"topic", mqttMsg.Topic(),
				"uuid", msg.UUID,
				"payloadSize", len(mqttMsg.Payload()),
				"qos", mqttMsg.Qos())
		case <-ctx.Done():
			s.logger.Debug("context cancelled, stopping message delivery",
				"topic", mqttMsg.Topic())
			return
		}
	}

	token := s.client.Subscribe(topic, s.config.MQTT.QoS, handler)
	if token.Wait() && token.Error() != nil {
		delete(s.subscriptions, topic)
		close(output)
		return nil, fmt.Errorf("failed to subscribe to MQTT topic %s: %w", topic, token.Error())
	}

	s.logger.Info("subscribed to MQTT topic", 
		"topic", topic, 
		"qos", s.config.MQTT.QoS,
		"broker", s.config.MQTT.Broker)

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		
		if s.client.IsConnected() {
			s.client.Unsubscribe(topic)
		}
		
		if ch, exists := s.subscriptions[topic]; exists {
			close(ch)
			delete(s.subscriptions, topic)
		}
		
		s.logger.Info("unsubscribed from MQTT topic", "topic", topic)
	}()

	return output, nil
}

// Close closes the MQTT subscriber connection
func (s *MQTTSubscriber) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Unsubscribe from all topics and close channels
	for topic, ch := range s.subscriptions {
		if s.client.IsConnected() {
			s.client.Unsubscribe(topic)
		}
		close(ch)
		s.logger.Debug("unsubscribed and closed channel", "topic", topic)
	}
	s.subscriptions = make(map[string]chan *message.Message)

	// Disconnect from broker
	if s.client.IsConnected() {
		s.client.Disconnect(250)
		s.logger.Info("MQTT subscriber disconnected")
	}
	
	return nil
}

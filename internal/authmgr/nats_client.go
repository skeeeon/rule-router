// file: internal/authmgr/nats_client.go

package authmgr

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
)

// Timeout and retry constants for NATS client operations
const (
	// natsKVOperationTimeout is the maximum time for KV store operations
	natsKVOperationTimeout = 10 * time.Second

	// natsReconnectWait is the delay between NATS reconnection attempts
	natsReconnectWait = 50 * time.Millisecond
)

// NATSClient provides minimal NATS KV write functionality
// No subscriptions, no consumers, no streams - just connect and write to KV bucket
type NATSClient struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	kv     jetstream.KeyValue
	logger *logger.Logger
	config *NATSConfig
}

// NewNATSClient creates a NATS client and opens KV bucket
func NewNATSClient(cfg *NATSConfig, storageConfig *StorageConfig, log *logger.Logger) (*NATSClient, error) {
	log.Info("connecting to NATS", "urls", cfg.URLs)

	// Build connection options (same pattern as broker package)
	opts, err := buildNATSOptions(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("failed to build NATS options: %w", err)
	}

	// Connect to NATS
	urlString := strings.Join(cfg.URLs, ",")
	nc, err := nats.Connect(urlString, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	connectedURL := nc.ConnectedUrl()
	log.Info("NATS connection established", "connectedURL", connectedURL)

	// Create JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to create JetStream: %w", err)
	}

	// Open KV bucket (must exist - fail fast if not)
	ctx, cancel := context.WithTimeout(context.Background(), natsKVOperationTimeout)
	defer cancel()

	kv, err := js.KeyValue(ctx, storageConfig.Bucket)
	if err != nil {
		nc.Close()
		if err == jetstream.ErrBucketNotFound {
			return nil, fmt.Errorf("KV bucket '%s' not found. Create it with: nats kv add %s",
				storageConfig.Bucket, storageConfig.Bucket)
		}
		return nil, fmt.Errorf("failed to open KV bucket '%s': %w", storageConfig.Bucket, err)
	}

	log.Info("KV bucket opened successfully", "bucket", storageConfig.Bucket)

	return &NATSClient{
		conn:   nc,
		js:     js,
		kv:     kv,
		logger: log,
		config: cfg,
	}, nil
}

// StoreToken writes a token to the KV bucket
func (c *NATSClient) StoreToken(ctx context.Context, key, token string) error {
	c.logger.Debug("storing token in KV", "key", key)

	_, err := c.kv.Put(ctx, key, []byte(token))
	if err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	c.logger.Debug("token stored successfully", "key", key)
	return nil
}

// Close gracefully closes the NATS connection
func (c *NATSClient) Close() error {
	c.logger.Info("closing NATS connection")

	if err := c.conn.Drain(); err != nil {
		return fmt.Errorf("failed to drain connection: %w", err)
	}

	c.logger.Info("NATS connection closed")
	return nil
}

// buildNATSOptions creates NATS connection options with auth and TLS
// This function is adapted from broker package
func buildNATSOptions(cfg *NATSConfig, log *logger.Logger) ([]nats.Option, error) {
	var opts []nats.Option

	// Connection handlers
	opts = append(opts,
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			log.Error("NATS connection closed", "error", nc.LastError())
		}),
		nats.MaxReconnects(-1), // Unlimited reconnects
		nats.ReconnectWait(natsReconnectWait),
	)

	// Authentication (choose one method)
	if cfg.CredsFile != "" {
		log.Info("using NATS creds file authentication", "credsFile", cfg.CredsFile)
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	} else if cfg.NKey != "" {
		log.Info("using NATS NKey authentication")
		opts = append(opts, nats.Nkey(cfg.NKey, nil))
	} else if cfg.Token != "" {
		log.Info("using NATS token authentication")
		opts = append(opts, nats.Token(cfg.Token))
	} else if cfg.Username != "" {
		log.Info("using NATS username/password authentication", "username", cfg.Username)
		opts = append(opts, nats.UserInfo(cfg.Username, cfg.Password))
	}

	// TLS configuration
	if cfg.TLS.Enable {
		log.Info("enabling TLS", "insecure", cfg.TLS.Insecure)

		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.TLS.Insecure,
		}

		if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load TLS cert/key: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
			log.Info("loaded TLS client certificate", "certFile", cfg.TLS.CertFile)
		}

		if cfg.TLS.CAFile != "" {
			opts = append(opts, nats.RootCAs(cfg.TLS.CAFile))
			log.Info("loaded TLS CA certificate", "caFile", cfg.TLS.CAFile)
		}

		opts = append(opts, nats.Secure(tlsConfig))
	}

	return opts, nil
}


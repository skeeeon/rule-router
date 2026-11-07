package authmgr

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nats.go/micro"
	"rule-router/internal/logger"
	"rule-router/internal/natsservice"
)

// serviceName is what every auth-manager registers as on NATS micro discovery,
// so `nats micro info nats-auth-manager` shows every running instance. Two
// instances on one bucket both refresh every token, and a provider that
// revokes the previous token on issue makes them invalidate each other.
const serviceName = "nats-auth-manager"

// Timeout and retry constants for NATS client operations
const (
	// natsKVOperationTimeout is the maximum time for KV store operations
	natsKVOperationTimeout = 10 * time.Second

	// natsReconnectWait is the delay between NATS reconnection attempts
	natsReconnectWait = 50 * time.Millisecond
)

// NATSClient provides minimal NATS KV write functionality. No consumers, no
// streams - just connect and write to the KV bucket, plus an identity-only
// discovery registration (see RegisterService).
type NATSClient struct {
	conn    *nats.Conn
	js      jetstream.JetStream
	kv      jetstream.KeyValue
	bucket  string
	service micro.Service
	logger  *logger.Logger
	config  *NATSConfig
}

// NewNATSClient creates a NATS client and opens KV bucket
func NewNATSClient(cfg *NATSConfig, storageConfig *StorageConfig, log *logger.Logger) (*NATSClient, error) {
	log = log.With("component", "auth-manager")
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
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, fmt.Errorf("KV bucket %q not found. Create it with: nats kv add %s",
				storageConfig.Bucket, storageConfig.Bucket)
		}
		return nil, fmt.Errorf("failed to open KV bucket %q: %w", storageConfig.Bucket, err)
	}

	log.Info("KV bucket opened successfully", "bucket", storageConfig.Bucket)

	return &NATSClient{
		conn:   nc,
		js:     js,
		kv:     kv,
		bucket: storageConfig.Bucket,
		logger: log,
		config: cfg,
	}, nil
}

// RegisterService announces this process on the NATS micro discovery protocol
// ($SRV.PING, $SRV.INFO, $SRV.STATS) with its version, host, bucket and the KV
// keys it keeps fresh. Identity only, no endpoints.
//
// Never fatal: token refresh must not depend on discovery, so a failure is a
// warning. A credential refused $SRV.> fails asynchronously instead, through
// handleAsyncError. Close stops the service.
func (c *NATSClient) RegisterService(version string, kvKeys []string) {
	svc, err := natsservice.Add(c.conn, serviceName, version,
		"Keeps API tokens fresh in a NATS KV bucket",
		map[string]string{
			"bucket": c.bucket,
			"kvKeys": strings.Join(kvKeys, ","),
		})
	if err != nil {
		c.logger.Warn("NATS service discovery registration failed, continuing without it", "error", err)
		return
	}
	c.service = svc
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

	if c.service != nil {
		if err := c.service.Stop(); err != nil {
			c.logger.Warn("failed to stop NATS service discovery", "error", err)
		}
	}

	if err := c.conn.Drain(); err != nil {
		return fmt.Errorf("failed to drain connection: %w", err)
	}

	c.logger.Info("NATS connection closed")
	return nil
}

// handleAsyncError logs errors reported outside any single request, chiefly a
// permissions violation such as a credential refused $SRV.>. Without a handler
// nats.go discards them, so a broken discovery registration would be silent.
func handleAsyncError(log *logger.Logger, sub *nats.Subscription, err error) {
	// nats.go passes a nil subscription for a permissions violation and runs
	// this callback with no recover, so an unguarded sub.Subject would kill
	// the process.
	subject := ""
	if sub != nil {
		subject = sub.Subject
	}
	log.Error("NATS asynchronous error", "subject", subject, "error", err)
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
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			handleAsyncError(log, sub, err)
		}),
		nats.MaxReconnects(-1), // Unlimited reconnects
		nats.ReconnectWait(natsReconnectWait),
	)

	// Authentication (choose one method)
	if cfg.CredsFile != "" {
		log.Info("using NATS creds file authentication", "credsFile", cfg.CredsFile)
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	} else if cfg.NKeySeedFile != "" {
		log.Info("using NATS NKey authentication", "seedFile", cfg.NKeySeedFile)
		nkeyOpt, err := nats.NkeyOptionFromSeed(cfg.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load NKey seed file: %w", err)
		}
		opts = append(opts, nkeyOpt)
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

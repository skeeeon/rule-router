// file: config/config.go

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"gopkg.in/yaml.v3"
)

// Config represents the unified configuration for both rule-router and http-gateway
type Config struct {
	NATS     NATSConfig     `json:"nats" yaml:"nats"`
	HTTP     HTTPConfig     `json:"http,omitempty" yaml:"http,omitempty"` // Optional: Only used by http-gateway
	Logging  LogConfig      `json:"logging" yaml:"logging"`
	Metrics  MetricsConfig  `json:"metrics" yaml:"metrics"`
	KV       KVConfig       `json:"kv" yaml:"kv"`
	Security SecurityConfig `json:"security" yaml:"security"`
}

// HTTPConfig contains HTTP server and client configuration for http-gateway
type HTTPConfig struct {
	Server HTTPServerConfig `json:"server" yaml:"server"`
	Client HTTPClientConfig `json:"client" yaml:"client"`
}

// HTTPServerConfig configures the inbound HTTP server
type HTTPServerConfig struct {
	Address             string        `json:"address" yaml:"address"`
	ReadTimeout         time.Duration `json:"readTimeout" yaml:"readTimeout"`
	WriteTimeout        time.Duration `json:"writeTimeout" yaml:"writeTimeout"`
	IdleTimeout         time.Duration `json:"idleTimeout" yaml:"idleTimeout"`
	MaxHeaderBytes      int           `json:"maxHeaderBytes" yaml:"maxHeaderBytes"`
	ShutdownGracePeriod time.Duration `json:"shutdownGracePeriod" yaml:"shutdownGracePeriod"`
}

// HTTPClientConfig configures the outbound HTTP client
type HTTPClientConfig struct {
	Timeout             time.Duration `json:"timeout" yaml:"timeout"`
	MaxIdleConns        int           `json:"maxIdleConns" yaml:"maxIdleConns"`
	MaxIdleConnsPerHost int           `json:"maxIdleConnsPerHost" yaml:"maxIdleConnsPerHost"`
	IdleConnTimeout     time.Duration `json:"idleConnTimeout" yaml:"idleConnTimeout"`
}

// NATSConfig contains NATS connection and JetStream configuration
type NATSConfig struct {
	URLs     []string `json:"urls" yaml:"urls"`
	Username string   `json:"username" yaml:"username"`
	Password string   `json:"password" yaml:"password"`
	Token    string   `json:"token" yaml:"token"`
	
	// NATS-specific authentication
	NKey      string `json:"nkey" yaml:"nkey"`
	CredsFile string `json:"credsFile" yaml:"credsFile"`
	
	TLS struct {
		Enable   bool   `json:"enable" yaml:"enable"`
		CertFile string `json:"certFile" yaml:"certFile"`
		KeyFile  string `json:"keyFile" yaml:"keyFile"`
		CAFile   string `json:"caFile" yaml:"caFile"`
		Insecure bool   `json:"insecure" yaml:"insecure"`
	} `json:"tls" yaml:"tls"`
	
	Consumers  ConsumerConfig   `json:"consumers" yaml:"consumers"`
	Connection ConnectionConfig `json:"connection" yaml:"connection"`
	Publish    PublishConfig    `json:"publish" yaml:"publish"`
}

// ConsumerConfig contains JetStream consumer configuration
type ConsumerConfig struct {
	SubscriberCount int           `json:"subscriberCount" yaml:"subscriberCount"`
	FetchBatchSize  int           `json:"fetchBatchSize" yaml:"fetchBatchSize"`
	FetchTimeout    time.Duration `json:"fetchTimeout" yaml:"fetchTimeout"`
	MaxAckPending   int           `json:"maxAckPending" yaml:"maxAckPending"`
	AckWaitTimeout  time.Duration `json:"ackWaitTimeout" yaml:"ackWaitTimeout"`
	MaxDeliver      int           `json:"maxDeliver" yaml:"maxDeliver"`
	DeliverPolicy   string        `json:"deliverPolicy" yaml:"deliverPolicy"`
	ReplayPolicy    string        `json:"replayPolicy" yaml:"replayPolicy"`
}

// ConnectionConfig contains NATS connection settings
type ConnectionConfig struct {
	MaxReconnects int           `json:"maxReconnects" yaml:"maxReconnects"`
	ReconnectWait time.Duration `json:"reconnectWait" yaml:"reconnectWait"`
}

// PublishConfig contains NATS publish configuration
type PublishConfig struct {
	Mode           string        `json:"mode" yaml:"mode"`
	AckTimeout     time.Duration `json:"ackTimeout" yaml:"ackTimeout"`
	MaxRetries     int           `json:"maxRetries" yaml:"maxRetries"`
	RetryBaseDelay time.Duration `json:"retryBaseDelay" yaml:"retryBaseDelay"`
}

// LogConfig contains logging configuration
type LogConfig struct {
	Level      string `json:"level" yaml:"level"`
	Encoding   string `json:"encoding" yaml:"encoding"`
	OutputPath string `json:"outputPath" yaml:"outputPath"`
}

// MetricsConfig contains metrics server configuration
type MetricsConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	Address        string `json:"address" yaml:"address"`
	Path           string `json:"path" yaml:"path"`
	UpdateInterval string `json:"updateInterval" yaml:"updateInterval"`
}

// KVConfig contains Key-Value store configuration
type KVConfig struct {
	Enabled    bool     `json:"enabled" yaml:"enabled"`
	Buckets    []string `json:"buckets" yaml:"buckets"`
	LocalCache struct {
		Enabled bool `json:"enabled" yaml:"enabled"`
	} `json:"localCache" yaml:"localCache"`
}

// SecurityConfig contains security-related configuration
type SecurityConfig struct {
	Verification VerificationConfig `json:"verification" yaml:"verification"`
}

// VerificationConfig contains signature verification settings
type VerificationConfig struct {
	Enabled           bool   `json:"enabled" yaml:"enabled"`
	PublicKeyHeader   string `json:"publicKeyHeader" yaml:"publicKeyHeader"`
	SignatureHeader   string `json:"signatureHeader" yaml:"signatureHeader"`
}

// Load reads and parses configuration from a file (JSON or YAML)
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config

	ext := strings.ToLower(filepath.Ext(path))
	var parseErr error

	switch ext {
	case ".yaml", ".yml":
		parseErr = yaml.Unmarshal(data, &config)
	case ".json":
		parseErr = json.Unmarshal(data, &config)
	default:
		// Try JSON first, then YAML
		parseErr = json.Unmarshal(data, &config)
		if parseErr != nil {
			yamlErr := yaml.Unmarshal(data, &config)
			if yamlErr != nil {
				return nil, fmt.Errorf("failed to parse config (tried JSON and YAML): %w", parseErr)
			}
			parseErr = nil
		}
	}

	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", parseErr)
	}

	setDefaults(&config)

	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// setDefaults applies default values to the configuration
func setDefaults(cfg *Config) {
	// NATS defaults
	if len(cfg.NATS.URLs) == 0 {
		cfg.NATS.URLs = []string{"nats://localhost:4222"}
	}
	if cfg.NATS.Connection.MaxReconnects == 0 {
		cfg.NATS.Connection.MaxReconnects = -1
	}
	if cfg.NATS.Connection.ReconnectWait == 0 {
		cfg.NATS.Connection.ReconnectWait = 50 * time.Millisecond
	}

	// Consumer defaults
	if cfg.NATS.Consumers.SubscriberCount == 0 {
		cfg.NATS.Consumers.SubscriberCount = 2
	}
	if cfg.NATS.Consumers.FetchBatchSize == 0 {
		cfg.NATS.Consumers.FetchBatchSize = 1
	}
	if cfg.NATS.Consumers.FetchTimeout == 0 {
		cfg.NATS.Consumers.FetchTimeout = 5 * time.Second
	}
	if cfg.NATS.Consumers.AckWaitTimeout == 0 {
		cfg.NATS.Consumers.AckWaitTimeout = 30 * time.Second
	}
	if cfg.NATS.Consumers.MaxDeliver == 0 {
		cfg.NATS.Consumers.MaxDeliver = 3
	}
	if cfg.NATS.Consumers.MaxAckPending == 0 {
		cfg.NATS.Consumers.MaxAckPending = 1000
	}
	if cfg.NATS.Consumers.DeliverPolicy == "" {
		cfg.NATS.Consumers.DeliverPolicy = "new"
	}
	if cfg.NATS.Consumers.ReplayPolicy == "" {
		cfg.NATS.Consumers.ReplayPolicy = "instant"
	}

	// Publish defaults
	if cfg.NATS.Publish.Mode == "" {
		cfg.NATS.Publish.Mode = "jetstream"
	}
	if cfg.NATS.Publish.AckTimeout == 0 {
		cfg.NATS.Publish.AckTimeout = 5 * time.Second
	}
	if cfg.NATS.Publish.MaxRetries == 0 {
		cfg.NATS.Publish.MaxRetries = 3
	}
	if cfg.NATS.Publish.RetryBaseDelay == 0 {
		cfg.NATS.Publish.RetryBaseDelay = 50 * time.Millisecond
	}

	// HTTP Server defaults (only for http-gateway)
	if cfg.HTTP.Server.Address == "" {
		cfg.HTTP.Server.Address = ":8080"
	}
	if cfg.HTTP.Server.ReadTimeout == 0 {
		cfg.HTTP.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.HTTP.Server.WriteTimeout == 0 {
		cfg.HTTP.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.HTTP.Server.IdleTimeout == 0 {
		cfg.HTTP.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.HTTP.Server.MaxHeaderBytes == 0 {
		cfg.HTTP.Server.MaxHeaderBytes = 1 << 20 // 1MB
	}
	if cfg.HTTP.Server.ShutdownGracePeriod == 0 {
		cfg.HTTP.Server.ShutdownGracePeriod = 30 * time.Second
	}

	// HTTP Client defaults (only for http-gateway)
	if cfg.HTTP.Client.Timeout == 0 {
		cfg.HTTP.Client.Timeout = 30 * time.Second
	}
	if cfg.HTTP.Client.MaxIdleConns == 0 {
		cfg.HTTP.Client.MaxIdleConns = 100
	}
	if cfg.HTTP.Client.MaxIdleConnsPerHost == 0 {
		cfg.HTTP.Client.MaxIdleConnsPerHost = 10
	}
	if cfg.HTTP.Client.IdleConnTimeout == 0 {
		cfg.HTTP.Client.IdleConnTimeout = 90 * time.Second
	}

	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Encoding == "" {
		cfg.Logging.Encoding = "json"
	}
	if cfg.Logging.OutputPath == "" {
		cfg.Logging.OutputPath = "stdout"
	}

	// Metrics defaults
	if cfg.Metrics.Address == "" {
		cfg.Metrics.Address = ":2112"
	}
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}
	if cfg.Metrics.UpdateInterval == "" {
		cfg.Metrics.UpdateInterval = "15s"
	}

	// KV defaults
	if cfg.KV.Enabled {
		cfg.KV.LocalCache.Enabled = true
	}

	// Security defaults
	if cfg.Security.Verification.PublicKeyHeader == "" {
		cfg.Security.Verification.PublicKeyHeader = "Nats-Public-Key"
	}
	if cfg.Security.Verification.SignatureHeader == "" {
		cfg.Security.Verification.SignatureHeader = "Nats-Signature"
	}
}

// validateConfig validates the configuration
func validateConfig(cfg *Config) error {
	// NATS validation
	if len(cfg.NATS.URLs) == 0 {
		return fmt.Errorf("at least one NATS URL must be specified")
	}

	// Authentication validation
	authCount := 0
	if cfg.NATS.Username != "" {
		authCount++
	}
	if cfg.NATS.Token != "" {
		authCount++
	}
	if cfg.NATS.NKey != "" {
		authCount++
	}
	if cfg.NATS.CredsFile != "" {
		authCount++
	}
	if authCount > 1 {
		return fmt.Errorf("only one NATS authentication method should be specified")
	}

	// TLS validation
	if cfg.NATS.TLS.Enable {
		if cfg.NATS.TLS.CertFile == "" {
			return fmt.Errorf("NATS TLS cert file is required when TLS is enabled")
		}
		if cfg.NATS.TLS.KeyFile == "" {
			return fmt.Errorf("NATS TLS key file is required when TLS is enabled")
		}
		if cfg.NATS.TLS.CAFile == "" {
			return fmt.Errorf("NATS TLS CA file is required when TLS is enabled")
		}
	}

	if cfg.NATS.CredsFile != "" {
		if _, err := os.Stat(cfg.NATS.CredsFile); os.IsNotExist(err) {
			return fmt.Errorf("NATS creds file does not exist: %s", cfg.NATS.CredsFile)
		}
	}

	// Consumer validation
	if cfg.NATS.Consumers.SubscriberCount < 1 {
		return fmt.Errorf("subscriber count must be at least 1")
	}
	if cfg.NATS.Consumers.FetchBatchSize < 1 {
		return fmt.Errorf("fetch batch size must be at least 1")
	}
	if cfg.NATS.Consumers.FetchTimeout <= 0 {
		return fmt.Errorf("fetch timeout must be positive")
	}
	if cfg.NATS.Consumers.MaxAckPending < 1 {
		return fmt.Errorf("max ack pending must be at least 1")
	}
	if cfg.NATS.Consumers.MaxDeliver < 1 {
		return fmt.Errorf("max deliver must be at least 1")
	}

	validDeliverPolicies := map[string]bool{
		"all":                true,
		"new":                true,
		"last":               true,
		"by_start_time":      true,
		"by_start_sequence":  true,
	}
	if !validDeliverPolicies[cfg.NATS.Consumers.DeliverPolicy] {
		return fmt.Errorf("invalid deliver policy: %s (valid options: all, new, last, by_start_time, by_start_sequence)",
			cfg.NATS.Consumers.DeliverPolicy)
	}

	validReplayPolicies := map[string]bool{
		"instant":  true,
		"original": true,
	}
	if !validReplayPolicies[cfg.NATS.Consumers.ReplayPolicy] {
		return fmt.Errorf("invalid replay policy: %s (valid options: instant, original)",
			cfg.NATS.Consumers.ReplayPolicy)
	}

	// Publish validation
	if cfg.NATS.Publish.Mode != "jetstream" && cfg.NATS.Publish.Mode != "core" {
		return fmt.Errorf("publish mode must be 'jetstream' or 'core', got: %s", cfg.NATS.Publish.Mode)
	}

	validLogLevels := map[string]bool{
		"debug": true, "info": true, "warn": true, "error": true,
	}
	if !validLogLevels[cfg.Logging.Level] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", cfg.Logging.Level)
	}

	// HTTP-specific validation (only when HTTP is configured)
	if cfg.HTTP.Server.Address != "" {
		if cfg.HTTP.Server.ReadTimeout < 0 {
			return fmt.Errorf("HTTP server read timeout cannot be negative")
		}
		if cfg.HTTP.Client.Timeout < 0 {
			return fmt.Errorf("HTTP client timeout cannot be negative")
		}
	}

	return nil
}

// ApplyOverrides applies command-line overrides to metrics configuration
func (c *Config) ApplyOverrides(metricsAddr, metricsPath string, metricsInterval time.Duration) {
	if metricsAddr != "" {
		c.Metrics.Address = metricsAddr
	}
	if metricsPath != "" {
		c.Metrics.Path = metricsPath
	}
	if metricsInterval > 0 {
		c.Metrics.UpdateInterval = metricsInterval.String()
	}
}

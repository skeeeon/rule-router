//file: config/config.go

package config

import (
	json "github.com/goccy/go-json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	NATS    NATSConfig    `json:"nats" yaml:"nats"`
	Logging LogConfig     `json:"logging" yaml:"logging"`
	Metrics MetricsConfig `json:"metrics" yaml:"metrics"`
	KV      KVConfig      `json:"kv" yaml:"kv"`
}

type NATSConfig struct {
	URLs     []string `json:"urls" yaml:"urls"`
	Username string   `json:"username" yaml:"username"`
	Password string   `json:"password" yaml:"password"`
	Token    string   `json:"token" yaml:"token"`
	
	// NATS-specific authentication
	NKey      string `json:"nkey" yaml:"nkey"`           // NKey for JWT authentication
	CredsFile string `json:"credsFile" yaml:"credsFile"` // Path to .creds file
	
	TLS struct {
		Enable   bool   `json:"enable" yaml:"enable"`
		CertFile string `json:"certFile" yaml:"certFile"`
		KeyFile  string `json:"keyFile" yaml:"keyFile"`
		CAFile   string `json:"caFile" yaml:"caFile"`
		Insecure bool   `json:"insecure" yaml:"insecure"` // Skip certificate verification
	} `json:"tls" yaml:"tls"`
	
	// JetStream consumer configuration
	Consumers ConsumerConfig `json:"consumers" yaml:"consumers"`
	
	// NATS connection behavior
	Connection ConnectionConfig `json:"connection" yaml:"connection"`
}

// ConsumerConfig defines JetStream consumer behavior and performance settings
type ConsumerConfig struct {
	// Performance tuning
	SubscriberCount int           `json:"subscriberCount" yaml:"subscriberCount"` // Concurrent workers per subscription
	FetchBatchSize  int           `json:"fetchBatchSize" yaml:"fetchBatchSize"`   // Messages to fetch per pull request
	FetchTimeout    time.Duration `json:"fetchTimeout" yaml:"fetchTimeout"`       // Max wait time when fetching messages
	MaxAckPending   int           `json:"maxAckPending" yaml:"maxAckPending"`     // Max unacknowledged messages
	
	// Reliability settings
	AckWaitTimeout time.Duration `json:"ackWaitTimeout" yaml:"ackWaitTimeout"` // Time to wait for ack before redelivery
	MaxDeliver     int           `json:"maxDeliver" yaml:"maxDeliver"`         // Max redelivery attempts
	
	// Delivery behavior
	DeliverPolicy string `json:"deliverPolicy" yaml:"deliverPolicy"` // all, new, last, by_start_time, by_start_sequence
	ReplayPolicy  string `json:"replayPolicy" yaml:"replayPolicy"`   // instant, original
}

// ConnectionConfig defines NATS connection behavior
type ConnectionConfig struct {
	MaxReconnects int           `json:"maxReconnects" yaml:"maxReconnects"` // -1 for unlimited
	ReconnectWait time.Duration `json:"reconnectWait" yaml:"reconnectWait"` // Wait time between reconnect attempts
}

// KVConfig configures NATS Key-Value store access with local caching support
type KVConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Buckets []string `json:"buckets" yaml:"buckets"`
	
	// Local cache configuration for performance optimization
	LocalCache struct {
		Enabled bool `json:"enabled" yaml:"enabled"` // Enable/disable local KV caching
	} `json:"localCache" yaml:"localCache"`
}

type LogConfig struct {
	Level      string `json:"level" yaml:"level"`           // debug, info, warn, error
	OutputPath string `json:"outputPath" yaml:"outputPath"` // file path or "stdout"
	Encoding   string `json:"encoding" yaml:"encoding"`     // json or console
}

type MetricsConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	Address        string `json:"address" yaml:"address"`
	Path           string `json:"path" yaml:"path"`
	UpdateInterval string `json:"updateInterval" yaml:"updateInterval"` // Duration string
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	
	// Determine file type by extension
	ext := strings.ToLower(filepath.Ext(path))
	var parseErr error
	
	switch ext {
	case ".yaml", ".yml":
		parseErr = yaml.Unmarshal(data, &config)
	case ".json":
		parseErr = json.Unmarshal(data, &config)
	default:
		// Try JSON first, then YAML if JSON fails
		parseErr = json.Unmarshal(data, &config)
		if parseErr != nil {
			yamlErr := yaml.Unmarshal(data, &config)
			if yamlErr != nil {
				return nil, fmt.Errorf("failed to parse config file (tried JSON and YAML): %w", parseErr)
			}
			parseErr = nil
		}
	}
	
	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", parseErr)
	}

	// Set defaults
	setDefaults(&config)

	// Validate the configuration
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// setDefaults sets default values for configuration
func setDefaults(cfg *Config) {
	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.OutputPath == "" {
		cfg.Logging.OutputPath = "stdout"
	}
	if cfg.Logging.Encoding == "" {
		cfg.Logging.Encoding = "json"
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

	// NATS defaults
	if len(cfg.NATS.URLs) == 0 {
		cfg.NATS.URLs = []string{"nats://localhost:4222"}
	}

	// NATS Connection defaults
	if cfg.NATS.Connection.MaxReconnects == 0 {
		cfg.NATS.Connection.MaxReconnects = -1 // Unlimited
	}
	if cfg.NATS.Connection.ReconnectWait == 0 {
		cfg.NATS.Connection.ReconnectWait = 50 * time.Millisecond
	}

	// Consumer defaults
	if cfg.NATS.Consumers.SubscriberCount == 0 {
		cfg.NATS.Consumers.SubscriberCount = 2
	}
	if cfg.NATS.Consumers.FetchBatchSize == 0 {
		cfg.NATS.Consumers.FetchBatchSize = 1 // Conservative default
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
		cfg.NATS.Consumers.DeliverPolicy = "all"
	}
	if cfg.NATS.Consumers.ReplayPolicy == "" {
		cfg.NATS.Consumers.ReplayPolicy = "instant"
	}

	// KV defaults - disabled by default, no buckets
	// KV Local Cache defaults - enabled by default when KV is enabled
	// Validation will handle setting the default to true if KV is enabled
}

// validateConfig performs validation of all configuration values
func validateConfig(cfg *Config) error {
	// Validate NATS configuration
	if len(cfg.NATS.URLs) == 0 {
		return fmt.Errorf("at least one NATS server URL is required")
	}

	// Validate authentication options are not conflicting
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

	// Validate NATS TLS config if enabled
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

	// Validate .creds file exists if specified
	if cfg.NATS.CredsFile != "" {
		if _, err := os.Stat(cfg.NATS.CredsFile); os.IsNotExist(err) {
			return fmt.Errorf("NATS creds file does not exist: %s", cfg.NATS.CredsFile)
		}
	}

	// Validate consumer configuration
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
	
	// Validate deliver policy
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
	
	// Validate replay policy
	validReplayPolicies := map[string]bool{
		"instant":  true,
		"original": true,
	}
	if !validReplayPolicies[cfg.NATS.Consumers.ReplayPolicy] {
		return fmt.Errorf("invalid replay policy: %s (valid options: instant, original)", 
			cfg.NATS.Consumers.ReplayPolicy)
	}

	// Validate logging config
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level: %s", cfg.Logging.Level)
	}

	switch cfg.Logging.Encoding {
	case "json", "console":
	default:
		return fmt.Errorf("invalid log encoding: %s", cfg.Logging.Encoding)
	}

	// Validate metrics config
	if cfg.Metrics.Enabled {
		if _, err := time.ParseDuration(cfg.Metrics.UpdateInterval); err != nil {
			return fmt.Errorf("invalid metrics update interval: %w", err)
		}
	}

	// Validate KV config
	if cfg.KV.Enabled {
		if len(cfg.KV.Buckets) == 0 {
			return fmt.Errorf("KV enabled but no buckets configured")
		}
		
		// Validate bucket names
		for _, bucket := range cfg.KV.Buckets {
			if err := validateBucketName(bucket); err != nil {
				return fmt.Errorf("invalid KV bucket name '%s': %w", bucket, err)
			}
		}
		
		// Check for duplicate bucket names
		bucketMap := make(map[string]bool)
		for _, bucket := range cfg.KV.Buckets {
			if bucketMap[bucket] {
				return fmt.Errorf("duplicate KV bucket name: %s", bucket)
			}
			bucketMap[bucket] = true
		}
		
		// Set local cache default to enabled if not explicitly configured
		if !isLocalCacheExplicitlyConfigured(cfg) {
			cfg.KV.LocalCache.Enabled = true
		}
	}

	return nil
}

// isLocalCacheExplicitlyConfigured checks if the user explicitly set localCache.enabled
func isLocalCacheExplicitlyConfigured(cfg *Config) bool {
	if !cfg.KV.Enabled {
		return true
	}
	return false
}

// validateBucketName validates NATS KV bucket naming rules
func validateBucketName(name string) error {
	if name == "" {
		return fmt.Errorf("bucket name cannot be empty")
	}
	
	if len(name) > 64 {
		return fmt.Errorf("bucket name too long (max 64 characters)")
	}
	
	// NATS bucket names: letters, numbers, dash, underscore
	for _, char := range name {
		if !((char >= 'a' && char <= 'z') || 
			 (char >= 'A' && char <= 'Z') || 
			 (char >= '0' && char <= '9') || 
			 char == '-' || char == '_') {
			return fmt.Errorf("bucket name contains invalid character '%c' (allowed: a-z, A-Z, 0-9, -, _)", char)
		}
	}
	
	return nil
}

// ApplyOverrides applies command line flag overrides to the configuration
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

//file: config/config.go

package config

import (
	json "github.com/goccy/go-json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	NATS       NATSConfig      `json:"nats" yaml:"nats"`
	Logging    LogConfig       `json:"logging" yaml:"logging"`
	Metrics    MetricsConfig   `json:"metrics" yaml:"metrics"`
	Watermill  WatermillConfig `json:"watermill" yaml:"watermill"`
	KV         KVConfig        `json:"kv" yaml:"kv"`
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
}

type WatermillConfig struct {
	// NATS JetStream configuration
	NATS struct {
		MaxReconnects    int           `json:"maxReconnects" yaml:"maxReconnects"`
		ReconnectWait    time.Duration `json:"reconnectWait" yaml:"reconnectWait"`
		PublishAsync     bool          `json:"publishAsync" yaml:"publishAsync"`
		MaxPendingAsync  int           `json:"maxPendingAsync" yaml:"maxPendingAsync"`
		SubscriberCount  int           `json:"subscriberCount" yaml:"subscriberCount"`
		AckWaitTimeout   time.Duration `json:"ackWaitTimeout" yaml:"ackWaitTimeout"`
		MaxDeliver       int           `json:"maxDeliver" yaml:"maxDeliver"`
		WriteBufferSize  int           `json:"writeBufferSize" yaml:"writeBufferSize"`
		ReconnectBufSize int           `json:"reconnectBufSize" yaml:"reconnectBufSize"`
	} `json:"nats" yaml:"nats"`
	
	// Router configuration
	Router struct {
		CloseTimeout time.Duration `json:"closeTimeout" yaml:"closeTimeout"`
	} `json:"router" yaml:"router"`
	
	// Middleware configuration
	Middleware struct {
		RetryMaxAttempts int           `json:"retryMaxAttempts" yaml:"retryMaxAttempts"`
		RetryInterval    time.Duration `json:"retryInterval" yaml:"retryInterval"`
		MetricsEnabled   bool          `json:"metricsEnabled" yaml:"metricsEnabled"`
		TracingEnabled   bool          `json:"tracingEnabled" yaml:"tracingEnabled"`
	} `json:"middleware" yaml:"middleware"`
}

// KVConfig configures NATS Key-Value store access
type KVConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Buckets []string `json:"buckets" yaml:"buckets"`
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

	// Watermill NATS defaults
	if cfg.Watermill.NATS.MaxReconnects == 0 {
		cfg.Watermill.NATS.MaxReconnects = -1 // Unlimited
	}
	if cfg.Watermill.NATS.ReconnectWait == 0 {
		cfg.Watermill.NATS.ReconnectWait = 50 * time.Millisecond
	}
	if cfg.Watermill.NATS.MaxPendingAsync == 0 {
		cfg.Watermill.NATS.MaxPendingAsync = 2000 // High throughput
	}
	if cfg.Watermill.NATS.SubscriberCount == 0 {
		cfg.Watermill.NATS.SubscriberCount = runtime.NumCPU() * 2
	}
	if cfg.Watermill.NATS.AckWaitTimeout == 0 {
		cfg.Watermill.NATS.AckWaitTimeout = 30 * time.Second
	}
	if cfg.Watermill.NATS.MaxDeliver == 0 {
		cfg.Watermill.NATS.MaxDeliver = 3
	}
	if cfg.Watermill.NATS.WriteBufferSize == 0 {
		cfg.Watermill.NATS.WriteBufferSize = 2 * 1024 * 1024 // 2MB
	}
	if cfg.Watermill.NATS.ReconnectBufSize == 0 {
		cfg.Watermill.NATS.ReconnectBufSize = 16 * 1024 * 1024 // 16MB
	}
	cfg.Watermill.NATS.PublishAsync = true // Default to async for performance

	// Router defaults
	if cfg.Watermill.Router.CloseTimeout == 0 {
		cfg.Watermill.Router.CloseTimeout = 30 * time.Second
	}

	// Middleware defaults
	if cfg.Watermill.Middleware.RetryMaxAttempts == 0 {
		cfg.Watermill.Middleware.RetryMaxAttempts = 3
	}
	if cfg.Watermill.Middleware.RetryInterval == 0 {
		cfg.Watermill.Middleware.RetryInterval = 100 * time.Millisecond
	}
	cfg.Watermill.Middleware.MetricsEnabled = true  // Default enabled

	// KV defaults - disabled by default, no buckets
	// cfg.KV.Enabled defaults to false
	// cfg.KV.Buckets defaults to empty slice
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

	// Validate Watermill config
	if cfg.Watermill.NATS.SubscriberCount < 1 {
		return fmt.Errorf("subscriber count must be greater than 0")
	}
	if cfg.Watermill.NATS.MaxPendingAsync < 100 {
		return fmt.Errorf("max pending async must be at least 100 for reasonable performance")
	}
	if cfg.Watermill.Middleware.RetryMaxAttempts < 0 {
		return fmt.Errorf("retry max attempts cannot be negative")
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
	}

	return nil
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

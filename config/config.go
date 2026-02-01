// file: config/config.go

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Default timeout and configuration constants
const (
	// DefaultNATSURL is the default NATS server URL
	DefaultNATSURL = "nats://localhost:4222"

	// DefaultReconnectWait is the default delay between NATS reconnection attempts
	DefaultReconnectWait = 50 * time.Millisecond

	// DefaultFetchTimeout is the default timeout for JetStream fetch operations
	DefaultFetchTimeout = 5 * time.Second

	// DefaultAckWaitTimeout is the default timeout for message acknowledgement
	DefaultAckWaitTimeout = 30 * time.Second

	// DefaultPublishAckTimeout is the default timeout for publish acknowledgement
	DefaultPublishAckTimeout = 5 * time.Second

	// DefaultRetryBaseDelay is the default base delay for publish retries
	DefaultRetryBaseDelay = 50 * time.Millisecond

	// DefaultHTTPReadTimeout is the default read timeout for HTTP server
	DefaultHTTPReadTimeout = 30 * time.Second

	// DefaultHTTPWriteTimeout is the default write timeout for HTTP server
	DefaultHTTPWriteTimeout = 30 * time.Second

	// DefaultHTTPIdleTimeout is the default idle timeout for HTTP server
	DefaultHTTPIdleTimeout = 120 * time.Second

	// DefaultHTTPShutdownGracePeriod is the default graceful shutdown period for HTTP server
	DefaultHTTPShutdownGracePeriod = 30 * time.Second

	// DefaultHTTPClientTimeout is the default timeout for HTTP client requests
	DefaultHTTPClientTimeout = 30 * time.Second

	// DefaultHTTPIdleConnTimeout is the default idle connection timeout for HTTP client
	DefaultHTTPIdleConnTimeout = 90 * time.Second

	// DefaultMaxHeaderBytes is the default maximum header size (1MB)
	DefaultMaxHeaderBytes = 1 << 20
)

// Default numeric configuration values
const (
	// DefaultWorkerCount is the default number of consumer workers
	DefaultWorkerCount = 2

	// DefaultFetchBatchSize is the default number of messages to fetch per batch
	DefaultFetchBatchSize = 1

	// DefaultMaxDeliver is the default maximum delivery attempts per message
	DefaultMaxDeliver = 3

	// DefaultMaxAckPending is the default maximum pending acknowledgements
	DefaultMaxAckPending = 1000

	// DefaultPublishMaxRetries is the default maximum publish retry attempts
	DefaultPublishMaxRetries = 3

	// DefaultMaxIdleConns is the default maximum idle HTTP connections
	DefaultMaxIdleConns = 100

	// DefaultMaxIdleConnsPerHost is the default maximum idle connections per host
	DefaultMaxIdleConnsPerHost = 10

	// DefaultForEachMaxIterations is the default maximum forEach iterations
	DefaultForEachMaxIterations = 100

	// DefaultInboundQueueSize is the default HTTP inbound queue size
	DefaultInboundQueueSize = 1000
)

// Validation limits
const (
	// MaxWorkerCount is the maximum allowed worker count
	MaxWorkerCount = 1000

	// MaxFetchBatchSize is the maximum allowed fetch batch size
	MaxFetchBatchSize = 10000

	// MaxAckPending is the maximum allowed pending acknowledgements
	MaxAckPending = 100000

	// MaxForEachIterations is the maximum allowed forEach iterations
	MaxForEachIterations = 10000

	// MaxInboundQueueSize is the maximum allowed inbound queue size
	MaxInboundQueueSize = 100000
)

// Default string configuration values
const (
	// DefaultConsumerPrefix is the default prefix for consumer names
	DefaultConsumerPrefix = "rule-router"

	// DefaultPublishMode is the default NATS publish mode
	DefaultPublishMode = "jetstream"

	// DefaultDeliverPolicy is the default consumer deliver policy
	DefaultDeliverPolicy = "new"

	// DefaultReplayPolicy is the default consumer replay policy
	DefaultReplayPolicy = "instant"

	// DefaultLogLevel is the default logging level
	DefaultLogLevel = "info"

	// DefaultLogEncoding is the default log encoding format
	DefaultLogEncoding = "json"

	// DefaultLogOutput is the default log output destination
	DefaultLogOutput = "stdout"

	// DefaultMetricsAddress is the default metrics server address
	DefaultMetricsAddress = ":2112"

	// DefaultMetricsPath is the default metrics endpoint path
	DefaultMetricsPath = "/metrics"

	// DefaultHTTPServerAddress is the default HTTP server address
	DefaultHTTPServerAddress = ":8080"

	// DefaultPublicKeyHeader is the default header for NKey public key
	DefaultPublicKeyHeader = "Nats-Public-Key"

	// DefaultSignatureHeader is the default header for NKey signature
	DefaultSignatureHeader = "Nats-Signature"
)

// Config represents the unified configuration for both rule-router and http-gateway
type Config struct {
	NATS     NATSConfig     `json:"nats" yaml:"nats" mapstructure:"nats"`
	HTTP     HTTPConfig     `json:"http,omitempty" yaml:"http,omitempty" mapstructure:"http"`
	Logging  LogConfig      `json:"logging" yaml:"logging" mapstructure:"logging"`
	Metrics  MetricsConfig  `json:"metrics" yaml:"metrics" mapstructure:"metrics"`
	KV       KVConfig       `json:"kv" yaml:"kv" mapstructure:"kv"`
	Security SecurityConfig `json:"security" yaml:"security" mapstructure:"security"`
	ForEach  ForEachConfig  `json:"forEach" yaml:"forEach" mapstructure:"forEach"`
}

// ForEachConfig contains configuration for array iteration operations
type ForEachConfig struct {
	MaxIterations int `json:"maxIterations" yaml:"maxIterations" mapstructure:"maxIterations"`
}

// HTTPConfig contains HTTP server and client configuration for http-gateway
type HTTPConfig struct {
	Server HTTPServerConfig `json:"server" yaml:"server" mapstructure:"server"`
	Client HTTPClientConfig `json:"client" yaml:"client" mapstructure:"client"`
}

// HTTPServerConfig configures the inbound HTTP server
type HTTPServerConfig struct {
	Address             string        `json:"address" yaml:"address" mapstructure:"address"`
	ReadTimeout         time.Duration `json:"readTimeout" yaml:"readTimeout" mapstructure:"readTimeout"`
	WriteTimeout        time.Duration `json:"writeTimeout" yaml:"writeTimeout" mapstructure:"writeTimeout"`
	IdleTimeout         time.Duration `json:"idleTimeout" yaml:"idleTimeout" mapstructure:"idleTimeout"`
	MaxHeaderBytes      int           `json:"maxHeaderBytes" yaml:"maxHeaderBytes" mapstructure:"maxHeaderBytes"`
	ShutdownGracePeriod time.Duration `json:"shutdownGracePeriod" yaml:"shutdownGracePeriod" mapstructure:"shutdownGracePeriod"`
	InboundWorkerCount  int           `json:"inboundWorkerCount" yaml:"inboundWorkerCount" mapstructure:"inboundWorkerCount"`
	InboundQueueSize    int           `json:"inboundQueueSize" yaml:"inboundQueueSize" mapstructure:"inboundQueueSize"`
}

// HTTPClientConfig configures the outbound HTTP client
type HTTPClientConfig struct {
	Timeout             time.Duration `json:"timeout" yaml:"timeout" mapstructure:"timeout"`
	MaxIdleConns        int           `json:"maxIdleConns" yaml:"maxIdleConns" mapstructure:"maxIdleConns"`
	MaxIdleConnsPerHost int           `json:"maxIdleConnsPerHost" yaml:"maxIdleConnsPerHost" mapstructure:"maxIdleConnsPerHost"`
	IdleConnTimeout     time.Duration `json:"idleConnTimeout" yaml:"idleConnTimeout" mapstructure:"idleConnTimeout"`
	TLS                 HTTPClientTLS `json:"tls,omitempty" yaml:"tls,omitempty" mapstructure:"tls"`
}

// HTTPClientTLS configures TLS settings for the outbound HTTP client
type HTTPClientTLS struct {
	InsecureSkipVerify bool `json:"insecureSkipVerify" yaml:"insecureSkipVerify" mapstructure:"insecureSkipVerify"`
}

// NATSConfig contains NATS connection and JetStream configuration
type NATSConfig struct {
	URLs      []string `json:"urls" yaml:"urls" mapstructure:"urls"`
	Username  string   `json:"username" yaml:"username" mapstructure:"username"`
	Password  string   `json:"password" yaml:"password" mapstructure:"password"`
	Token     string   `json:"token" yaml:"token" mapstructure:"token"`
	NKey      string   `json:"nkey" yaml:"nkey" mapstructure:"nkey"`
	CredsFile string   `json:"credsFile" yaml:"credsFile" mapstructure:"credsFile"`

	TLS struct {
		Enable   bool   `json:"enable" yaml:"enable" mapstructure:"enable"`
		CertFile string `json:"certFile" yaml:"certFile" mapstructure:"certFile"`
		KeyFile  string `json:"keyFile" yaml:"keyFile" mapstructure:"keyFile"`
		CAFile   string `json:"caFile" yaml:"caFile" mapstructure:"caFile"`
		Insecure bool   `json:"insecure" yaml:"insecure" mapstructure:"insecure"`
	} `json:"tls" yaml:"tls" mapstructure:"tls"`

	Consumers  ConsumerConfig   `json:"consumers" yaml:"consumers" mapstructure:"consumers"`
	Connection ConnectionConfig `json:"connection" yaml:"connection" mapstructure:"connection"`
	Publish    PublishConfig    `json:"publish" yaml:"publish" mapstructure:"publish"`
}

// ConsumerConfig contains JetStream consumer configuration
type ConsumerConfig struct {
	ConsumerPrefix  string        `json:"consumerPrefix" yaml:"consumerPrefix" mapstructure:"consumerPrefix"`
	WorkerCount int           `json:"workerCount" yaml:"workerCount" mapstructure:"workerCount"`
	FetchBatchSize  int           `json:"fetchBatchSize" yaml:"fetchBatchSize" mapstructure:"fetchBatchSize"`
	FetchTimeout    time.Duration `json:"fetchTimeout" yaml:"fetchTimeout" mapstructure:"fetchTimeout"`
	MaxAckPending   int           `json:"maxAckPending" yaml:"maxAckPending" mapstructure:"maxAckPending"`
	AckWaitTimeout  time.Duration `json:"ackWaitTimeout" yaml:"ackWaitTimeout" mapstructure:"ackWaitTimeout"`
	MaxDeliver      int           `json:"maxDeliver" yaml:"maxDeliver" mapstructure:"maxDeliver"`
	DeliverPolicy   string        `json:"deliverPolicy" yaml:"deliverPolicy" mapstructure:"deliverPolicy"`
	ReplayPolicy    string        `json:"replayPolicy" yaml:"replayPolicy" mapstructure:"replayPolicy"`
}

// ConnectionConfig contains NATS connection settings
type ConnectionConfig struct {
	MaxReconnects int           `json:"maxReconnects" yaml:"maxReconnects" mapstructure:"maxReconnects"`
	ReconnectWait time.Duration `json:"reconnectWait" yaml:"reconnectWait" mapstructure:"reconnectWait"`
}

// PublishConfig contains NATS publish configuration
type PublishConfig struct {
	Mode           string        `json:"mode" yaml:"mode" mapstructure:"mode"`
	AckTimeout     time.Duration `json:"ackTimeout" yaml:"ackTimeout" mapstructure:"ackTimeout"`
	MaxRetries     int           `json:"maxRetries" yaml:"maxRetries" mapstructure:"maxRetries"`
	RetryBaseDelay time.Duration `json:"retryBaseDelay" yaml:"retryBaseDelay" mapstructure:"retryBaseDelay"`
}

// LogConfig contains logging configuration
type LogConfig struct {
	Level      string `json:"level" yaml:"level" mapstructure:"level"`
	Encoding   string `json:"encoding" yaml:"encoding" mapstructure:"encoding"`
	OutputPath string `json:"outputPath" yaml:"outputPath" mapstructure:"outputPath"`
}

// MetricsConfig contains metrics server configuration
type MetricsConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	Address        string `json:"address" yaml:"address" mapstructure:"address"`
	Path           string `json:"path" yaml:"path" mapstructure:"path"`
	UpdateInterval string `json:"updateInterval" yaml:"updateInterval" mapstructure:"updateInterval"`
}

// KVConfig contains Key-Value store configuration
type KVConfig struct {
	Enabled    bool     `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	Buckets    []string `json:"buckets" yaml:"buckets" mapstructure:"buckets"`
	LocalCache struct {
		Enabled bool `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	} `json:"localCache" yaml:"localCache" mapstructure:"localCache"`
}

// SecurityConfig contains security-related configuration
type SecurityConfig struct {
	Verification VerificationConfig `json:"verification" yaml:"verification" mapstructure:"verification"`
}

// VerificationConfig contains signature verification settings
type VerificationConfig struct {
	Enabled         bool   `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	PublicKeyHeader string `json:"publicKeyHeader" yaml:"publicKeyHeader" mapstructure:"publicKeyHeader"`
	SignatureHeader string `json:"signatureHeader" yaml:"signatureHeader" mapstructure:"signatureHeader"`
}

// Load reads configuration using Viper, supporting file, env vars, and flags.
func Load(path string) (*Config, error) {
	v := viper.New()

	// Set the config file path and type
	v.SetConfigFile(path)
	ext := filepath.Ext(path)
	v.SetConfigType(strings.TrimPrefix(ext, "."))

	// Configure environment variable handling
	v.SetEnvPrefix("RR") // e.g., RR_NATS_URLS
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_")) // Allows RR_HTTP_SERVER_ADDRESS

	// Read the configuration file
	if err := v.ReadInConfig(); err != nil {
		// It's okay if the config file doesn't exist, we can rely on env vars/flags
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var config Config

	// Set defaults first
	setDefaults(&config)

	// Unmarshal the configuration into the struct. This merges all sources:
	// file, environment variables, and any bound flags.
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Run final validation
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// LoadHTTPConfig loads configuration and validates that HTTP fields are present
func LoadHTTPConfig(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}

	// Validate that HTTP configuration is present and valid
	if cfg.HTTP.Server.Address == "" {
		return nil, fmt.Errorf("HTTP server address is required for http-gateway")
	}

	return cfg, nil
}

// setDefaults applies default values to the configuration
func setDefaults(cfg *Config) {
	// NATS defaults
	if len(cfg.NATS.URLs) == 0 {
		cfg.NATS.URLs = []string{DefaultNATSURL}
	}
	if cfg.NATS.Connection.MaxReconnects == 0 {
		cfg.NATS.Connection.MaxReconnects = -1
	}
	if cfg.NATS.Connection.ReconnectWait == 0 {
		cfg.NATS.Connection.ReconnectWait = DefaultReconnectWait
	}

	// Consumer defaults
	if cfg.NATS.Consumers.ConsumerPrefix == "" {
		cfg.NATS.Consumers.ConsumerPrefix = DefaultConsumerPrefix
	}
	if cfg.NATS.Consumers.WorkerCount == 0 {
		cfg.NATS.Consumers.WorkerCount = DefaultWorkerCount
	}
	if cfg.NATS.Consumers.FetchBatchSize == 0 {
		cfg.NATS.Consumers.FetchBatchSize = DefaultFetchBatchSize
	}
	if cfg.NATS.Consumers.FetchTimeout == 0 {
		cfg.NATS.Consumers.FetchTimeout = DefaultFetchTimeout
	}
	if cfg.NATS.Consumers.AckWaitTimeout == 0 {
		cfg.NATS.Consumers.AckWaitTimeout = DefaultAckWaitTimeout
	}
	if cfg.NATS.Consumers.MaxDeliver == 0 {
		cfg.NATS.Consumers.MaxDeliver = DefaultMaxDeliver
	}
	if cfg.NATS.Consumers.MaxAckPending == 0 {
		cfg.NATS.Consumers.MaxAckPending = DefaultMaxAckPending
	}
	if cfg.NATS.Consumers.DeliverPolicy == "" {
		cfg.NATS.Consumers.DeliverPolicy = DefaultDeliverPolicy
	}
	if cfg.NATS.Consumers.ReplayPolicy == "" {
		cfg.NATS.Consumers.ReplayPolicy = DefaultReplayPolicy
	}

	// Publish defaults
	if cfg.NATS.Publish.Mode == "" {
		cfg.NATS.Publish.Mode = DefaultPublishMode
	}
	if cfg.NATS.Publish.AckTimeout == 0 {
		cfg.NATS.Publish.AckTimeout = DefaultPublishAckTimeout
	}
	if cfg.NATS.Publish.MaxRetries == 0 {
		cfg.NATS.Publish.MaxRetries = DefaultPublishMaxRetries
	}
	if cfg.NATS.Publish.RetryBaseDelay == 0 {
		cfg.NATS.Publish.RetryBaseDelay = DefaultRetryBaseDelay
	}

	// HTTP Server defaults
	if cfg.HTTP.Server.Address == "" {
		cfg.HTTP.Server.Address = DefaultHTTPServerAddress
	}
	if cfg.HTTP.Server.ReadTimeout == 0 {
		cfg.HTTP.Server.ReadTimeout = DefaultHTTPReadTimeout
	}
	if cfg.HTTP.Server.WriteTimeout == 0 {
		cfg.HTTP.Server.WriteTimeout = DefaultHTTPWriteTimeout
	}
	if cfg.HTTP.Server.IdleTimeout == 0 {
		cfg.HTTP.Server.IdleTimeout = DefaultHTTPIdleTimeout
	}
	if cfg.HTTP.Server.MaxHeaderBytes == 0 {
		cfg.HTTP.Server.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if cfg.HTTP.Server.ShutdownGracePeriod == 0 {
		cfg.HTTP.Server.ShutdownGracePeriod = DefaultHTTPShutdownGracePeriod
	}

	// HTTP Client defaults
	if cfg.HTTP.Client.Timeout == 0 {
		cfg.HTTP.Client.Timeout = DefaultHTTPClientTimeout
	}
	if cfg.HTTP.Client.MaxIdleConns == 0 {
		cfg.HTTP.Client.MaxIdleConns = DefaultMaxIdleConns
	}
	if cfg.HTTP.Client.MaxIdleConnsPerHost == 0 {
		cfg.HTTP.Client.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	}
	if cfg.HTTP.Client.IdleConnTimeout == 0 {
		cfg.HTTP.Client.IdleConnTimeout = DefaultHTTPIdleConnTimeout
	}
	if cfg.HTTP.Server.InboundWorkerCount == 0 {
		cfg.HTTP.Server.InboundWorkerCount = 10
	}
	if cfg.HTTP.Server.InboundQueueSize == 0 {
		cfg.HTTP.Server.InboundQueueSize = DefaultInboundQueueSize
	}

	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = DefaultLogLevel
	}
	if cfg.Logging.Encoding == "" {
		cfg.Logging.Encoding = DefaultLogEncoding
	}
	if cfg.Logging.OutputPath == "" {
		cfg.Logging.OutputPath = DefaultLogOutput
	}

	// Metrics defaults
	if !cfg.Metrics.Enabled {
		// If metrics are disabled, don't set address/path defaults
	} else {
		if cfg.Metrics.Address == "" {
			cfg.Metrics.Address = DefaultMetricsAddress
		}
		if cfg.Metrics.Path == "" {
			cfg.Metrics.Path = DefaultMetricsPath
		}
		if cfg.Metrics.UpdateInterval == "" {
			cfg.Metrics.UpdateInterval = "15s"
		}
	}

	// KV defaults
	if cfg.KV.Enabled {
		cfg.KV.LocalCache.Enabled = true
	}

	// Security defaults
	if cfg.Security.Verification.PublicKeyHeader == "" {
		cfg.Security.Verification.PublicKeyHeader = DefaultPublicKeyHeader
	}
	if cfg.Security.Verification.SignatureHeader == "" {
		cfg.Security.Verification.SignatureHeader = DefaultSignatureHeader
	}

	// ForEach defaults
	if cfg.ForEach.MaxIterations == 0 {
		cfg.ForEach.MaxIterations = DefaultForEachMaxIterations
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
		if cfg.NATS.TLS.CertFile != "" && cfg.NATS.TLS.KeyFile == "" {
			return fmt.Errorf("NATS TLS key file is required when a cert file is provided")
		}
		if cfg.NATS.TLS.KeyFile != "" && cfg.NATS.TLS.CertFile == "" {
			return fmt.Errorf("NATS TLS cert file is required when a key file is provided")
		}
	}

	if cfg.NATS.CredsFile != "" {
		if _, err := os.Stat(cfg.NATS.CredsFile); os.IsNotExist(err) {
			return fmt.Errorf("NATS creds file does not exist: %s", cfg.NATS.CredsFile)
		}
	}

	// Consumer validation with bounds checking
	if cfg.NATS.Consumers.WorkerCount < 1 {
		return fmt.Errorf("worker count must be at least 1")
	}
	if cfg.NATS.Consumers.WorkerCount > MaxWorkerCount {
		return fmt.Errorf("worker count too high (%d), maximum is %d", cfg.NATS.Consumers.WorkerCount, MaxWorkerCount)
	}
	if cfg.NATS.Consumers.FetchBatchSize < 1 {
		return fmt.Errorf("fetch batch size must be at least 1")
	}
	if cfg.NATS.Consumers.FetchBatchSize > MaxFetchBatchSize {
		return fmt.Errorf("fetch batch size too high (%d), maximum is %d", cfg.NATS.Consumers.FetchBatchSize, MaxFetchBatchSize)
	}
	if cfg.NATS.Consumers.FetchTimeout <= 0 {
		return fmt.Errorf("fetch timeout must be positive")
	}
	if cfg.NATS.Consumers.MaxAckPending < 1 {
		return fmt.Errorf("max ack pending must be at least 1")
	}
	if cfg.NATS.Consumers.MaxAckPending > MaxAckPending {
		return fmt.Errorf("max ack pending too high (%d), maximum is %d", cfg.NATS.Consumers.MaxAckPending, MaxAckPending)
	}
	if cfg.NATS.Consumers.MaxDeliver < 1 {
		return fmt.Errorf("max deliver must be at least 1")
	}

	validDeliverPolicies := map[string]bool{
		"all": true, "new": true, "last": true, "by_start_time": true, "by_start_sequence": true,
	}
	if !validDeliverPolicies[cfg.NATS.Consumers.DeliverPolicy] {
		return fmt.Errorf("invalid deliver policy: %s", cfg.NATS.Consumers.DeliverPolicy)
	}

	validReplayPolicies := map[string]bool{"instant": true, "original": true}
	if !validReplayPolicies[cfg.NATS.Consumers.ReplayPolicy] {
		return fmt.Errorf("invalid replay policy: %s", cfg.NATS.Consumers.ReplayPolicy)
	}

	// Publish validation
	if cfg.NATS.Publish.Mode != "jetstream" && cfg.NATS.Publish.Mode != "core" {
		return fmt.Errorf("publish mode must be 'jetstream' or 'core', got: %s", cfg.NATS.Publish.Mode)
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[cfg.Logging.Level] {
		return fmt.Errorf("invalid log level: %s", cfg.Logging.Level)
	}

	// Metrics validation
	if cfg.Metrics.Enabled {
		if cfg.Metrics.UpdateInterval != "" {
			if _, err := time.ParseDuration(cfg.Metrics.UpdateInterval); err != nil {
				return fmt.Errorf("invalid metrics update interval '%s': %w", cfg.Metrics.UpdateInterval, err)
			}
		}
	}

	// HTTP-specific validation with bounds checking
	if cfg.HTTP.Server.Address != "" {
		if cfg.HTTP.Server.ReadTimeout < 0 {
			return fmt.Errorf("HTTP server read timeout cannot be negative")
		}
		if cfg.HTTP.Server.WriteTimeout < 0 {
			return fmt.Errorf("HTTP server write timeout cannot be negative")
		}
		if cfg.HTTP.Server.InboundWorkerCount < 1 {
			return fmt.Errorf("inbound worker count must be at least 1")
		}
		if cfg.HTTP.Server.InboundWorkerCount > MaxWorkerCount {
			return fmt.Errorf("inbound worker count too high (%d), maximum is %d", cfg.HTTP.Server.InboundWorkerCount, MaxWorkerCount)
		}
		if cfg.HTTP.Server.InboundQueueSize < 1 {
			return fmt.Errorf("inbound queue size must be at least 1")
		}
		if cfg.HTTP.Server.InboundQueueSize > MaxInboundQueueSize {
			return fmt.Errorf("inbound queue size too high (%d), maximum is %d", cfg.HTTP.Server.InboundQueueSize, MaxInboundQueueSize)
		}
		if cfg.HTTP.Client.Timeout < 0 {
			return fmt.Errorf("HTTP client timeout cannot be negative")
		}
		if cfg.HTTP.Client.MaxIdleConns < 0 {
			return fmt.Errorf("HTTP client max idle connections cannot be negative")
		}
		if cfg.HTTP.Client.MaxIdleConnsPerHost < 0 {
			return fmt.Errorf("HTTP client max idle connections per host cannot be negative")
		}
	}

	// ForEach validation
	if cfg.ForEach.MaxIterations < 0 {
		return fmt.Errorf("forEach maxIterations cannot be negative (use 0 for unlimited)")
	}
	if cfg.ForEach.MaxIterations > MaxForEachIterations {
		return fmt.Errorf("forEach maxIterations too high (%d), maximum is %d", cfg.ForEach.MaxIterations, MaxForEachIterations)
	}

	return nil
}



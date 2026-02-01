// file: config/config_test.go

package config

import (
	"testing"
	"time"
)

func TestSetDefaults(t *testing.T) {
	tests := []struct {
		name     string
		initial  Config
		validate func(t *testing.T, cfg *Config)
	}{
		{
			name:    "empty config gets all defaults",
			initial: Config{},
			validate: func(t *testing.T, cfg *Config) {
				// NATS defaults
				if len(cfg.NATS.URLs) != 1 || cfg.NATS.URLs[0] != "nats://localhost:4222" {
					t.Errorf("NATS URLs = %v, want [nats://localhost:4222]", cfg.NATS.URLs)
				}
				if cfg.NATS.Connection.MaxReconnects != -1 {
					t.Errorf("MaxReconnects = %d, want -1", cfg.NATS.Connection.MaxReconnects)
				}
				if cfg.NATS.Connection.ReconnectWait != 50*time.Millisecond {
					t.Errorf("ReconnectWait = %v, want 50ms", cfg.NATS.Connection.ReconnectWait)
				}

				// Consumer defaults
				if cfg.NATS.Consumers.ConsumerPrefix != "rule-router" {
					t.Errorf("ConsumerPrefix = %s, want rule-router", cfg.NATS.Consumers.ConsumerPrefix)
				}
				if cfg.NATS.Consumers.WorkerCount != 2 {
					t.Errorf("WorkerCount = %d, want 2", cfg.NATS.Consumers.WorkerCount)
				}
				if cfg.NATS.Consumers.FetchBatchSize != 1 {
					t.Errorf("FetchBatchSize = %d, want 1", cfg.NATS.Consumers.FetchBatchSize)
				}
				if cfg.NATS.Consumers.FetchTimeout != 5*time.Second {
					t.Errorf("FetchTimeout = %v, want 5s", cfg.NATS.Consumers.FetchTimeout)
				}
				if cfg.NATS.Consumers.AckWaitTimeout != 30*time.Second {
					t.Errorf("AckWaitTimeout = %v, want 30s", cfg.NATS.Consumers.AckWaitTimeout)
				}
				if cfg.NATS.Consumers.MaxDeliver != 3 {
					t.Errorf("MaxDeliver = %d, want 3", cfg.NATS.Consumers.MaxDeliver)
				}
				if cfg.NATS.Consumers.MaxAckPending != 1000 {
					t.Errorf("MaxAckPending = %d, want 1000", cfg.NATS.Consumers.MaxAckPending)
				}
				if cfg.NATS.Consumers.DeliverPolicy != "new" {
					t.Errorf("DeliverPolicy = %s, want new", cfg.NATS.Consumers.DeliverPolicy)
				}
				if cfg.NATS.Consumers.ReplayPolicy != "instant" {
					t.Errorf("ReplayPolicy = %s, want instant", cfg.NATS.Consumers.ReplayPolicy)
				}

				// Publish defaults
				if cfg.NATS.Publish.Mode != "jetstream" {
					t.Errorf("Publish.Mode = %s, want jetstream", cfg.NATS.Publish.Mode)
				}
				if cfg.NATS.Publish.AckTimeout != 5*time.Second {
					t.Errorf("Publish.AckTimeout = %v, want 5s", cfg.NATS.Publish.AckTimeout)
				}
				if cfg.NATS.Publish.MaxRetries != 3 {
					t.Errorf("Publish.MaxRetries = %d, want 3", cfg.NATS.Publish.MaxRetries)
				}

				// HTTP Server defaults
				if cfg.HTTP.Server.Address != ":8080" {
					t.Errorf("HTTP.Server.Address = %s, want :8080", cfg.HTTP.Server.Address)
				}
				if cfg.HTTP.Server.ReadTimeout != 30*time.Second {
					t.Errorf("HTTP.Server.ReadTimeout = %v, want 30s", cfg.HTTP.Server.ReadTimeout)
				}
				if cfg.HTTP.Server.InboundWorkerCount != 10 {
					t.Errorf("HTTP.Server.InboundWorkerCount = %d, want 10", cfg.HTTP.Server.InboundWorkerCount)
				}
				if cfg.HTTP.Server.InboundQueueSize != 1000 {
					t.Errorf("HTTP.Server.InboundQueueSize = %d, want 1000", cfg.HTTP.Server.InboundQueueSize)
				}

				// HTTP Client defaults
				if cfg.HTTP.Client.Timeout != 30*time.Second {
					t.Errorf("HTTP.Client.Timeout = %v, want 30s", cfg.HTTP.Client.Timeout)
				}
				if cfg.HTTP.Client.MaxIdleConns != 100 {
					t.Errorf("HTTP.Client.MaxIdleConns = %d, want 100", cfg.HTTP.Client.MaxIdleConns)
				}

				// Logging defaults
				if cfg.Logging.Level != "info" {
					t.Errorf("Logging.Level = %s, want info", cfg.Logging.Level)
				}
				if cfg.Logging.Encoding != "json" {
					t.Errorf("Logging.Encoding = %s, want json", cfg.Logging.Encoding)
				}
				if cfg.Logging.OutputPath != "stdout" {
					t.Errorf("Logging.OutputPath = %s, want stdout", cfg.Logging.OutputPath)
				}

				// Security defaults
				if cfg.Security.Verification.PublicKeyHeader != "Nats-Public-Key" {
					t.Errorf("PublicKeyHeader = %s, want Nats-Public-Key", cfg.Security.Verification.PublicKeyHeader)
				}
				if cfg.Security.Verification.SignatureHeader != "Nats-Signature" {
					t.Errorf("SignatureHeader = %s, want Nats-Signature", cfg.Security.Verification.SignatureHeader)
				}

				// ForEach defaults
				if cfg.ForEach.MaxIterations != 100 {
					t.Errorf("ForEach.MaxIterations = %d, want 100", cfg.ForEach.MaxIterations)
				}
			},
		},
		{
			name: "existing values not overwritten",
			initial: Config{
				NATS: NATSConfig{
					URLs: []string{"nats://custom:4222"},
					Consumers: ConsumerConfig{
						ConsumerPrefix: "custom-prefix",
						WorkerCount:    5,
					},
				},
				Logging: LogConfig{
					Level: "debug",
				},
			},
			validate: func(t *testing.T, cfg *Config) {
				if len(cfg.NATS.URLs) != 1 || cfg.NATS.URLs[0] != "nats://custom:4222" {
					t.Errorf("NATS URLs overwritten, got %v", cfg.NATS.URLs)
				}
				if cfg.NATS.Consumers.ConsumerPrefix != "custom-prefix" {
					t.Errorf("ConsumerPrefix overwritten, got %s", cfg.NATS.Consumers.ConsumerPrefix)
				}
				if cfg.NATS.Consumers.WorkerCount != 5 {
					t.Errorf("WorkerCount overwritten, got %d", cfg.NATS.Consumers.WorkerCount)
				}
				if cfg.Logging.Level != "debug" {
					t.Errorf("Logging.Level overwritten, got %s", cfg.Logging.Level)
				}
			},
		},
		{
			name: "metrics defaults only when enabled",
			initial: Config{
				Metrics: MetricsConfig{
					Enabled: true,
				},
			},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Metrics.Address != ":2112" {
					t.Errorf("Metrics.Address = %s, want :2112", cfg.Metrics.Address)
				}
				if cfg.Metrics.Path != "/metrics" {
					t.Errorf("Metrics.Path = %s, want /metrics", cfg.Metrics.Path)
				}
				if cfg.Metrics.UpdateInterval != "15s" {
					t.Errorf("Metrics.UpdateInterval = %s, want 15s", cfg.Metrics.UpdateInterval)
				}
			},
		},
		{
			name: "metrics disabled leaves address empty",
			initial: Config{
				Metrics: MetricsConfig{
					Enabled: false,
				},
			},
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Metrics.Address != "" {
					t.Errorf("Metrics.Address should be empty when disabled, got %s", cfg.Metrics.Address)
				}
			},
		},
		{
			name: "KV local cache enabled when KV enabled",
			initial: Config{
				KV: KVConfig{
					Enabled: true,
				},
			},
			validate: func(t *testing.T, cfg *Config) {
				if !cfg.KV.LocalCache.Enabled {
					t.Error("KV.LocalCache.Enabled should be true when KV is enabled")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.initial
			setDefaults(&cfg)
			tt.validate(t, &cfg)
		})
	}
}

func TestValidateConfig(t *testing.T) {
	// Helper to create a minimal valid config
	validConfig := func() *Config {
		cfg := &Config{}
		setDefaults(cfg)
		return cfg
	}

	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr string
	}{
		{
			name:    "valid default config",
			modify:  func(cfg *Config) {},
			wantErr: "",
		},
		{
			name: "empty NATS URLs",
			modify: func(cfg *Config) {
				cfg.NATS.URLs = []string{}
			},
			wantErr: "at least one NATS URL must be specified",
		},
		{
			name: "multiple auth methods - username and token",
			modify: func(cfg *Config) {
				cfg.NATS.Username = "user"
				cfg.NATS.Token = "token"
			},
			wantErr: "only one NATS authentication method",
		},
		{
			name: "multiple auth methods - token and nkey",
			modify: func(cfg *Config) {
				cfg.NATS.Token = "token"
				cfg.NATS.NKey = "nkey"
			},
			wantErr: "only one NATS authentication method",
		},
		{
			name: "TLS cert without key",
			modify: func(cfg *Config) {
				cfg.NATS.TLS.Enable = true
				cfg.NATS.TLS.CertFile = "/path/to/cert.pem"
			},
			wantErr: "NATS TLS key file is required",
		},
		{
			name: "TLS key without cert",
			modify: func(cfg *Config) {
				cfg.NATS.TLS.Enable = true
				cfg.NATS.TLS.KeyFile = "/path/to/key.pem"
			},
			wantErr: "NATS TLS cert file is required",
		},
		{
			name: "worker count too low",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.WorkerCount = 0
			},
			wantErr: "worker count must be at least 1",
		},
		{
			name: "worker count too high",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.WorkerCount = 1001
			},
			wantErr: "worker count too high",
		},
		{
			name: "fetch batch size too low",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.FetchBatchSize = 0
			},
			wantErr: "fetch batch size must be at least 1",
		},
		{
			name: "fetch batch size too high",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.FetchBatchSize = 10001
			},
			wantErr: "fetch batch size too high",
		},
		{
			name: "negative fetch timeout",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.FetchTimeout = -1 * time.Second
			},
			wantErr: "fetch timeout must be positive",
		},
		{
			name: "max ack pending too low",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.MaxAckPending = 0
			},
			wantErr: "max ack pending must be at least 1",
		},
		{
			name: "max ack pending too high",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.MaxAckPending = 100001
			},
			wantErr: "max ack pending too high",
		},
		{
			name: "max deliver too low",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.MaxDeliver = 0
			},
			wantErr: "max deliver must be at least 1",
		},
		{
			name: "invalid deliver policy",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.DeliverPolicy = "invalid"
			},
			wantErr: "invalid deliver policy",
		},
		{
			name: "invalid replay policy",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.ReplayPolicy = "invalid"
			},
			wantErr: "invalid replay policy",
		},
		{
			name: "invalid publish mode",
			modify: func(cfg *Config) {
				cfg.NATS.Publish.Mode = "invalid"
			},
			wantErr: "publish mode must be",
		},
		{
			name: "invalid log level",
			modify: func(cfg *Config) {
				cfg.Logging.Level = "invalid"
			},
			wantErr: "invalid log level",
		},
		{
			name: "invalid metrics update interval",
			modify: func(cfg *Config) {
				cfg.Metrics.Enabled = true
				cfg.Metrics.Address = ":2112"
				cfg.Metrics.UpdateInterval = "invalid"
			},
			wantErr: "invalid metrics update interval",
		},
		{
			name: "negative HTTP read timeout",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.ReadTimeout = -1 * time.Second
			},
			wantErr: "HTTP server read timeout cannot be negative",
		},
		{
			name: "negative HTTP write timeout",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.WriteTimeout = -1 * time.Second
			},
			wantErr: "HTTP server write timeout cannot be negative",
		},
		{
			name: "inbound worker count too low",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.InboundWorkerCount = 0
			},
			wantErr: "inbound worker count must be at least 1",
		},
		{
			name: "inbound worker count too high",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.InboundWorkerCount = 1001
			},
			wantErr: "inbound worker count too high",
		},
		{
			name: "inbound queue size too low",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.InboundQueueSize = 0
			},
			wantErr: "inbound queue size must be at least 1",
		},
		{
			name: "inbound queue size too high",
			modify: func(cfg *Config) {
				cfg.HTTP.Server.InboundQueueSize = 100001
			},
			wantErr: "inbound queue size too high",
		},
		{
			name: "negative HTTP client timeout",
			modify: func(cfg *Config) {
				cfg.HTTP.Client.Timeout = -1 * time.Second
			},
			wantErr: "HTTP client timeout cannot be negative",
		},
		{
			name: "negative max idle conns",
			modify: func(cfg *Config) {
				cfg.HTTP.Client.MaxIdleConns = -1
			},
			wantErr: "HTTP client max idle connections cannot be negative",
		},
		{
			name: "negative max idle conns per host",
			modify: func(cfg *Config) {
				cfg.HTTP.Client.MaxIdleConnsPerHost = -1
			},
			wantErr: "HTTP client max idle connections per host cannot be negative",
		},
		{
			name: "negative forEach maxIterations",
			modify: func(cfg *Config) {
				cfg.ForEach.MaxIterations = -1
			},
			wantErr: "forEach maxIterations cannot be negative",
		},
		{
			name: "forEach maxIterations too high",
			modify: func(cfg *Config) {
				cfg.ForEach.MaxIterations = 10001
			},
			wantErr: "forEach maxIterations too high",
		},
		{
			name: "valid deliver policies",
			modify: func(cfg *Config) {
				// Test all valid deliver policies
				for _, policy := range []string{"all", "new", "last", "by_start_time", "by_start_sequence"} {
					cfg.NATS.Consumers.DeliverPolicy = policy
				}
			},
			wantErr: "",
		},
		{
			name: "valid replay policies",
			modify: func(cfg *Config) {
				cfg.NATS.Consumers.ReplayPolicy = "original"
			},
			wantErr: "",
		},
		{
			name: "publish mode core is valid",
			modify: func(cfg *Config) {
				cfg.NATS.Publish.Mode = "core"
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)

			err := validateConfig(cfg)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateConfig() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateConfig() expected error containing %q, got nil", tt.wantErr)
				} else if !contains(err.Error(), tt.wantErr) {
					t.Errorf("validateConfig() error = %q, want error containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}


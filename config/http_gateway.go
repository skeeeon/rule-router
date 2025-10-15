// file: config/http_gateway.go

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

// HTTPGatewayConfig extends base config with HTTP-specific settings
type HTTPGatewayConfig struct {
	NATS     NATSConfig         `json:"nats" yaml:"nats"`
	HTTP     HTTPConfig         `json:"http" yaml:"http"`
	Logging  LogConfig          `json:"logging" yaml:"logging"`
	Metrics  MetricsConfig      `json:"metrics" yaml:"metrics"`
	KV       KVConfig           `json:"kv" yaml:"kv"`
	Security SecurityConfig     `json:"security" yaml:"security"`
}

// HTTPConfig contains HTTP server and client configuration
type HTTPConfig struct {
	Server HTTPServerConfig `json:"server" yaml:"server"`
	Client HTTPClientConfig `json:"client" yaml:"client"`
}

// HTTPServerConfig configures the inbound HTTP server
type HTTPServerConfig struct {
	Address              string        `json:"address" yaml:"address"`
	ReadTimeout          time.Duration `json:"readTimeout" yaml:"readTimeout"`
	WriteTimeout         time.Duration `json:"writeTimeout" yaml:"writeTimeout"`
	IdleTimeout          time.Duration `json:"idleTimeout" yaml:"idleTimeout"`
	MaxHeaderBytes       int           `json:"maxHeaderBytes" yaml:"maxHeaderBytes"`
	ShutdownGracePeriod  time.Duration `json:"shutdownGracePeriod" yaml:"shutdownGracePeriod"`
}

// HTTPClientConfig configures the outbound HTTP client
type HTTPClientConfig struct {
	Timeout            time.Duration `json:"timeout" yaml:"timeout"`
	MaxIdleConns       int           `json:"maxIdleConns" yaml:"maxIdleConns"`
	MaxIdleConnsPerHost int          `json:"maxIdleConnsPerHost" yaml:"maxIdleConnsPerHost"`
	IdleConnTimeout    time.Duration `json:"idleConnTimeout" yaml:"idleConnTimeout"`
}

// LoadHTTPGateway loads HTTP gateway configuration from file
func LoadHTTPGateway(path string) (*HTTPGatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	
	var config HTTPGatewayConfig
	
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
	
	setHTTPGatewayDefaults(&config)
	
	if err := validateHTTPGatewayConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	
	return &config, nil
}

// setHTTPGatewayDefaults sets default values for HTTP gateway config
func setHTTPGatewayDefaults(cfg *HTTPGatewayConfig) {
	// Set base config defaults
	setDefaults((*Config)(cfg))
	
	// HTTP Server defaults
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
	
	// HTTP Client defaults
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
}

// validateHTTPGatewayConfig validates HTTP gateway configuration
func validateHTTPGatewayConfig(cfg *HTTPGatewayConfig) error {
	// Validate base config
	baseCfg := &Config{
		NATS:     cfg.NATS,
		Logging:  cfg.Logging,
		Metrics:  cfg.Metrics,
		KV:       cfg.KV,
		Security: cfg.Security,
	}
	if err := validateConfig(baseCfg); err != nil {
		return err
	}
	
	// HTTP-specific validation
	if cfg.HTTP.Server.Address == "" {
		return fmt.Errorf("HTTP server address cannot be empty")
	}
	
	if cfg.HTTP.Server.ReadTimeout < 0 {
		return fmt.Errorf("HTTP server read timeout cannot be negative")
	}
	
	if cfg.HTTP.Client.Timeout < 0 {
		return fmt.Errorf("HTTP client timeout cannot be negative")
	}
	
	return nil
}

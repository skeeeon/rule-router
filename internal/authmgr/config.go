// file: internal/authmgr/config.go

package authmgr

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
	"rule-router/config"
)

// envVarPattern matches environment variable placeholders: ${VAR_NAME}
// Allows uppercase, lowercase, numbers, and underscores for consistency
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

// Config represents the complete auth-manager configuration
type Config struct {
	NATS      NATSConfig       `mapstructure:"nats"`
	Storage   StorageConfig    `mapstructure:"storage"`
	Logging   config.LogConfig `mapstructure:"logging"`
	Metrics   MetricsConfig    `mapstructure:"metrics"`
	Providers []ProviderConfig `mapstructure:"providers"`
}

// NATSConfig mirrors rule-router NATS config (connection only)
type NATSConfig struct {
	URLs      []string `mapstructure:"urls"`
	Username  string   `mapstructure:"username"`
	Password  string   `mapstructure:"password"`
	Token     string   `mapstructure:"token"`
	NKey      string   `mapstructure:"nkey"`
	CredsFile string   `mapstructure:"credsFile"`

	TLS struct {
		Enable   bool   `mapstructure:"enable"`
		CertFile string `mapstructure:"certFile"`
		KeyFile  string `mapstructure:"keyFile"`
		CAFile   string `mapstructure:"caFile"`
		Insecure bool   `mapstructure:"insecure"`
	} `mapstructure:"tls"`
}

// StorageConfig defines where to store tokens
type StorageConfig struct {
	Bucket    string `mapstructure:"bucket"`    // KV bucket name
	KeyPrefix string `mapstructure:"keyPrefix"` // Optional prefix for keys
}

// MetricsConfig for optional Prometheus metrics
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Address string `mapstructure:"address"`
}

// ProviderConfig defines an authentication provider
type ProviderConfig struct {
	ID            string            `mapstructure:"id"`
	Type          string            `mapstructure:"type"` // "oauth2" or "custom-http"
	KVKey         string            `mapstructure:"kvKey"`
	RefreshBefore string            `mapstructure:"refreshBefore"` // OAuth2 only
	RefreshEvery  string            `mapstructure:"refreshEvery"`  // Custom HTTP

	// OAuth2 fields
	TokenURL     string   `mapstructure:"tokenUrl"`
	ClientID     string   `mapstructure:"clientId"`
	ClientSecret string   `mapstructure:"clientSecret"`
	Scopes       []string `mapstructure:"scopes"`

	// Custom HTTP fields
	AuthURL   string            `mapstructure:"authUrl"`
	Method    string            `mapstructure:"method"`
	Headers   map[string]string `mapstructure:"headers"`
	Body      string            `mapstructure:"body"`
	TokenPath string            `mapstructure:"tokenPath"`
}

// Load reads configuration from file using Viper
func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)

	// Environment variable support (for direct overrides like AUTH_MGR_NATS_URLS)
	v.SetEnvPrefix("AUTH_MGR")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	var cfg Config
	setDefaults(&cfg)

	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand ${VAR_NAME} placeholders throughout the loaded config
	if err := expandEnvVars(&cfg); err != nil {
		return nil, fmt.Errorf("failed to expand environment variables: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// unsetEnvVars tracks environment variables that were referenced but not set
var unsetEnvVars []string

// expandEnvVars recursively expands ${VAR} placeholders in the config struct.
func expandEnvVars(cfg interface{}) error {
	// Reset tracking for each expansion
	unsetEnvVars = nil

	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return fmt.Errorf("config must be a non-nil pointer")
	}
	if err := expandRecursive(v); err != nil {
		return err
	}

	// Report any unset environment variables
	if len(unsetEnvVars) > 0 {
		return fmt.Errorf("missing environment variables: %v (set them or remove ${} references from config)", unsetEnvVars)
	}
	return nil
}

// expandRecursive is the helper that uses reflection to walk the config struct.
func expandRecursive(v reflect.Value) error {
	// Dereference pointers and interfaces to get to the actual value
	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := expandRecursive(v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if err := expandRecursive(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			val := v.MapIndex(key)
			if val.Kind() == reflect.Interface {
				val = val.Elem()
			}
			if val.Kind() == reflect.String {
				expanded := expandString(val.String())
				v.SetMapIndex(key, reflect.ValueOf(expanded))
			} else {
				if err := expandRecursive(val); err != nil {
					return err
				}
			}
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(expandString(v.String()))
		}
	}
	return nil
}

// expandString performs the actual replacement for a single string.
// Tracks unset variables for later reporting.
func expandString(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		value := os.Getenv(varName)
		if value == "" {
			// Track unset variables (avoid duplicates)
			found := false
			for _, v := range unsetEnvVars {
				if v == varName {
					found = true
					break
				}
			}
			if !found {
				unsetEnvVars = append(unsetEnvVars, varName)
			}
		}
		return value
	})
}

// setDefaults applies sensible defaults
func setDefaults(cfg *Config) {
	if len(cfg.NATS.URLs) == 0 {
		cfg.NATS.URLs = []string{"nats://localhost:4222"}
	}
	if cfg.Storage.Bucket == "" {
		cfg.Storage.Bucket = "tokens"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Encoding == "" {
		cfg.Logging.Encoding = "json"
	}
	if cfg.Logging.OutputPath == "" {
		cfg.Logging.OutputPath = "stdout"
	}
	if cfg.Metrics.Address == "" {
		cfg.Metrics.Address = ":2113"
	}
}

// validate ensures configuration is valid
func validate(cfg *Config) error {
	// NATS validation
	if len(cfg.NATS.URLs) == 0 {
		return fmt.Errorf("at least one NATS URL required")
	}

	// Auth method validation (only one allowed)
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
		return fmt.Errorf("only one NATS auth method allowed")
	}

	// Providers validation
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider required")
	}

	seenIDs := make(map[string]bool)
	for i, p := range cfg.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider %d: id is required", i)
		}
		if seenIDs[p.ID] {
			return fmt.Errorf("provider %d: duplicate id '%s'", i, p.ID)
		}
		seenIDs[p.ID] = true

		if p.Type != "oauth2" && p.Type != "custom-http" {
			return fmt.Errorf("provider %s: invalid type '%s' (must be 'oauth2' or 'custom-http')", p.ID, p.Type)
		}

		// Default kvKey to ID if not specified
		if cfg.Providers[i].KVKey == "" {
			cfg.Providers[i].KVKey = p.ID
		}

		// Type-specific validation
		if p.Type == "oauth2" {
			if p.TokenURL == "" {
				return fmt.Errorf("provider %s: tokenUrl required for oauth2", p.ID)
			}
			if p.ClientID == "" {
				return fmt.Errorf("provider %s: clientId required for oauth2", p.ID)
			}
			if p.ClientSecret == "" {
				return fmt.Errorf("provider %s: clientSecret required for oauth2", p.ID)
			}
			if p.RefreshBefore == "" {
				return fmt.Errorf("provider %s: refreshBefore required for oauth2", p.ID)
			}
			// Validate duration format
			if _, err := time.ParseDuration(p.RefreshBefore); err != nil {
				return fmt.Errorf("provider %s: invalid refreshBefore duration: %w", p.ID, err)
			}
		} else if p.Type == "custom-http" {
			if p.AuthURL == "" {
				return fmt.Errorf("provider %s: authUrl required for custom-http", p.ID)
			}
			if cfg.Providers[i].Method == "" {
				cfg.Providers[i].Method = "POST" // Default
			}
			if p.TokenPath == "" {
				return fmt.Errorf("provider %s: tokenPath required for custom-http", p.ID)
			}
			if p.RefreshEvery == "" {
				return fmt.Errorf("provider %s: refreshEvery required for custom-http", p.ID)
			}
			if _, err := time.ParseDuration(p.RefreshEvery); err != nil {
				return fmt.Errorf("provider %s: invalid refreshEvery duration: %w", p.ID, err)
			}
		}
	}

	// Storage validation
	if cfg.Storage.Bucket == "" {
		return fmt.Errorf("storage bucket name cannot be empty")
	}

	// TLS validation
	if cfg.NATS.TLS.Enable {
		if cfg.NATS.TLS.CertFile != "" && cfg.NATS.TLS.KeyFile == "" {
			return fmt.Errorf("NATS TLS key file required when cert file provided")
		}
		if cfg.NATS.TLS.KeyFile != "" && cfg.NATS.TLS.CertFile == "" {
			return fmt.Errorf("NATS TLS cert file required when key file provided")
		}
	}

	// Validate creds file exists if specified
	if cfg.NATS.CredsFile != "" {
		if _, err := os.Stat(cfg.NATS.CredsFile); os.IsNotExist(err) {
			return fmt.Errorf("NATS creds file does not exist: %s", cfg.NATS.CredsFile)
		}
	}

	return nil
}

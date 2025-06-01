//file: internal/broker/tls_utils.go

package broker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"rule-router/internal/logger"
)

// TLSConfig represents common TLS configuration
type TLSConfig struct {
	Enable   bool   `json:"enable" yaml:"enable"`
	CertFile string `json:"certFile" yaml:"certFile"`
	KeyFile  string `json:"keyFile" yaml:"keyFile"`
	CAFile   string `json:"caFile" yaml:"caFile"`
	Insecure bool   `json:"insecure" yaml:"insecure"` // Skip certificate verification
}

// CreateTLSConfig creates a *tls.Config from common TLS configuration
// This eliminates duplication between NATS and MQTT broker implementations
func CreateTLSConfig(cfg TLSConfig, logger *logger.Logger, brokerType string) (*tls.Config, error) {
	if !cfg.Enable {
		return nil, nil
	}

	logger.Info("enabling TLS connection",
		"brokerType", brokerType,
		"insecure", cfg.Insecure)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.Insecure,
	}

	// Load client certificates if provided
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s TLS client certificate: %w", brokerType, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		logger.Info("loaded TLS client certificate",
			"brokerType", brokerType,
			"certFile", cfg.CertFile)
	}

	// Load CA certificate if provided
	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s CA certificate: %w", brokerType, err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse %s CA certificate", brokerType)
		}
		tlsConfig.RootCAs = caCertPool
		logger.Info("loaded TLS CA certificate",
			"brokerType", brokerType,
			"caFile", cfg.CAFile)
	}

	return tlsConfig, nil
}

// ValidateTLSConfig validates TLS configuration settings
func ValidateTLSConfig(cfg TLSConfig, brokerType string) error {
	if !cfg.Enable {
		return nil
	}

	// Validate that both cert and key are provided together
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return fmt.Errorf("%s TLS requires both certFile and keyFile to be specified together", brokerType)
	}

	// Check that certificate files exist
	if cfg.CertFile != "" {
		if _, err := os.Stat(cfg.CertFile); os.IsNotExist(err) {
			return fmt.Errorf("%s TLS cert file does not exist: %s", brokerType, cfg.CertFile)
		}
	}

	if cfg.KeyFile != "" {
		if _, err := os.Stat(cfg.KeyFile); os.IsNotExist(err) {
			return fmt.Errorf("%s TLS key file does not exist: %s", brokerType, cfg.KeyFile)
		}
	}

	if cfg.CAFile != "" {
		if _, err := os.Stat(cfg.CAFile); os.IsNotExist(err) {
			return fmt.Errorf("%s TLS CA file does not exist: %s", brokerType, cfg.CAFile)
		}
	}

	return nil
}

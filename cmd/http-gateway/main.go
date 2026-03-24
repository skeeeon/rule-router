// file: cmd/http-gateway/main.go

package main

import (
	"strings"

	flag "github.com/spf13/pflag" // Use pflag aliased as flag
	"github.com/spf13/viper"
	"rule-router/config"
	"rule-router/internal/app"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
)

func main() {
	if err := run(); err != nil {
		logger.NewBootstrapLogger().Fatal("application error", "error", err)
	}
}

func run() error {
	// Parse configuration once - reused across reloads
	cfg, rulesPath := parseFlags()

	// Setup logger early for the lifecycle manager
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		return err
	}
	defer appLogger.Sync()

	// Create app factory function
	createApp := func() (lifecycle.Application, error) {
		// Build the common components using the builder.
		builder := app.NewAppBuilder(cfg, rulesPath).
			WithLogger().
			WithMetrics().
			WithNATSBroker()

		// Choose rule loading strategy: KV-based or file-based
		if cfg.KV.Rules.Enabled {
			builder = builder.WithKVRuleProcessor()
		} else {
			builder = builder.WithRuleProcessor()
		}

		baseApp, err := builder.Build()
		if err != nil {
			return nil, err
		}

		// Create the specific application with the common base.
		return app.NewGatewayApp(baseApp, cfg)
	}

	// Run with reload support (handles SIGHUP automatically)
	return lifecycle.RunWithReload(createApp, appLogger)
}

// parseFlags parses command line arguments and loads config via Viper
func parseFlags() (*config.Config, string) {
	// Define flags
	configPath := flag.String("config", "config/http-gateway.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")

	// Define override flags (consistent with rule-router)
	flag.String("nats-urls", "", "Comma-separated NATS server URLs to override config")
	flag.Bool("metrics-enabled", true, "Override enabling of metrics server")
	flag.String("metrics-addr", "", "Override metrics server address")
	flag.String("metrics-path", "", "Override metrics endpoint path")
	flag.String("log-level", "", "Override log level (debug, info, warn, error)")

	flag.Parse()

	// Bind flags to Viper
	v := viper.GetViper()
	v.BindPFlag("nats.urls", flag.Lookup("nats-urls"))
	v.BindPFlag("metrics.enabled", flag.Lookup("metrics-enabled"))
	v.BindPFlag("metrics.address", flag.Lookup("metrics-addr"))
	v.BindPFlag("metrics.path", flag.Lookup("metrics-path"))
	v.BindPFlag("logging.level", flag.Lookup("log-level"))

	// Special handling for comma-separated nats.urls string from flag/env
	if natsURLs := v.GetString("nats.urls"); natsURLs != "" {
		v.Set("nats.urls", strings.Split(natsURLs, ","))
	}

	// Load configuration using the new Viper-powered loader for the gateway
	cfg, err := config.LoadHTTPConfig(*configPath)
	if err != nil {
		logger.NewBootstrapLogger().Fatal("failed to load config", "error", err)
	}

	return cfg, *rulesPath
}

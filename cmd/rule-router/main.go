//file: cmd/rule-router/main.go

package main

import (
	"flag"
	"log"

	"rule-router/config"
	"rule-router/internal/app"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse configuration once - reused across reloads
	cfg, rulesPath := parseFlags()

	// Setup logger
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		return err
	}
	defer appLogger.Sync()

	// Create app factory function
	createApp := func() (lifecycle.Application, error) {
		return app.NewRouterApp(cfg, rulesPath)
	}

	// Run with reload support (handles SIGHUP automatically)
	return lifecycle.RunWithReload(createApp, appLogger)
}

// parseFlags parses command line arguments and applies overrides
func parseFlags() (*config.Config, string) {
	configPath := flag.String("config", "config/config.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")

	// Metrics overrides
	metricsAddrOverride := flag.String("metrics-addr", "", "override metrics server address (empty = use config)")
	metricsPathOverride := flag.String("metrics-path", "", "override metrics endpoint path (empty = use config)")
	metricsIntervalOverride := flag.Duration("metrics-interval", 0, "override metrics collection interval (0 = use config)")

	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Apply command line overrides (metrics only)
	cfg.ApplyOverrides(
		*metricsAddrOverride,
		*metricsPathOverride,
		*metricsIntervalOverride,
	)

	return cfg, *rulesPath
}

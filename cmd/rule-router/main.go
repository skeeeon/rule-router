//file: cmd/rule-router/main.go

package main

import (
	"context"
	"flag"
	"log"

	"rule-router/config"
	"rule-router/internal/app"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags and load config
	cfg, rulesPath := parseFlags()

	// Create app instance
	application, err := app.NewApp(cfg, rulesPath)
	if err != nil {
		return err
	}
	defer application.Close()

	// Run the application
	return application.Run(context.Background())
}

// parseFlags parses command line arguments and applies overrides
func parseFlags() (*config.Config, string) {
	configPath := flag.String("config", "config/config.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")
	workersOverride := flag.Int("workers", 0, "override number of worker threads (0 = use config)")
	queueSizeOverride := flag.Int("queue-size", 0, "override size of processing queue (0 = use config)")
	batchSizeOverride := flag.Int("batch-size", 0, "override message batch size (0 = use config)")
	metricsAddrOverride := flag.String("metrics-addr", "", "override metrics server address (empty = use config)")
	metricsPathOverride := flag.String("metrics-path", "", "override metrics endpoint path (empty = use config)")
	metricsIntervalOverride := flag.Duration("metrics-interval", 0, "override metrics collection interval (0 = use config)")

	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Apply command line overrides
	cfg.ApplyOverrides(
		*workersOverride,
		*queueSizeOverride,
		*batchSizeOverride,
		*metricsAddrOverride,
		*metricsPathOverride,
		*metricsIntervalOverride,
	)

	return cfg, *rulesPath
}

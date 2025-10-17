// file: cmd/http-gateway/main.go

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
		return app.NewGatewayApp(cfg, rulesPath)
	}

	// Run with reload support (handles SIGHUP automatically)
	return lifecycle.RunWithReload(createApp, appLogger)
}

// parseFlags parses command line arguments
func parseFlags() (*config.Config, string) {
	configPath := flag.String("config", "config/http-gateway.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")

	flag.Parse()

	// Load configuration (validates HTTP fields are present)
	cfg, err := config.LoadHTTPConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	return cfg, *rulesPath
}

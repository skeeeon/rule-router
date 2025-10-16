// file: cmd/http-gateway/main.go

package main

import (
	"context"
	"flag"
	"log"

	"rule-router/config"
	"rule-router/internal/gateway"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags
	configPath := flag.String("config", "config/http-gateway.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")
	flag.Parse()

	// Load configuration (validates HTTP fields are present)
	cfg, err := config.LoadHTTPConfig(*configPath)
	if err != nil {
		return err
	}

	// Create application
	app, err := gateway.NewApp(cfg, *rulesPath)
	if err != nil {
		return err
	}
	defer app.Close()

	// Run application
	return app.Run(context.Background())
}

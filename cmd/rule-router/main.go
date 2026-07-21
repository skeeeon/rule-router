// file: cmd/rule-router/main.go

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"
	"rule-router/config"
	"rule-router/internal/app"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

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

	// Log enabled features
	var features []string
	if cfg.Features.Router {
		features = append(features, "router")
	}
	if cfg.Features.Gateway {
		features = append(features, "gateway")
	}
	if cfg.Features.Scheduler {
		features = append(features, "scheduler")
	}
	appLogger.Info("rule-router starting", "version", version, "features", features)

	// Create app factory function
	createApp := func() (lifecycle.Application, error) {
		// Build the common components using the builder
		builder := app.NewAppBuilder(cfg, rulesPath).
			WithLogger().
			WithMetrics().
			WithNATSBroker()

		// Choose rule loading strategy: KV-based or file-based
		if cfg.KV.Rules.Enabled {
			builder = builder.WithKVRuleProcessor().WithKVRuleManager()
		} else {
			builder = builder.WithRuleProcessor()
		}

		baseApp, err := builder.Build()
		if err != nil {
			return nil, err
		}

		// Create enabled feature apps
		var apps []lifecycle.Application

		if cfg.Features.Router {
			routerApp, err := app.NewRouterApp(baseApp, cfg)
			if err != nil {
				return nil, fmt.Errorf("failed to create router: %w", err)
			}
			apps = append(apps, routerApp)
		}

		if cfg.Features.Gateway {
			gatewayApp, err := app.NewGatewayApp(baseApp, cfg)
			if err != nil {
				return nil, fmt.Errorf("failed to create gateway: %w", err)
			}
			apps = append(apps, gatewayApp)
		}

		if cfg.Features.Scheduler {
			schedulerApp, err := app.NewSchedulerApp(baseApp, cfg)
			if err != nil {
				return nil, fmt.Errorf("failed to create scheduler: %w", err)
			}
			apps = append(apps, schedulerApp)
		}

		// Wait for KV rules to sync after all callbacks are registered
		if cfg.KV.Rules.Enabled && baseApp.RuleKVManager != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := baseApp.RuleKVManager.WaitReady(ctx); err != nil {
				return nil, fmt.Errorf("timeout waiting for initial KV rule sync: %w", err)
			}
		}

		// Startup is complete: broker connected and initial KV sync (if any) done.
		// The /ready probe now reports ready (and tracks the live NATS connection).
		baseApp.MarkReady()

		return app.NewCompositeApp(apps, baseApp), nil
	}

	// validate is called before each SIGHUP reload tears down the running app.
	// In file mode it loads and validates the rules directory into a throwaway
	// processor; on failure the reload is aborted and the current app keeps
	// serving (fail-safe reload). KV-mode reloads come from the KV watcher, which
	// is already fail-safe per key, so there is nothing to pre-check here.
	validate := func() error {
		if cfg.KV.Rules.Enabled {
			return nil
		}
		kvBuckets := []string{}
		if cfg.KV.Enabled {
			kvBuckets = cfg.KV.BucketNames()
		}
		rules, err := rule.NewRulesLoader(appLogger, kvBuckets).LoadFromDirectory(rulesPath)
		if err != nil {
			return err
		}
		return rule.NewProcessor(appLogger, nil, nil, nil).LoadRules(rules)
	}

	// Run with reload support (handles SIGHUP automatically)
	return lifecycle.RunWithReload(createApp, validate, appLogger)
}

// parseFlags parses command line arguments and loads config via Viper
func parseFlags() (*config.Config, string) {
	configPath := flag.String("config", "config/rule-router.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")
	showVersion := flag.Bool("version", false, "print version and exit")

	flag.Parse()

	if *showVersion {
		fmt.Println("rule-router", version)
		os.Exit(0)
	}

	// Overrides come from RR_* environment variables (e.g. RR_NATS_URLS,
	// RR_LOGGING_LEVEL), layered over the config file in config.Load.
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.NewBootstrapLogger().Fatal("failed to load config", "error", err)
	}

	return cfg, *rulesPath
}

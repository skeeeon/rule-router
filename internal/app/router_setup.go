// file: internal/app/router_setup.go

package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"rule-router/internal/broker"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

func (a *RouterApp) setupLogger() error {
	var err error
	a.logger, err = logger.NewLogger(&a.config.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

func (a *RouterApp) setupMetrics() error {
	if !a.config.Metrics.Enabled {
		a.logger.Info("metrics disabled")
		return nil
	}

	reg := prometheus.NewRegistry()
	var err error
	a.metrics, err = metrics.NewMetrics(reg)
	if err != nil {
		return fmt.Errorf("failed to create metrics service: %w", err)
	}

	updateInterval, err := time.ParseDuration(a.config.Metrics.UpdateInterval)
	if err != nil {
		return fmt.Errorf("invalid metrics update interval: %w", err)
	}

	a.metricsCollector = metrics.NewMetricsCollector(a.metrics, updateInterval)
	a.metricsCollector.Start()

	if err := a.setupMetricsServer(reg); err != nil {
		return fmt.Errorf("failed to setup metrics server: %w", err)
	}

	a.logger.Info("metrics initialized successfully",
		"address", a.config.Metrics.Address,
		"path", a.config.Metrics.Path,
		"updateInterval", updateInterval)

	return nil
}

func (a *RouterApp) setupMetricsServer(reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle(a.config.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))

	a.httpServer = &http.Server{
		Addr:    a.config.Metrics.Address,
		Handler: mux,
	}

	go func() {
		a.logger.Info("starting metrics server",
			"address", a.config.Metrics.Address,
			"path", a.config.Metrics.Path)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("metrics server error", "error", err)
		}
	}()

	return nil
}

func (a *RouterApp) setupRules() error {
	kvBuckets := []string{}
	if a.config.KV.Enabled {
		kvBuckets = a.config.KV.Buckets
	}

	rulesLoader := rule.NewRulesLoader(a.logger, kvBuckets)
	rules, err := rulesLoader.LoadFromDirectory(a.rulesPath)
	if err != nil {
		return fmt.Errorf("failed to load rules: %w", err)
	}

	var kvContext *rule.KVContext
	if a.config.KV.Enabled && a.broker != nil {
		kvStores := a.broker.GetKVStores()
		localKVCache := a.broker.GetLocalKVCache()

		kvContext = rule.NewKVContext(kvStores, a.logger, localKVCache)

		a.logger.Info("KV context created with local cache support",
			"bucketCount", len(kvStores),
			"localCacheEnabled", localKVCache.IsEnabled())
	} else {
		a.logger.Info("KV support disabled or NATS broker not ready")
	}

	// Create signature verification config
	var sigVerification *rule.SignatureVerification
	if a.config.Security.Verification.Enabled {
		sigVerification = rule.NewSignatureVerification(
			a.config.Security.Verification.Enabled,
			a.config.Security.Verification.PublicKeyHeader,
			a.config.Security.Verification.SignatureHeader,
		)
		a.logger.Info("signature verification enabled",
			"publicKeyHeader", a.config.Security.Verification.PublicKeyHeader,
			"signatureHeader", a.config.Security.Verification.SignatureHeader)
	} else {
		a.logger.Info("signature verification disabled")
	}

	// Create rule processor with signature verification
	a.processor = rule.NewProcessor(a.logger, a.metrics, kvContext, sigVerification)

	if err := a.processor.LoadRules(rules); err != nil {
		return fmt.Errorf("failed to load rules into processor: %w", err)
	}

	a.logger.Info("rules loaded successfully",
		"ruleCount", len(rules),
		"kvEnabled", a.config.KV.Enabled,
		"signatureVerificationEnabled", a.config.Security.Verification.Enabled)

	return nil
}

func (a *RouterApp) setupNATSBroker() error {
	a.logger.Info("connecting to NATS JetStream server", "urls", a.config.NATS.URLs)

	natsBroker, err := broker.NewNATSBroker(a.config, a.logger, a.metrics)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS JetStream server: %w", err)
	}
	a.broker = natsBroker

	if a.config.KV.Enabled {
		a.logger.Info("initializing local KV cache",
			"buckets", a.config.KV.Buckets,
			"localCacheEnabled", a.config.KV.LocalCache.Enabled)

		if err := a.broker.InitializeKVCache(); err != nil {
			a.logger.Error("failed to initialize local KV cache", "error", err)
			a.logger.Info("continuing with direct NATS KV access (degraded performance)")
		} else {
			localCache := a.broker.GetLocalKVCache()
			if localCache != nil && localCache.IsEnabled() {
				stats := localCache.GetStats()
				a.logger.Info("local KV cache initialized successfully", "stats", stats)
			}
		}
	}

	if a.config.KV.Enabled {
		kvStores := a.broker.GetKVStores()
		localCache := a.broker.GetLocalKVCache()

		a.logger.Info("NATS JetStream connected with KV support",
			"kvBuckets", len(kvStores),
			"configuredBuckets", a.config.KV.Buckets,
			"localCacheEnabled", localCache != nil && localCache.IsEnabled())
	} else {
		a.logger.Info("NATS JetStream connected without KV support")
	}

	return nil
}

func (a *RouterApp) setupSubscriptions() error {
	subjects := a.processor.GetSubjects()
	a.logger.Info("setting up subscriptions for rule subjects", "subjectCount", len(subjects))

	if err := a.broker.ValidateSubjects(subjects); err != nil {
		return fmt.Errorf("stream validation failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, subject := range subjects {
		a.logger.Debug("setting up subscription for subject", "subject", subject)

		if err := a.broker.CreateConsumerForSubject(subject); err != nil {
			return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
		}

		if err := a.broker.AddSubscription(subject); err != nil {
			return fmt.Errorf("failed to add subscription for subject '%s': %w", subject, err)
		}

		a.logger.Info("subscription configured", "subject", subject)

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout during subscription setup")
		default:
		}
	}

	a.logger.Info("all subscriptions configured successfully", "subscriptionCount", len(subjects))
	return nil
}

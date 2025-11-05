// file: internal/authmgr/manager.go

package authmgr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rule-router/internal/authmgr/providers"
	"rule-router/internal/logger"
)

// Manager orchestrates authentication providers and token storage
type Manager struct {
	nats      *NATSClient
	providers []providers.Provider
	logger    *logger.Logger
	metrics   *Metrics
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewManager creates a new authentication manager
func NewManager(nats *NATSClient, providerList []providers.Provider, log *logger.Logger, metrics *Metrics) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	return &Manager{
		nats:      nats,
		providers: providerList,
		logger:    log,
		metrics:   metrics,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins the refresh loops for all providers
func (m *Manager) Start() error {
	m.logger.Info("starting authentication manager", "providers", len(m.providers))

	// Start one goroutine per provider
	for _, p := range m.providers {
		m.wg.Add(1)
		go m.refreshLoop(p)
	}

	return nil
}

// refreshLoop manages authentication for a single provider
func (m *Manager) refreshLoop(p providers.Provider) {
	defer m.wg.Done()

	providerID := p.ID()
	m.logger.Info("starting refresh loop", "provider", providerID)

	// Authenticate immediately on startup
	if err := m.authenticate(p); err != nil {
		m.logger.Error("initial authentication failed",
			"provider", providerID,
			"error", err,
			"willRetry", true)
	}

	// Setup refresh ticker
	interval := p.RefreshInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	m.logger.Info("refresh schedule established",
		"provider", providerID,
		"interval", interval)

	// Refresh loop
	for {
		select {
		case <-m.ctx.Done():
			m.logger.Info("refresh loop stopped", "provider", providerID)
			return

		case <-ticker.C:
			if err := m.authenticate(p); err != nil {
				m.logger.Error("authentication failed during refresh",
					"provider", providerID,
					"error", err,
					"nextRetry", interval)
			} else {
				// Update ticker in case refresh interval changed (OAuth2 token expiry)
				newInterval := p.RefreshInterval()
				if newInterval != interval {
					m.logger.Info("refresh interval updated",
						"provider", providerID,
						"oldInterval", interval,
						"newInterval", newInterval)
					interval = newInterval
					ticker.Reset(interval)
				}
			}
		}
	}
}

// authenticate performs authentication and stores the token
func (m *Manager) authenticate(p providers.Provider) error {
	providerID := p.ID()
	start := time.Now()

	m.logger.Debug("authenticating", "provider", providerID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()

	// Get token from provider
	token, err := p.GetToken(ctx)
	if err != nil {
		if m.metrics != nil {
			m.metrics.IncAuthFailure(providerID)
		}
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Store token in NATS KV
	if err := m.nats.StoreToken(ctx, providerID, token); err != nil {
		if m.metrics != nil {
			m.metrics.IncKVStoreFailure(providerID)
		}
		return fmt.Errorf("failed to store token: %w", err)
	}

	duration := time.Since(start)
	m.logger.Info("authentication successful",
		"provider", providerID,
		"duration", duration)

	if m.metrics != nil {
		m.metrics.IncAuthSuccess(providerID)
		m.metrics.ObserveAuthDuration(providerID, duration.Seconds())
	}

	return nil
}

// Stop gracefully shuts down all refresh loops
func (m *Manager) Stop() error {
	m.logger.Info("stopping authentication manager")

	// Cancel all refresh loops
	m.cancel()

	// Wait for all goroutines to finish
	m.wg.Wait()

	m.logger.Info("authentication manager stopped")
	return nil
}

// file: internal/authmgr/providers/oauth2.go

package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2/clientcredentials"
)

// Timeout constants for OAuth2 provider
const (
	// minRefreshInterval is the minimum interval between token refresh attempts
	minRefreshInterval = 1 * time.Minute
)

// OAuth2Provider implements OAuth2 client credentials authentication
type OAuth2Provider struct {
	id            string
	config        *clientcredentials.Config
	refreshBuffer time.Duration // Refresh this much before expiry

	// Cache the token expiry to avoid unnecessary auth calls in RefreshInterval()
	mu           sync.RWMutex
	cachedExpiry time.Time
}

// NewOAuth2Provider creates a new OAuth2 provider
func NewOAuth2Provider(id, tokenURL, clientID, clientSecret string, scopes []string, refreshBuffer time.Duration) *OAuth2Provider {
	config := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       scopes,
	}

	return &OAuth2Provider{
		id:            id,
		config:        config,
		refreshBuffer: refreshBuffer,
	}
}

// ID returns the provider identifier
func (p *OAuth2Provider) ID() string {
	return p.id
}

// GetToken authenticates and returns an access token
func (p *OAuth2Provider) GetToken(ctx context.Context) (string, error) {
	// Use the library to handle OAuth2 flow
	token, err := p.config.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("oauth2 authentication failed: %w", err)
	}

	if token.AccessToken == "" {
		return "", fmt.Errorf("received empty access token")
	}

	// Cache the expiry time for efficient RefreshInterval() calls
	p.mu.Lock()
	p.cachedExpiry = token.Expiry
	p.mu.Unlock()

	return token.AccessToken, nil
}

// RefreshInterval returns how often to re-authenticate
// For OAuth2, we use the token expiry minus the refresh buffer
func (p *OAuth2Provider) RefreshInterval() time.Duration {
	// Check if we have a cached expiry from a previous GetToken call
	p.mu.RLock()
	cachedExpiry := p.cachedExpiry
	p.mu.RUnlock()

	if !cachedExpiry.IsZero() {
		// Calculate time until expiry
		timeUntilExpiry := time.Until(cachedExpiry)

		// Refresh before expiry by the buffer amount
		refreshInterval := timeUntilExpiry - p.refreshBuffer

		// Ensure minimum refresh interval
		if refreshInterval < minRefreshInterval {
			refreshInterval = minRefreshInterval
		}

		return refreshInterval
	}

	// No cached expiry yet (first call before GetToken)
	// Return refresh buffer as initial interval
	// This will be updated after the first authentication
	return p.refreshBuffer
}


// file: internal/authmgr/providers/provider.go

package providers

import (
	"context"
	"time"
)

// Provider represents an authentication strategy
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string

	// GetToken authenticates and returns an access token
	// Always performs full authentication (no refresh token logic)
	GetToken(ctx context.Context) (string, error)

	// RefreshInterval returns how often to re-authenticate
	RefreshInterval() time.Duration
}

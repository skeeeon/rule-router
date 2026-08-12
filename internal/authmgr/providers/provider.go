// Package providers holds the token sources authmgr can refresh from: an OAuth2
// client-credentials provider and a generic HTTP provider for APIs that issue
// tokens through a bespoke endpoint.
//
// Each implementation satisfies Provider, so adding a source means adding a
// file here rather than touching the manager.
package providers

import (
	"context"
	"time"
)

// Provider represents an authentication strategy
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string

	// Token authenticates and returns an access token
	// Always performs full authentication (no refresh token logic)
	Token(ctx context.Context) (string, error)

	// RefreshInterval returns how often to re-authenticate
	RefreshInterval() time.Duration
}

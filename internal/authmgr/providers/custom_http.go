// file: internal/authmgr/providers/custom_http.go

package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// CustomHTTPProvider implements authentication via custom HTTP endpoints
type CustomHTTPProvider struct {
	id            string
	authURL       string
	method        string
	headers       map[string]string
	bodyTemplate  string // Body with ${VAR} placeholders
	tokenPath     string // JSON path to extract token (e.g., "data.token")
	refreshEvery  time.Duration
	httpClient    *http.Client
}

var envVarPattern = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// NewCustomHTTPProvider creates a new custom HTTP provider
func NewCustomHTTPProvider(
	id, authURL, method string,
	headers map[string]string,
	bodyTemplate, tokenPath string,
	refreshEvery time.Duration,
) *CustomHTTPProvider {
	return &CustomHTTPProvider{
		id:           id,
		authURL:      authURL,
		method:       method,
		headers:      headers,
		bodyTemplate: bodyTemplate,
		tokenPath:    tokenPath,
		refreshEvery: refreshEvery,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ID returns the provider identifier
func (p *CustomHTTPProvider) ID() string {
	return p.id
}

// GetToken performs HTTP authentication and extracts the token
func (p *CustomHTTPProvider) GetToken(ctx context.Context) (string, error) {
	// Expand environment variables in body template
	body := p.expandEnvVars(p.bodyTemplate)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, p.method, p.authURL, bytes.NewBufferString(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers
	for key, value := range p.headers {
		req.Header.Set(key, value)
	}

	// Execute request
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse JSON response
	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Extract token using JSON path
	token, err := p.extractJSONPath(result, p.tokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to extract token at path '%s': %w", p.tokenPath, err)
	}

	if token == "" {
		return "", fmt.Errorf("extracted token is empty")
	}

	return token, nil
}

// RefreshInterval returns how often to re-authenticate
func (p *CustomHTTPProvider) RefreshInterval() time.Duration {
	return p.refreshEvery
}

// expandEnvVars replaces ${VAR} with environment variable values
func (p *CustomHTTPProvider) expandEnvVars(template string) string {
	return envVarPattern.ReplaceAllStringFunc(template, func(match string) string {
		// Extract variable name from ${VAR}
		varName := match[2 : len(match)-1]
		return os.Getenv(varName)
	})
}

// extractJSONPath extracts a value from parsed JSON using dot notation
// Supports simple paths like "token" or "data.access_token"
func (p *CustomHTTPProvider) extractJSONPath(data interface{}, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	parts := strings.Split(path, ".")
	current := data

	for i, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			val, exists := v[part]
			if !exists {
				return "", fmt.Errorf("key '%s' not found at path segment %d", part, i)
			}
			current = val
		default:
			return "", fmt.Errorf("cannot traverse into %T at path segment %d", current, i)
		}
	}

	// Convert final value to string
	switch v := current.(type) {
	case string:
		return v, nil
	case float64:
		return fmt.Sprintf("%.0f", v), nil
	case bool:
		return fmt.Sprintf("%t", v), nil
	default:
		return "", fmt.Errorf("final value is not a string, got %T", current)
	}
}

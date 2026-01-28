// file: internal/authmgr/providers/custom_http_test.go

package providers

import (
	"testing"
	"time"
)

func TestExtractJSONPath(t *testing.T) {
	// Create a minimal provider for testing the extractJSONPath method
	provider := NewCustomHTTPProvider("test", "http://example.com", "POST", nil, "", "", time.Hour)

	tests := []struct {
		name    string
		data    interface{}
		path    string
		want    string
		wantErr bool
	}{
		// Simple paths
		{
			name:    "simple string field",
			data:    map[string]interface{}{"token": "abc123"},
			path:    "token",
			want:    "abc123",
			wantErr: false,
		},
		{
			name:    "simple number field",
			data:    map[string]interface{}{"count": float64(42)},
			path:    "count",
			want:    "42",
			wantErr: false,
		},
		{
			name:    "simple boolean field",
			data:    map[string]interface{}{"active": true},
			path:    "active",
			want:    "true",
			wantErr: false,
		},
		{
			name:    "boolean false field",
			data:    map[string]interface{}{"active": false},
			path:    "active",
			want:    "false",
			wantErr: false,
		},

		// Nested paths
		{
			name: "nested string field",
			data: map[string]interface{}{
				"data": map[string]interface{}{
					"access_token": "xyz789",
				},
			},
			path:    "data.access_token",
			want:    "xyz789",
			wantErr: false,
		},
		{
			name: "deeply nested field",
			data: map[string]interface{}{
				"response": map[string]interface{}{
					"auth": map[string]interface{}{
						"credentials": map[string]interface{}{
							"token": "deep_token",
						},
					},
				},
			},
			path:    "response.auth.credentials.token",
			want:    "deep_token",
			wantErr: false,
		},

		// Float formatting
		{
			name:    "float with decimals truncated",
			data:    map[string]interface{}{"value": float64(123.999)},
			path:    "value",
			want:    "124", // %.0f rounds
			wantErr: false,
		},
		{
			name:    "large number",
			data:    map[string]interface{}{"id": float64(9999999999)},
			path:    "id",
			want:    "9999999999",
			wantErr: false,
		},

		// Error cases
		{
			name:    "empty path",
			data:    map[string]interface{}{"token": "abc"},
			path:    "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "missing key",
			data:    map[string]interface{}{"token": "abc"},
			path:    "missing",
			want:    "",
			wantErr: true,
		},
		{
			name: "missing nested key",
			data: map[string]interface{}{
				"data": map[string]interface{}{
					"token": "abc",
				},
			},
			path:    "data.missing",
			want:    "",
			wantErr: true,
		},
		{
			name: "traverse into non-map",
			data: map[string]interface{}{
				"data": "string_not_map",
			},
			path:    "data.token",
			want:    "",
			wantErr: true,
		},
		{
			name: "traverse into number",
			data: map[string]interface{}{
				"count": float64(42),
			},
			path:    "count.value",
			want:    "",
			wantErr: true,
		},
		{
			name: "final value is array",
			data: map[string]interface{}{
				"tokens": []interface{}{"a", "b", "c"},
			},
			path:    "tokens",
			want:    "",
			wantErr: true,
		},
		{
			name: "final value is nested object",
			data: map[string]interface{}{
				"data": map[string]interface{}{
					"nested": map[string]interface{}{
						"value": "test",
					},
				},
			},
			path:    "data.nested",
			want:    "",
			wantErr: true,
		},
		{
			name:    "nil data",
			data:    nil,
			path:    "token",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := provider.extractJSONPath(tt.data, tt.path)

			if tt.wantErr {
				if err == nil {
					t.Errorf("extractJSONPath() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("extractJSONPath() unexpected error: %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("extractJSONPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomHTTPProvider_ID(t *testing.T) {
	provider := NewCustomHTTPProvider("my-provider", "http://example.com", "POST", nil, "", "token", time.Hour)

	if got := provider.ID(); got != "my-provider" {
		t.Errorf("ID() = %q, want %q", got, "my-provider")
	}
}

func TestCustomHTTPProvider_RefreshInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
	}{
		{"one hour", time.Hour},
		{"30 minutes", 30 * time.Minute},
		{"5 seconds", 5 * time.Second},
		{"zero", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewCustomHTTPProvider("test", "http://example.com", "POST", nil, "", "token", tt.interval)

			if got := provider.RefreshInterval(); got != tt.interval {
				t.Errorf("RefreshInterval() = %v, want %v", got, tt.interval)
			}
		})
	}
}

func TestNewCustomHTTPProvider(t *testing.T) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer test",
	}

	provider := NewCustomHTTPProvider(
		"test-id",
		"http://auth.example.com/token",
		"POST",
		headers,
		`{"grant_type": "client_credentials"}`,
		"access_token",
		time.Hour,
	)

	if provider.id != "test-id" {
		t.Errorf("id = %q, want %q", provider.id, "test-id")
	}
	if provider.authURL != "http://auth.example.com/token" {
		t.Errorf("authURL = %q, want %q", provider.authURL, "http://auth.example.com/token")
	}
	if provider.method != "POST" {
		t.Errorf("method = %q, want %q", provider.method, "POST")
	}
	if provider.tokenPath != "access_token" {
		t.Errorf("tokenPath = %q, want %q", provider.tokenPath, "access_token")
	}
	if provider.refreshEvery != time.Hour {
		t.Errorf("refreshEvery = %v, want %v", provider.refreshEvery, time.Hour)
	}
	if provider.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
	if len(provider.headers) != 2 {
		t.Errorf("headers length = %d, want 2", len(provider.headers))
	}
}


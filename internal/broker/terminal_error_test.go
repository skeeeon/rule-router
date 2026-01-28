// file: internal/broker/terminal_error_test.go

package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// createJSONSyntaxError creates a real json.SyntaxError by attempting to parse invalid JSON
func createJSONSyntaxError() error {
	var v interface{}
	return json.Unmarshal([]byte(`{invalid json`), &v)
}

// createJSONUnmarshalTypeError creates a real json.UnmarshalTypeError by parsing wrong types
func createJSONUnmarshalTypeError() error {
	var v int
	return json.Unmarshal([]byte(`"not a number"`), &v)
}

func TestIsTerminalError(t *testing.T) {
	// Pre-create real JSON errors for testing
	jsonSyntaxErr := createJSONSyntaxError()
	jsonTypeErr := createJSONUnmarshalTypeError()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Custom terminal error types
		{
			name: "ErrMalformedJSON is terminal",
			err:  ErrMalformedJSON,
			want: true,
		},
		{
			name: "ErrInvalidPayload is terminal",
			err:  ErrInvalidPayload,
			want: true,
		},
		{
			name: "wrapped ErrMalformedJSON is terminal",
			err:  fmt.Errorf("processing failed: %w", ErrMalformedJSON),
			want: true,
		},
		{
			name: "wrapped ErrInvalidPayload is terminal",
			err:  fmt.Errorf("validation failed: %w", ErrInvalidPayload),
			want: true,
		},

		// Standard library JSON errors
		{
			name: "json.SyntaxError is terminal",
			err:  jsonSyntaxErr,
			want: true,
		},
		{
			name: "json.UnmarshalTypeError is terminal",
			err:  jsonTypeErr,
			want: true,
		},
		{
			name: "wrapped json.SyntaxError is terminal",
			err:  fmt.Errorf("parse error: %w", jsonSyntaxErr),
			want: true,
		},

		// Non-terminal errors (should be retried)
		{
			name: "generic error is not terminal",
			err:  errors.New("connection reset"),
			want: false,
		},
		{
			name: "timeout error is not terminal",
			err:  errors.New("context deadline exceeded"),
			want: false,
		},
		{
			name: "network error is not terminal",
			err:  fmt.Errorf("dial tcp: connection refused"),
			want: false,
		},
		{
			name: "nil error is not terminal",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTerminalError(tt.err)
			if got != tt.want {
				t.Errorf("isTerminalError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsTerminalError_DoublyWrappedErrors verifies that deeply wrapped errors are still detected
func TestIsTerminalError_DoublyWrappedErrors(t *testing.T) {
	// Create a doubly-wrapped terminal error
	innerErr := ErrMalformedJSON
	wrappedOnce := fmt.Errorf("layer 1: %w", innerErr)
	wrappedTwice := fmt.Errorf("layer 2: %w", wrappedOnce)

	if !isTerminalError(wrappedTwice) {
		t.Error("isTerminalError should detect doubly-wrapped ErrMalformedJSON")
	}

	// Create a doubly-wrapped JSON syntax error
	jsonErr := createJSONSyntaxError()
	jsonWrappedOnce := fmt.Errorf("layer 1: %w", jsonErr)
	jsonWrappedTwice := fmt.Errorf("layer 2: %w", jsonWrappedOnce)

	if !isTerminalError(jsonWrappedTwice) {
		t.Error("isTerminalError should detect doubly-wrapped json.SyntaxError")
	}
}


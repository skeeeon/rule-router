package broker

import (
	"errors"
	"fmt"
	"testing"

	"rule-router/internal/httpclient"
)

// TestIsPermanent pins that the classification survives the wrapping an action
// error picks up on its way out of executeAction, so a 4xx terminates the
// message instead of NAKing it.
func TestIsPermanent(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("failed to execute HTTP action: %w",
			fmt.Errorf("HTTP request failed after 1 attempts: %w", err))
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"404", wrap(&httpclient.StatusError{StatusCode: 404}), true},
		{"429", wrap(&httpclient.StatusError{StatusCode: 429}), false},
		{"503", wrap(&httpclient.StatusError{StatusCode: 503}), false},
		{"transport", wrap(errors.New("connection refused")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanent(tt.err); got != tt.want {
				t.Errorf("isPermanent() = %v, want %v", got, tt.want)
			}
		})
	}
}

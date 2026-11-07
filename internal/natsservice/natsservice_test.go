package natsservice

import "testing"

func TestVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"semver", "1.2.3", "1.2.3"},
		{"leading v dropped", "v1.2.3", "1.2.3"},
		{"pre-release kept", "1.2.3-rc.1", "1.2.3-rc.1"},
		{"unstamped dev build", "dev", "0.0.0-dev"},
		{"missing patch", "1.2", "0.0.0-dev"},
		{"empty", "", "0.0.0-dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Version(tt.in); got != tt.want {
				t.Errorf("Version(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestKVPush_RejectsEnvRefs pins that `kv push` validates as the router's KV
// loader does: a rule using ${VAR} fails locally, before anything is uploaded.
// Both cases run with --dry-run, which validates and then stops short of NATS.
func TestKVPush_RejectsEnvRefs(t *testing.T) {
	tests := []struct {
		name   string
		rule   string
		wantIn string // "" means the push should validate cleanly
	}{
		{
			name: "env ref is rejected",
			rule: `
- trigger: { nats: { subject: "events.in" } }
  action: { nats: { subject: out, payload: '{"t":"${GITHUB_SECRET}"}' } }`,
			wantIn: "${GITHUB_SECRET} in action.nats.payload",
		},
		{
			name: "plain rule validates",
			rule: `
- trigger: { nats: { subject: "events.in" } }
  action: { nats: { subject: out, payload: "{}" } }`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rule.yaml")
			if err := os.WriteFile(path, []byte(tt.rule), 0644); err != nil {
				t.Fatal(err)
			}

			cmd := &cobra.Command{}
			cmd.Flags().Bool("dry-run", true, "")
			err := runKVPush(cmd, []string{path})

			if tt.wantIn == "" {
				if err != nil {
					t.Fatalf("push failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantIn)
			}
		})
	}
}

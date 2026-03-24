// file: cmd/rule-cli/cmd/kv.go

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
	"rule-router/internal/logger"
	"rule-router/internal/rule"
)

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Manage rules stored in NATS KV",
	Long:  `Commands for pushing, listing, and managing rules in a NATS KV bucket.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var kvPushCmd = &cobra.Command{
	Use:   "push <file-or-dir>",
	Short: "Push rule files to a NATS KV bucket",
	Long: `Reads YAML rule files and writes them to the configured NATS KV bucket.
File paths are converted to dotted KV keys:

  sensors/tank.yaml       -> sensors.tank
  alerts/critical.yml     -> alerts.critical
  routing.yaml            -> routing

When given a directory, pushes all *.yaml and *.yml files in that directory
(non-recursive). To push a nested structure, push each directory separately.`,
	Args: cobra.ExactArgs(1),
	RunE: runKVPush,
}

func init() {
	kvPushCmd.Flags().StringP("url", "s", "nats://localhost:4222", "NATS server URL")
	kvPushCmd.Flags().String("creds", "", "NATS credentials file")
	kvPushCmd.Flags().StringP("bucket", "b", "rules", "KV bucket name")
	kvPushCmd.Flags().Bool("dry-run", false, "Show what would be pushed without writing")

	kvCmd.AddCommand(kvPushCmd)
}

func runKVPush(cmd *cobra.Command, args []string) error {
	target := args[0]
	natsURL, _ := cmd.Flags().GetString("url")
	credsFile, _ := cmd.Flags().GetString("creds")
	bucket, _ := cmd.Flags().GetString("bucket")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Collect files to push
	files, baseDir, err := collectFiles(target)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no YAML rule files found in %s", target)
	}

	// Create a rules loader for validation
	log := logger.NewBootstrapLogger()
	rulesLoader := rule.NewRulesLoader(log, []string{})

	// Validate all files first
	type fileToPush struct {
		path string
		key  string
		data []byte
	}

	var pushList []fileToPush

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", file, err)
		}

		key := pathToKey(file, baseDir)

		// Validate the rules
		_, err = rulesLoader.ParseAndValidateYAML(data, file)
		if err != nil {
			return fmt.Errorf("validation failed for %s: %w", file, err)
		}

		pushList = append(pushList, fileToPush{path: file, key: key, data: data})
	}

	// Dry run: just show what would be pushed
	if dryRun {
		fmt.Printf("Would push %d file(s) to KV bucket %q:\n", len(pushList), bucket)
		for _, f := range pushList {
			fmt.Printf("  %s -> %s\n", f.path, f.key)
		}
		return nil
	}

	// Connect to NATS
	var natsOpts []nats.Option
	if credsFile != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(credsFile))
	}

	nc, err := nats.Connect(natsURL, natsOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS at %s: %w", natsURL, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("failed to create JetStream interface: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get or create the KV bucket
	store, err := js.KeyValue(ctx, bucket)
	if err != nil {
		// Try to create it
		store, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		if err != nil {
			return fmt.Errorf("failed to access or create KV bucket %q: %w", bucket, err)
		}
		fmt.Printf("Created KV bucket %q\n", bucket)
	}

	// Push all files
	for _, f := range pushList {
		if _, err := store.Put(ctx, f.key, f.data); err != nil {
			return fmt.Errorf("failed to push %s (key=%s): %w", f.path, f.key, err)
		}
		fmt.Printf("  %s -> %s ✓\n", f.path, f.key)
	}

	fmt.Printf("\nPushed %d rule(s) to KV bucket %q\n", len(pushList), bucket)
	return nil
}

// collectFiles returns YAML files and the base directory for key derivation.
// If target is a file, returns just that file. If a directory, returns all
// *.yaml and *.yml files in that directory (non-recursive).
func collectFiles(target string) ([]string, string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, "", fmt.Errorf("cannot access %s: %w", target, err)
	}

	if !info.IsDir() {
		// Single file — base dir is the file's parent directory
		return []string{target}, filepath.Dir(target), nil
	}

	// Directory — collect YAML files (non-recursive)
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read directory %s: %w", target, err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			files = append(files, filepath.Join(target, entry.Name()))
		}
	}

	return files, target, nil
}

// pathToKey converts a file path to a dotted KV key.
// Strips the extension and replaces path separators with dots.
//
// Examples:
//
//	pathToKey("sensors/tank.yaml", "sensors") -> "tank"
//	pathToKey("sensors/tank.yaml", ".") -> "sensors.tank"
//	pathToKey("alerts/critical.yml", ".") -> "alerts.critical"
func pathToKey(filePath, baseDir string) string {
	rel, err := filepath.Rel(baseDir, filePath)
	if err != nil {
		// Fallback: use the filename without extension
		rel = filepath.Base(filePath)
	}

	// Strip extension
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))

	// Replace path separators with dots
	rel = strings.ReplaceAll(rel, string(filepath.Separator), ".")

	// Also handle forward slashes (cross-platform)
	rel = strings.ReplaceAll(rel, "/", ".")

	return rel
}

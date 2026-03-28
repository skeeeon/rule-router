# KV Rule Store

Rules are standalone YAML files, decoupled from the application binaries. When KV rule storage is enabled, the server watches a NATS KV bucket and hot-reloads on any change — you never restart to deploy new routing logic.

This makes rules ideal for a **GitOps workflow**: author rules in version control, validate in CI with `rule-cli lint` and `rule-cli test`, and push to NATS KV on merge.

-----

## Overview

By default, rules are loaded from YAML files in the `rules/` directory at startup. KV rule storage is an optional alternative that **replaces** file-based loading:

| | File-based (default) | KV-based |
|---|---|---|
| **Rule source** | `rules/` directory | NATS KV bucket |
| **When rules load** | At startup (or SIGHUP reload) | Continuously via KV Watch |
| **Hot reload** | Requires SIGHUP signal | Automatic on any KV change |
| **Dynamic subjects** | Requires restart for new subjects | Consumers/cron jobs created/removed automatically |
| **Best for** | Simple deployments, local dev | GitOps, multi-team, dynamic environments |

Both modes use the exact same YAML rule format. A rule that works from a file works identically from KV.

-----

## Configuration

Enable KV rule storage in your application config:

```yaml
kv:
  enabled: true
  buckets:
    - "device_status"

  rules:
    enabled: true             # Enable KV-based rule storage
    bucket: "rules"           # KV bucket name (default: "rules")
    autoProvision: false      # Auto-create the bucket if it doesn't exist
```

When `kv.rules.enabled` is `true`, the `rules/` directory is ignored. All rules come from the KV bucket.

This works with all rule-based applications:
*   **`rule-router`**: NATS trigger rules are loaded from KV, consumers created dynamically.
*   **`http-gateway`**: Both inbound (HTTP trigger) and outbound (NATS trigger + HTTP action) rules are loaded from KV. HTTP paths are handled dynamically via a catch-all handler.
*   **`rule-scheduler`**: Schedule-triggered rules are loaded from KV. Cron jobs are rebuilt automatically when rules change — no restart or SIGHUP needed.

-----

## Pushing Rules

Use `rule-cli kv push` to upload rule files to the KV bucket:

```bash
# Push a single file
rule-cli kv push sensors/tank.yaml --url nats://localhost:4222

# Push all YAML files in a directory (non-recursive)
rule-cli kv push sensors/ --url nats://localhost:4222

# Push with credentials
rule-cli kv push rules/ --url nats://my-server:4222 --creds /path/to/creds.json

# Preview what would be pushed without writing
rule-cli kv push sensors/ --dry-run
```

### KV Key Derivation

When pushing, the file path determines the KV key:

1. Strip the `.yaml` or `.yml` extension
2. Replace path separators (`/`, `\`) with `.`

| File Path | KV Key |
|---|---|
| `routing.yaml` | `routing` |
| `sensors/tank.yaml` | `sensors.tank` |
| `alerts/critical.yml` | `alerts.critical` |

Your directory structure maps directly to a dotted namespace in the KV store.

### Directory Push Behavior

`rule-cli kv push <dir>` pushes all `*.yaml` and `*.yml` files in the given directory but **does not recurse** into subdirectories. To push a nested structure, push each directory separately:

```bash
rule-cli kv push sensors/   --url $NATS_URL
rule-cli kv push alerts/    --url $NATS_URL
rule-cli kv push webhooks/  --url $NATS_URL
```

-----

## Separate Rules Repository

Rules don't need to live in the same repository as your deployment. A dedicated rules repo keeps routing logic independent and lets domain teams own their rules without access to infrastructure code.

```
my-rules-repo/
├── sensors/
│   ├── tank.yaml
│   └── temperature.yaml
├── alerts/
│   └── critical.yaml
├── webhooks/
│   └── github.yaml
└── README.md
```

### GitOps Workflow

A typical CI/CD pipeline for a rules repository:

```yaml
# .github/workflows/deploy-rules.yml
name: Deploy Rules
on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Build rule-cli
        run: go build -o rule-cli ./cmd/rule-cli

      - name: Lint Rules
        run: ./rule-cli lint --rules .

      - name: Test Rules
        run: ./rule-cli test --rules .

      - name: Push to KV
        run: |
          ./rule-cli kv push sensors/  --url ${{ secrets.NATS_URL }} --creds ${{ secrets.NATS_CREDS }}
          ./rule-cli kv push alerts/   --url ${{ secrets.NATS_URL }} --creds ${{ secrets.NATS_CREDS }}
          ./rule-cli kv push webhooks/ --url ${{ secrets.NATS_URL }} --creds ${{ secrets.NATS_CREDS }}
```

-----

## How It Works

When KV rules are enabled, the application:

1. **Connects** to the configured KV bucket (or creates it if `autoProvision: true`).
2. **Watches** all keys using the NATS KV Watch API.
3. **Initial sync**: Loads all existing rules from the bucket. The application blocks until this completes.
4. **Live updates**: On any key put/delete, the rule set is hot-reloaded:
   - New rules are parsed, validated, and pushed to the processor.
   - New NATS trigger subjects get JetStream consumers created automatically.
   - Deleted rules have their orphaned consumers and subscriptions cleaned up.

### Scheduler Behavior

For `rule-scheduler`, KV rule changes trigger a cron job rebuild rather than subscription changes. When a KV key containing schedule rules is updated or deleted:

1. All existing KV-sourced cron jobs are removed.
2. New cron jobs are registered from the updated rule set.
3. The scheduler continues running — no restart, no gap in execution.

Jobs that are mid-execution when a rebuild occurs are not interrupted.

### Stream Refresh

When a new rule references a NATS subject, the application refreshes its JetStream stream list to pick up any newly created streams. If a stream doesn't exist for a subject, the rule is still loaded but the subscription is skipped with a warning — you can create the stream later and the next rule update will pick it up.

### Validation

Rules pushed to KV go through the same validation pipeline as file-based rules:
- YAML parsing
- Environment variable expansion (`${VAR_NAME}`)
- Structural validation (trigger, conditions, action)
- KV field validation
- Stream subject validation

If a rule fails validation, it is rejected and the previous rules for that key are preserved.

-----

## Managing Rules with `nats` CLI

You can also manage rules directly with the `nats` CLI:

```bash
# Create the rules bucket
nats kv add rules

# Put a rule
nats kv put rules sensors.tank < sensors/tank.yaml

# Get a rule
nats kv get rules sensors.tank

# List all rule keys
nats kv ls rules

# Delete a rule
nats kv del rules sensors.tank

# Watch for changes (useful for debugging)
nats kv watch rules
```

-----

## `kv push` CLI Reference

```
Usage:
  rule-cli kv push <file-or-dir> [flags]

Flags:
  -b, --bucket string   KV bucket name (default "rules")
      --creds string    NATS credentials file
      --dry-run         Show what would be pushed without writing
  -h, --help            help for push
  -s, --url string      NATS server URL (default "nats://localhost:4222")
```

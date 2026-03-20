# Rule Scheduler

Cron-based scheduled message publishing to NATS using the shared rule engine. Define schedule-triggered rules in the same YAML format as `rule-router` and `http-gateway` — with full support for conditions, KV lookups, time-based logic, and templating.

## Features

- **Cron Scheduling**: Standard 5-field cron expressions with optional IANA timezone support.
- **Shared Rule Engine**: Same conditions, templates, KV lookups, and time variables as `rule-router`.
- **JetStream & Core Publishing**: Publish actions via JetStream (reliable) or core NATS (fire-and-forget).
- **Conditional Execution**: Schedule rules can include conditions (e.g., only unlock doors if building status is "occupied").
- **Hot Reload**: `SIGHUP` reloads rules and re-registers cron jobs without downtime.
- **Prometheus Metrics**: Action publish tracking and rule activity metrics.
- **Graceful Shutdown**: Waits for running jobs to complete before exiting.

## Quick Start

### Prerequisites

- Go 1.23+
- NATS Server with JetStream enabled
- JetStream streams configured for your action subjects

### Installation

```bash
# Build
go build -o rule-scheduler ./cmd/rule-scheduler

# Run
./rule-scheduler --config config/rule-scheduler.yaml --rules rules/
```

### Example Rules

Create a file at `rules/scheduler/access-control.yaml`:

```yaml
# Unlock the front door every weekday morning at 8am Eastern
- trigger:
    schedule:
      cron: "0 8 * * 1-5"
      timezone: "America/New_York"
  action:
    nats:
      subject: "access.door.front.command"
      payload: |
        {
          "command": "unlock",
          "source": "rule-scheduler",
          "id": "{@uuid4()}",
          "at": "{@timestamp()}"
        }

# Lock all doors every evening at 6pm Eastern
- trigger:
    schedule:
      cron: "0 18 * * *"
      timezone: "America/New_York"
  action:
    nats:
      subject: "access.door.all.command"
      payload: |
        {
          "command": "lock",
          "source": "rule-scheduler",
          "id": "{@uuid4()}",
          "at": "{@timestamp()}"
        }
```

### Test It

```bash
# Subscribe to the action subjects
nats sub "access.door.>"

# Start the scheduler
./rule-scheduler --config config/rule-scheduler.yaml --rules rules/
```

## Schedule Trigger

The `schedule` trigger uses standard 5-field cron expressions:

```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of week (0-6, Sunday=0)
│ │ │ │ │
* * * * *
```

| Field | Required | Description |
|-------|----------|-------------|
| `cron` | Yes | Standard 5-field cron expression |
| `timezone` | No | IANA timezone (e.g., `America/New_York`). Defaults to system local time. |

**Examples:**

| Expression | Description |
|------------|-------------|
| `*/5 * * * *` | Every 5 minutes |
| `0 8 * * 1-5` | Weekdays at 8:00 AM |
| `0 */2 * * *` | Every 2 hours |
| `30 6 1 * *` | 6:30 AM on the 1st of each month |
| `0 0 * * 0` | Midnight every Sunday |

## Conditional Schedules

Schedule rules support the full conditions engine. When conditions evaluate to false, no actions are published for that execution.

```yaml
# Only unlock if the building is marked as occupied in KV
- trigger:
    schedule:
      cron: "0 8 * * 1-5"
      timezone: "America/New_York"
  conditions:
    operator: and
    items:
      - field: "{@kv.building_status.main:occupied}"
        operator: eq
        value: true
  action:
    nats:
      subject: "access.door.front.command"
      payload: '{"command": "unlock", "id": "{@uuid4()}", "at": "{@timestamp()}"}'
```

### Available Context in Schedule Rules

Since schedule rules have no incoming message, only a subset of system variables are available:

| Available | Variable | Example |
|-----------|----------|---------|
| Yes | `{@time.*}`, `{@day.*}`, `{@date.*}`, `{@timestamp.*}` | Time-based logic |
| Yes | `{@kv.bucket.key}` | KV store lookups |
| Yes | `{@uuid4()}`, `{@uuid7()}`, `{@timestamp()}` | Template functions |
| No | `{fieldName}`, `{@msg.*}` | No incoming message payload |
| No | `{@subject.*}`, `{@header.*}` | No NATS/HTTP trigger context |
| No | `{@path.*}`, `{@method}` | No HTTP context |
| No | `{@signature.*}` | No signature context |

## Configuration

See `config/rule-scheduler.yaml` for a fully documented example.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/rule-scheduler.yaml` | Path to config file |
| `--rules` | `rules` | Path to rules directory |
| `--nats-urls` | | Override NATS server URLs (comma-separated) |
| `--metrics-enabled` | `true` | Enable/disable metrics server |
| `--metrics-addr` | | Override metrics server address |
| `--metrics-path` | | Override metrics endpoint path |
| `--log-level` | | Override log level |

All flags can also be set via environment variables with the `RR_` prefix (e.g., `RR_NATS_URLS`, `RR_LOG_LEVEL`).

### Key Configuration Differences from rule-router

The scheduler only publishes — it does not subscribe to NATS subjects. This means:
- No `consumers` configuration block needed
- No `security.verification` needed (no inbound messages to verify)
- KV is optional (only needed if your schedule rules use KV conditions or templates)

## Advanced Features

The `rule-scheduler` shares its powerful rule engine with `rule-router` and `http-gateway`. For detailed documentation:

*   **[Core Concepts](./../../docs/01-core-concepts.md)**: Triggers, Conditions, Actions, and Environment Variables.
*   **[System Variables & Functions](./../../docs/02-system-variables.md)**: Full reference for all `@` variables and functions.
*   **[Array Processing](./../../docs/03-array-processing.md)**: Guide to using `forEach` and array operators.

## Architecture

```
                                    ┌──────────────────────────────┐
                                    │        NATS JetStream        │
                                    │  ┌──────────┐  ┌──────────┐ │
                                    │  │ Streams  │  │ KV Stores│ │
                                    │  └────▲─────┘  └────┬─────┘ │
                                    └───────┼─────────────┼───────┘
                                            │             │
┌───────────────────────────────────────────┼─────────────┼───────┐
│                   Rule Scheduler          │             │       │
│  ┌──────────┐  ┌──────────────┐  ┌───────┴──────┐      │       │
│  │  Cron    │  │ Rule Engine  │  │   Publish    │      │       │
│  │ Scheduler│─▶│ + Conditions │─▶│ NATS Actions │      │       │
│  │ (gocron) │  │ + Templates  │  │              │      │       │
│  └──────────┘  └──────┬───────┘  └──────────────┘      │       │
│                       │                                 │       │
│                       └── KV Lookups ───────────────────┘       │
└─────────────────────────────────────────────────────────────────┘
```

1. Cron scheduler fires at the configured time
2. Rule engine evaluates conditions (KV lookups, time checks)
3. If conditions pass, templates are rendered and actions are published to NATS

## Metrics

The rule-scheduler exposes Prometheus metrics on port `:2114` (configurable).

### Key Metrics

*   `rules_active` — Number of schedule rules registered
*   `actions_total{status="success|error"}` — Actions published
*   `action_publish_failures_total` — Failed publish attempts

## Testing Rules

Use `rule-cli` to validate your schedule rules offline:

```bash
# Lint all rules (including schedule rules)
rule-cli check --rules ./rules

# Validate cron expressions and timezone settings
rule-cli lint ./rules/scheduler/
```

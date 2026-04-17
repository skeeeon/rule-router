# Rule Router

Rule Router is a unified binary containing three features built on a shared, high-performance rule engine:

*   **Router** (default) — NATS-to-NATS message routing, filtering, and enrichment
*   **Gateway** — Bidirectional HTTP-NATS integration (inbound webhooks, outbound API calls)
*   **Scheduler** — Cron-based scheduled publishing to NATS and HTTP endpoints

Features are enabled via config (`features.router`, `features.gateway`, `features.scheduler`) or environment variables (`RR_FEATURES_GATEWAY=true`). Multiple features can run in a single process with shared NATS connections, metrics, and KV cache.

## Features

*   **High Performance** - Microsecond rule evaluation, thousands of messages per second.
*   **Array Processing** - Native support for batch messages with array operators and forEach iteration.
*   **Primitive Message Support** - Handle strings, numbers, booleans, and arrays at the root.
*   **NATS JetStream Native** - Built on pull consumers for durable, scalable subscriptions.
*   **Cryptographic Security** - NKey signature verification and replay protection for secure workflows.
*   **Key-Value Store Integration** - Dynamic lookups with a local cache for ~25x faster lookups.
*   **Time-Based Rules** - Schedule-aware evaluation without external schedulers.
*   **Zero-Copy Passthrough** - Forward messages unchanged for high-performance filtering.
*   **Production Ready** - Prometheus metrics, structured logging, graceful shutdown.

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                     NATS JetStream                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────┐  │
│  │ Streams  │  │ Consumers│  │ KV Stores│  │ KV Changes│  │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └─────┬─────┘  │
└───────┼─────────────┼─────────────┼──────────────┼────────┘
        │             │             │              │
        ▼             ▼             ▼              ▼
┌───────────────────────────────────────────────────────────┐
│                      Rule Router                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │ Subscription │  │ Rule Engine  │  │ Local Cache  │     │
│  │ Manager      │──│ + Templates  │──│ (KV Mirror)  │     │
│  │              │  │ + Conditions │  │              │     │
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘     │
│         └─────────────────┴───► Publish NATS Actions      │
└───────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

*   Go 1.23+
*   NATS Server with JetStream enabled
*   **JetStream streams must exist before starting `rule-router`**

### Setup

1.  **Create Streams**
    ```bash
    # Stream for rule triggers (input)
    nats stream add SENSORS --subjects "sensors.>"

    # Stream for rule actions (output)
    nats stream add ALERTS --subjects "alerts.>"
    ```

2.  **Create a Rule**
    Create a file at `rules/temperature.yaml`:

    ```yaml
    - trigger:
        nats:
          subject: sensors.temperature
      conditions:
        operator: and
        items:
          - field: "{temperature}"
            operator: gt
            value: 30
      action:
        nats:
          subject: alerts.high-temperature
          payload: |
            {
              "alert": "High temperature detected",
              "temperature": {temperature},
              "timestamp": "{@timestamp()}"
            }
    ```

3.  **Run `rule-router`**
    ```bash
    ./rule-router --config config/rule-router.yaml --rules rules/
    ```

4.  **Test the Rule**
    ```bash
    nats pub sensors.temperature '{"temperature": 35}'
    ```

## Configuration

See `config/rule-router.yaml` for a fully documented example.

## Advanced Features

The `rule-router` uses a powerful, shared rule engine. For detailed documentation on advanced features, please see the main documentation:

*   **[Array Processing](./../../docs/03-array-processing.md)**: Guide to using `forEach` and array operators (`any`, `all`, `none`) for batch processing.
*   **[Primitive & Array Root Messages](./../../docs/04-primitive-messages.md)**: How to handle non-object JSON payloads.
*   **[System Variables & Functions](./../../docs/02-system-variables.md)**: Full reference for all `@` variables (including `@subject`) and functions.
*   **[Security](./../../docs/05-security.md)**: Guide to Cryptographic Signature Verification.

### Example: Batch Processing with `forEach`

This rule processes a batch of events, generating one new message for each "critical" event in the array.

```yaml
- trigger:
    nats:
      subject: "device.batch.>"
  action:
    nats:
      # Generate one alert per critical event
      forEach: "{events}"
      filter:
        operator: and
        items:
          - field: "{status}"
            operator: eq
            value: "critical"
      subject: "alerts.critical.{deviceId}"
      payload: |
        {
          "deviceId": "{deviceId}",
          "timestamp": "{timestamp}",
          "batchId": "{@msg.batchId}"
        }
```

### Example: Debounce / Throttle

Suppress rapid-fire alerts per room, allowing one alert every 30 seconds:

```yaml
- trigger:
    nats:
      subject: "sensors.temperature.>"
      debounce:
        window: "5s"
  conditions:
    operator: and
    items:
      - field: "{temperature}"
        operator: gt
        value: 30
  action:
    nats:
      subject: "alerts.high_temp"
      payload: '{"alert": true, "temp": {temperature}}'
      debounce:
        window: "30s"
        key: "{@subject.2}"
```

See [Core Concepts](./../../docs/01-core-concepts.md) for full debounce semantics.

### Example: Message Enrichment with `merge`

This rule enriches incoming orders with customer data from a KV store, preserving all original fields:

```yaml
- trigger:
    nats:
      subject: "orders.incoming"
  action:
    nats:
      subject: "orders.enriched.{customer_id}"
      merge: true
      payload: |
        {
          "customer_tier": "{@kv.customers.{customer_id}:tier}",
          "processed_at": "{@timestamp()}",
          "trace_id": "{@uuid7()}"
        }
```

With `merge: true`, the original message fields are preserved and the overlay fields are added or overwritten. See [Core Concepts](./../../docs/01-core-concepts.md) for full merge semantics.

## Rule Loading: File vs KV

By default, rules are loaded from YAML files in the `rules/` directory. You can optionally load rules from a NATS KV bucket instead, enabling live updates without restarts.

### File-Based (Default)

```bash
./rule-router --config config/rule-router.yaml --rules rules/
```

Rules load at startup. Send `SIGHUP` to reload from disk.

### KV-Based

Enable in your config:

```yaml
kv:
  enabled: true
  rules:
    enabled: true
    bucket: "rules"
    autoProvision: false
```

With KV rules enabled, the router watches the configured bucket and hot-reloads rules on any change. JetStream consumers and subscriptions are created and removed automatically as rule subjects change. Push rules with:

```bash
rule-cli kv push rules/ --url nats://localhost:4222
```

For full details on KV rule storage, GitOps workflows, and the `rule-cli kv push` command, see the [KV Rule Store documentation](./../../docs/06-kv-rule-store.md).

## Testing Rules

Use the standalone `rule-cli` utility for offline validation of your rules.

```bash
# Scaffold tests for a new rule (auto-detects forEach)
rule-cli scaffold ./rules/my-batch-rule.yaml

# Run all tests
rule-cli test --rules ./rules
```

For complete documentation, see the [**`rule-cli` README**](../rule-cli/README.md).

## Monitoring

The rule-router exposes Prometheus metrics on port `:2112` (configurable).

### Key Metrics

*   `messages_total{status="received|processed|error"}`
*   `rule_matches_total`
*   `actions_total{status="success|error"}`
*   `foreach_iterations_total{rule_file="batch_notifications"}`
*   `throttle_suppressed_total{phase="trigger|action"}`
*   `kv_cache_hits_total` / `kv_cache_misses_total`
*   `nats_connection_status`

# Rule Router

Rule Router is a high-performance NATS-to-NATS message router that evaluates JSON messages against configurable rules and publishes templated actions. It is a core component of the Rule-Based Messaging Platform and shares its powerful rule engine with the `http-gateway`.

This application is purpose-built for internal, high-throughput message routing, filtering, enrichment, and security validation within your NATS infrastructure.

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
*   `forEach_iterations_total{rule_file="batch_notifications"}`
*   `kv_cache_hits_total` / `kv_cache_misses_total`
*   `nats_connection_status`

# HTTP Gateway

Bidirectional HTTP ↔ NATS integration gateway with rule-based routing, templating, and transformation.

## Features

- **HTTP → NATS** (Inbound Webhooks): Fire-and-forget pattern, returns 200 immediately
- **NATS → HTTP** (Outbound Webhooks): ACK-on-success with exponential backoff retry
- **Array Processing**: Full support for batch webhooks with forEach iteration
- **Primitive Message Support**: Handle strings, numbers, arrays, and objects
- **Shared Rule Engine**: Code reuse with rule-router
- **Template Engine**: Variable substitution, KV lookups, time-based routing
- **Prometheus Metrics**: Complete observability
- **Graceful Shutdown**: Clean resource cleanup

## Quick Start

### Prerequisites

- Go 1.23+
- NATS Server with JetStream enabled
- JetStream streams configured for your subjects

### Installation

```bash
# Build
go build -o http-gateway ./cmd/http-gateway

# Run
./http-gateway --config config/http-gateway.yaml --rules rules/
```

### Example Configuration

```yaml
http:
  server:
    address: ":8080"
  client:
    timeout: "30s"

nats:
  urls:
    - "nats://localhost:4222"

forEach:
  maxIterations: 100  # Limit batch webhook processing
```

See `config/http-gateway.yaml` for full configuration.

## Usage

### Inbound Webhooks (HTTP → NATS)

Receive HTTP webhooks and publish to NATS:

```yaml
- trigger:
    http:
      path: "/webhooks/github"
      method: "POST"
  conditions:
    operator: and
    items:
      - field: "{@header.X-GitHub-Event}"
        operator: eq
        value: "pull_request"
  action:
    nats:
      subject: "github.events.pr"
      payload: |
        {
          "repo": "{repository.name}",
          "pr": {number},
          "author": "{user.login}"
        }
```

### Outbound Webhooks (NATS → HTTP)

Subscribe to NATS and make HTTP requests:

```yaml
- trigger:
    nats:
      subject: "alerts.critical.>"
  action:
    http:
      url: "https://events.pagerduty.com/v2/enqueue"
      method: "POST"
      payload: '{"alert": "{message}"}'
      retry:
        maxAttempts: 3
        initialDelay: "1s"
```

## Advanced Features

The `http-gateway` shares its powerful rule engine with the `rule-router`. For detailed documentation on advanced features, please see the main documentation:

*   **[Array Processing](./../../docs/03-array-processing.md)**: Guide to using `forEach` and array operators (`any`, `all`, `none`) for batch webhooks.
*   **[Primitive & Array Root Messages](./../../docs/04-primitive-messages.md)**: How to handle non-object JSON payloads from webhooks.
*   **[System Variables & Functions](./../../docs/02-system-variables.md)**: Full reference for all `@` variables (including `@path`, `@method`) and functions.
*   **[Security](./../../docs/05-security.md)**: Guide to Cryptographic Signature Verification.

### Example: Batch Webhook Processing (HTTP → NATS)

This example processes a Stripe webhook containing multiple events, generating one NATS message per successful charge.

```yaml
- trigger:
    http:
      path: "/webhooks/stripe/events"
      method: "POST"
  action:
    nats:
      # Generate one NATS message per successful charge
      forEach: "{data}"
      filter:
        operator: and
        items:
          - field: "{type}"
            operator: eq
            value: "charge.succeeded"
      subject: "payments.success.{object.customer}"
      payload: |
        {
          "customerId": "{object.customer}",
          "amount": {object.amount},
          "chargeId": "{id}",
          "webhookId": "{@msg.id}"
        }
```

## Architecture

```
┌─────────────┐         ┌─────────────────┐         ┌──────────────┐
│   Webhook   │ ─────>  │  HTTP Gateway   │ ─────>  │     NATS     │
│   Senders   │         │                 │         │  JetStream   │
│  (GitHub,   │ <─────  │  Fire-and-Forget│ <─────  │              │
│   Stripe)   │         │  ACK-on-Success │         │              │
└─────────────┘         └─────────────────┘         └──────────────┘
```

### Inbound Flow (HTTP → NATS)

1. HTTP request arrives at `/webhooks/path`
2. Return `200 OK` immediately (fire-and-forget)
3. Extract headers and body
4. Evaluate rules asynchronously in a worker pool
5. Publish matched actions to NATS (supports forEach)

### Outbound Flow (NATS → HTTP)

1. Subscribe to NATS subject
2. Fetch messages in batches
3. Evaluate rules
4. Make HTTP request(s) with retry (supports forEach)
5. ACK on HTTP 200-299, NAK on failure (triggers redelivery)

## Metrics

The gateway exposes detailed metrics, including:

```
# HTTP Inbound
http_inbound_requests_total{path="/webhooks/github",method="POST",status="200"}
http_request_duration_seconds{path="/webhooks/github",method="POST"}

# HTTP Outbound
http_outbound_requests_total{url="https://api.example.com",status_code="200"}
http_outbound_duration_seconds{url="https://api.example.com"}

# Array Operations
forEach_iterations_total{rule_file="batch_webhook"}
forEach_actions_generated_total{rule_file="batch_webhook"}
```

## Testing

Use the `rule-cli` for offline development and testing of your gateway rules.

```bash
# Scaffold tests for a webhook rule
rule-cli scaffold rules/my-webhook-rule.yaml

# Run all tests
rule-cli test --rules rules/
```

See the [rule-cli README](../rule-cli/README.md) for details.

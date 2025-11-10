# Rule Router & HTTP Gateway

A high-performance, rule-based messaging platform for NATS, providing an internal message router, a bidirectional HTTP gateway, and an automated token manager for secure API integration.

This monorepo contains three primary, interoperable applications built on a shared, powerful rule engine:

*   **`rule-router`**: A high-throughput NATS-to-NATS message router for internal, event-driven workflows.
*   **`http-gateway`**: A bidirectional bridge for integrating external systems with your NATS fabric via webhooks (HTTP → NATS) and outbound API calls (NATS → HTTP).
*   **`nats-auth-manager`**: A standalone utility that securely manages and refreshes API tokens (OAuth2, etc.), storing them in NATS KV for use by the `http-gateway` on outbound webhooks.

---

## Features

The platform is designed for performance, security, and flexibility in event-driven architectures.

*   **High Performance**: Microsecond rule evaluation, asynchronous processing, and thousands of messages per second.
*   **Array Processing**: Native support for batch message processing with array operators and forEach iteration.
*   **Primitive Message Support**: Handle strings, numbers, arrays, and objects at the root - perfect for IoT protocols and simple formats.
*   **Bidirectional HTTP Gateway**:
    *   **Inbound**: "Fire-and-forget" webhook ingestion returns `200 OK` immediately for maximum compatibility.
    *   **Outbound**: "ACK-on-Success" API calls with configurable retries and exponential backoff ensure reliable delivery.
*   **NATS JetStream Native**: Built on JetStream pull consumers for durable, scalable, and resilient message processing.
*   **Powerful Rule Engine**:
    *   **Dynamic Conditions**: Evaluate message payloads, headers, NATS subjects, and HTTP paths.
    *   **Templating**: Construct new message payloads, subjects, URLs, and headers using data from the trigger.
    *   **Key-Value Integration**: Enrich messages with data from NATS KV stores, with an optional local cache for a ~25x performance boost.
    *   **Time-Based Logic**: Create rules that only run at certain times of day, on specific days, or within a time window.
*   **Cryptographic Security**: Verify message integrity and authenticity using NATS NKey signatures.
*   **Production Ready**: Structured logging, Prometheus metrics, graceful shutdown, and full NATS authentication support.

## Core Concepts

The platform uses a simple `Trigger -> Conditions -> Action` model defined in YAML files.

*   **Triggers** define what starts a rule (e.g., a NATS message on `sensors.>` or an HTTP POST to `/webhooks/github`).
*   **Conditions** define the logic to determine if the action should run (e.g., `temperature > 30`).
*   **Actions** define what to do if conditions pass (e.g., publish a new NATS message or make an HTTP call).

**» For a complete guide, see the [Core Concepts documentation](./docs/01-core-concepts.md).**

## Documentation

Detailed documentation on the rule engine's features can be found in the `docs/` directory:

*   **[01 - Core Concepts](./docs/01-core-concepts.md)**: Triggers, Conditions, Actions, and Environment Variables.
*   **[02 - System Variables & Functions](./docs/02-system-variables.md)**: Full reference for all `@` variables and functions.
*   **[03 - Array Processing](./docs/03-array-processing.md)**: Guide to using `forEach` and array operators (`any`, `all`, `none`).
*   **[04 - Primitive & Array Root Messages](./docs/04-primitive-messages.md)**: How to handle non-object JSON payloads.
*   **[05 - Security](./docs/05-security.md)**: Guide to Cryptographic Signature Verification.

---

## Quick Start

### Prerequisites

*   Go 1.23+
*   A running NATS Server with JetStream enabled.

### 1. Build the Binaries

From the root of the repository, build both applications:

```bash
# Build the NATS-to-NATS router
go build -o rule-router ./cmd/rule-router

# Build the HTTP Gateway
go build -o http-gateway ./cmd/http-gateway
```

### 2. Configure NATS Streams

For this example, we'll receive a webhook and route it to an internal alerts stream.

```bash
# Stream for messages coming from the HTTP gateway
nats stream add WEBHOOKS --subjects "webhooks.>"

# Stream for critical alerts processed by the rule-router
nats stream add ALERTS --subjects "alerts.>"
```

### 3. Create Rules

Create a `rules/` directory with two rule files.

**`rules/http_ingress.yaml`** (For `http-gateway`)
This rule listens for inbound webhooks at `/webhooks/devices` and publishes a standardized message to NATS.

```yaml
- trigger:
    http:
      path: "/webhooks/devices"
      method: "POST"
  conditions:
    operator: and
    items:
      - field: "{status}"
        operator: "eq"
        value: "error"
  action:
    nats:
      subject: "webhooks.devices.status"
      payload: |
        {
          "device_id": "{device_id}",
          "error_code": "{error.code}",
          "error_message": "{error.message}",
          "received_at": "{@timestamp()}"
        }
```

**`rules/internal_routing.yaml`** (For `rule-router`)
This rule listens for the internal status messages and routes critical errors to a dedicated alerts subject.

```yaml
- trigger:
    nats:
      subject: "webhooks.devices.status"
  conditions:
    operator: and
    items:
      - field: "{error_code}"
        operator: gte
        value: 5000 # Critical error codes
  action:
    nats:
      subject: "alerts.critical.{device_id}"
      passthrough: true # Forward the original message payload
      headers:
        X-Routed-By: "rule-router"
```

### 4. Run the Applications

You will need two separate configuration files (see the `config/` directory for examples).

**Terminal 1: Start the HTTP Gateway**
```bash
./http-gateway --config config/http-gateway.yaml --rules ./rules
```

**Terminal 2: Start the Rule Router**
```bash
./rule-router --config config/rule-router.yaml --rules ./rules
```

### 5. Test the Flow

**Terminal 3: Subscribe to the final alerts subject**
```bash
nats sub "alerts.>"
```

**Terminal 4: Send a test webhook**
```bash
curl -X POST http://localhost:8080/webhooks/devices \
  -H "Content-Type: application/json" \
  -d '{
    "device_id": "sensor-123",
    "status": "error",
    "error": {
      "code": 5001,
      "message": "Internal system failure"
    }
  }'
```

You will see the message appear on the `alerts.>` subscription, having been processed by both applications.

## Applications

*   **`cmd/rule-router`**: A dedicated NATS-to-NATS message router. Ideal for high-performance, internal event stream processing, filtering, and enrichment. [**» View Router README**](./cmd/rule-router/README.md)

*   **`cmd/http-gateway`**: A bidirectional HTTP-to-NATS gateway. Perfect for integrating with third-party webhooks and for triggering external APIs from NATS events. [**» View Gateway README**](./cmd/http-gateway/README.md)

*   **`cmd/nats-auth-manager`**: A standalone service that handles OAuth2 and custom API authentication, storing tokens in NATS KV for use by other services. [**» View Auth Manager README**](./cmd/nats-auth-manager/README.md)

*   **`cmd/rule-cli`**: A powerful command-line utility for linting, scaffolding, and testing your rules offline, enabling CI/CD and ensuring rule correctness. [**» View CLI README**](./cmd/rule-cli/README.md)

## Monitoring

Both applications expose a Prometheus metrics endpoint, typically on port `:2112`. Key metrics include:

**Message Processing:**
*   `messages_total`: Total messages processed by status.
*   `rule_matches_total`: Total number of times any rule has matched.
*   `actions_total`: Total actions executed by status.

**Array Operations:**
*   `forEach_iterations_total`: Total array elements processed in forEach operations.
*   `forEach_filtered_total`: Elements filtered out by forEach filter conditions.
*   `forEach_actions_generated_total`: Actions successfully generated by forEach.

**HTTP Gateway:**
*   `http_inbound_requests_total`: (Gateway) Inbound HTTP requests.
*   `http_outbound_requests_total`: (Gateway) Outbound HTTP requests.

**Auth Manager:**
*   `authmgr_auth_success_total`: Successful authentications by provider.
*   `authmgr_auth_failures_total`: Failed authentications by provider.

## License

This project is licensed under the MIT License - see the [LICENSE](./LICENSE) file for details.

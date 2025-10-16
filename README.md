
# Rule Router & HTTP Gateway

A high-performance, rule-based messaging platform for NATS, providing both an internal message router and a bidirectional HTTP gateway.

This monorepo contains two primary applications built on a shared, powerful rule engine:

*   **`rule-router`**: A high-throughput NATS-to-NATS message router for internal, event-driven workflows.
*   **`http-gateway`**: A bidirectional bridge for integrating external systems with your NATS fabric via webhooks (HTTP → NATS) and outbound API calls (NATS → HTTP).

---

## Features

The platform is designed for performance, security, and flexibility in event-driven architectures.

*   **High Performance**: Microsecond rule evaluation, asynchronous processing, and thousands of messages per second.
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

All applications in this repository are configured using a shared rule syntax. A **rule** is a YAML object composed of a `trigger`, `conditions`, and an `action`.

```yaml
# A rule is defined by a trigger, optional conditions, and an action.
- trigger:
    # ... defines what starts the rule evaluation (e.g., a NATS message or an HTTP request)
  conditions:
    # ... defines the logic to determine if the action should run (e.g., field value checks)
  action:
    # ... defines what to do if the conditions pass (e.g., publish a NATS message or make an HTTP call)
```

### 1. Triggers (The "If")

A trigger defines the event that initiates a rule evaluation.

**NATS Trigger**: Evaluates a message from a NATS subject.
```yaml
trigger:
  nats:
    subject: "sensors.temperature.>" # Supports wildcards
```

**HTTP Trigger**: Evaluates an incoming HTTP request.
```yaml
trigger:
  http:
    path: "/webhooks/github"   # Exact path match
    method: "POST"             # Optional, defaults to all methods
```

### 2. Conditions (The "When")

Conditions are an optional block of logic that must evaluate to `true` for the action to be executed.

```yaml
conditions:
  operator: and # or "or"
  items:
    # Check a field in the JSON payload
    - field: "temperature"
      operator: gte
      value: 30
    # Check an HTTP header
    - field: "@header.X-Device-Auth"
      operator: "exists"
    # Check the time of day
    - field: "@time.hour"
      operator: gte
      value: 9
    # Check a value from a NATS KV store
    - field: "@kv.device_status.{device_id}:status"
      operator: "eq"
      value: "active"
```

### 3. Actions (The "Then")

An action defines the work to be done when a rule's conditions are met.

**NATS Action**: Publishes a new message to a NATS subject.
```yaml
action:
  nats:
    subject: "alerts.high_temp.{device_id}"
    payload: |
      {
        "alert": "High temperature detected!",
        "temp": {temperature},
        "device": "{device_id}",
        "timestamp": "{@timestamp()}"
      }
```

**HTTP Action**: Makes an outbound HTTP request to an external service.
```yaml
action:
  http:
    url: "https://api.pagerduty.com/incidents"
    method: "POST"
    headers:
      Authorization: "Token ${PAGERDUTY_TOKEN}" # Env vars supported
    payload: '{"service": "app-alerts", "message": "{alert_message}"}'
    retry:
      maxAttempts: 3
      initialDelay: "1s"
```

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
      - field: "status"
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
      - field: "error_code"
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
./http-gateway -config config/http-gateway.yaml -rules ./rules
```

**Terminal 2: Start the Rule Router**
```bash
./rule-router -config config/rule-router.yaml -rules ./rules
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

*   **`cmd/http-gateway`**: A bidirectional HTTP-to-NATS gateway. Perfect for integrating with third-party webhooks (e.g., GitHub, Stripe) and for triggering external APIs from NATS events. [**» View Gateway README**](./cmd/http-gateway/README.md)

*   **`cmd/rule-tester`**: A powerful command-line utility for linting, scaffolding, and testing your rules offline, enabling CI/CD and ensuring rule correctness. [**» View Tester README**](./cmd/rule-tester/README.md)

## Monitoring

Both applications expose a Prometheus metrics endpoint, typically on port `:2112`. Key metrics include:
*   `messages_total`: Total messages processed by status.
*   `rule_matches_total`: Total number of times any rule has matched.
*   `actions_total`: Total actions executed by status.
*   `http_inbound_requests_total`: (Gateway) Inbound HTTP requests.
*   `http_outbound_requests_total`: (Gateway) Outbound HTTP requests.
*   `nats_connection_status`: Health of the NATS connection.

## License

This project is licensed under the MIT License - see the [LICENSE](./LICENSE) file for details.


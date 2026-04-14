# Rule Router

A high-performance, rule-based messaging platform for NATS, providing an internal message router, cron-based scheduled publishing, a bidirectional HTTP gateway, and an automated token manager for secure API integration.

**`rule-router`** is a single unified binary with three selectable features, plus a companion auth manager:

*   **Router** (default): A high-throughput NATS-to-NATS message router for internal, event-driven workflows.
*   **Scheduler**: A cron-based scheduler that publishes to NATS and HTTP endpoints on configurable schedules, with full conditions, KV support, and HTTP retry logic.
*   **Gateway**: A bidirectional bridge for integrating external systems with your NATS fabric via webhooks (HTTP → NATS) and outbound API calls (NATS → HTTP).
*   **`nats-auth-manager`**: A standalone utility that securely manages and refreshes API tokens (OAuth2, etc.), storing them in NATS KV for use by the gateway on outbound webhooks.

Features are enabled via config (`features.router`, `features.gateway`, `features.scheduler`) or environment variables (`RR_FEATURES_GATEWAY=true`). Multiple features can run in a single process.

---

## Features

The platform is designed for performance, security, and flexibility in event-driven architectures.

*   **High Performance**: Microsecond rule evaluation, asynchronous processing, and thousands of messages per second.
*   **Cron Scheduling**: Publish to NATS and HTTP endpoints on cron schedules with timezone support, conditional execution, KV lookups, and HTTP retry logic.
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
    *   **KV Rule Store**: Optionally store rules in a NATS KV bucket with automatic hot-reload on any change. Push rules with `rule-cli kv push` for a GitOps workflow.
    *   **Time-Based Logic**: Create rules that only run at certain times of day, on specific days, or within a time window.
    *   **Debounce / Throttle**: Optional per-rule fire-first suppression on triggers and/or actions with configurable time windows and template-based keys.
*   **Cryptographic Security**: Verify message integrity and authenticity using NATS NKey signatures.
*   **Production Ready**: Structured logging, Prometheus metrics, graceful shutdown, and full NATS authentication support.

## Core Concepts

The platform uses a simple `Trigger -> Conditions -> Action` model defined in YAML files, loaded from the local filesystem or a NATS KV bucket.

*   **Triggers** define what starts a rule (e.g., a NATS message on `sensors.>`, an HTTP POST to `/webhooks/github`, or a cron schedule).
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
*   **[06 - KV Rule Store](./docs/06-kv-rule-store.md)**: Store rules in NATS KV with hot-reload and GitOps push workflow.
*   **[Rule Builder Web UI](./web/README.md)**: Visual rule creation, live YAML preview, and NATS KV push/pull.
*   **[Docker Deployment](#quick-start)**: Docker Compose setup for local development and deployment.

---

## Quick Start

### Option A: Download a Release

Download pre-built binaries from [GitHub Releases](https://github.com/skeeeon/rule-router/releases) for your platform. Each release includes all applications and the CLI tool.

```bash
# Extract the release archive
tar -xzf rule-router_<version>_<os>_<arch>.tar.gz

# Run with router feature (default)
./rule-router --config config/rule-router.yaml --rules ./rules

# Run with gateway feature
RR_FEATURES_ROUTER=false RR_FEATURES_GATEWAY=true \
  ./rule-router --config config/http-gateway.yaml --rules ./rules

# Run with scheduler feature
RR_FEATURES_ROUTER=false RR_FEATURES_SCHEDULER=true \
  ./rule-router --config config/rule-scheduler.yaml --rules ./rules

# Run all features in a single process
RR_FEATURES_GATEWAY=true RR_FEATURES_SCHEDULER=true \
  ./rule-router --config config/rule-router.yaml --rules ./rules

# Run the auth manager (separate binary)
./nats-auth-manager --config config/auth-manager.yaml
```

A running NATS Server with JetStream enabled is required. See [Configure NATS Streams](#2-configure-nats-streams) below for setup.

### Option B: Docker

#### Docker Compose (with bundled NATS)

Brings up all services including a NATS server with JetStream — no external dependencies needed.

```bash
# Build and start all apps with a bundled NATS server
docker compose --profile nats up -d

# Check that everything is healthy
docker compose ps

# View logs
docker compose logs -f

# Include the web UI on port 3000
docker compose --profile nats --profile web up -d

# Run the CLI tool
docker compose run --rm rule-cli lint /rules

# Tear down (add -v to also remove NATS data)
docker compose down
```

#### Docker Compose (with external NATS)

If you already have a NATS server running, start just the applications and point them to it:

```bash
# Start apps only, pointing to your NATS server
NATS_URLS=nats://your-nats-host:4222 docker compose up -d

# Or set it in a .env file
echo 'NATS_URLS=nats://your-nats-host:4222' > .env
docker compose up -d
```

#### Building Docker Images

The repository uses a single parameterized `Dockerfile`. The `rule-router` image includes all three features (router, gateway, scheduler) — select which to enable at runtime via config or environment variables:

```bash
# Build the rule-router image (includes all features)
docker build --build-arg APP_NAME=rule-router -t rule-router .

# Build companion images
docker build --build-arg APP_NAME=nats-auth-manager -t nats-auth-manager .
docker build --build-arg APP_NAME=rule-cli -t rule-cli .

# Run as router (default)
docker run -d \
  -v ./config/rule-router.yaml:/config/rule-router.yaml:ro \
  -v ./rules/router:/rules:ro \
  -p 2112:2112 \
  rule-router --config /config/rule-router.yaml --rules /rules

# Run as gateway
docker run -d \
  -e RR_FEATURES_ROUTER=false -e RR_FEATURES_GATEWAY=true \
  -v ./config/http-gateway.yaml:/config/http-gateway.yaml:ro \
  -v ./rules/http:/rules:ro \
  -p 8080:8080 -p 2112:2112 \
  rule-router --config /config/http-gateway.yaml --rules /rules
```

**Ports exposed:**

| Service | Port | Description |
|---|---|---|
| NATS (optional) | 4222 | Client connections |
| NATS (optional) | 8222 | Monitoring dashboard |
| rule-router (gateway) | 8080 | Inbound webhooks |
| rule-router | 2112 | Prometheus metrics |
| nats-auth-manager | 2115 | Prometheus metrics |
| web (optional) | 3000 | Rule Builder UI |

Configuration files in `config/` and rules in `rules/` are mounted as volumes, so edits are picked up on restart (or via SIGHUP for hot-reload).

### Option C: Build from Source

#### Prerequisites

*   Go 1.26+
*   A running NATS Server with JetStream enabled.

#### 1. Build the Binaries

From the root of the repository:

```bash
# Build the unified rule-router binary (includes router, gateway, and scheduler features)
go build -o rule-router ./cmd/rule-router

# Build the auth manager
go build -o nats-auth-manager ./cmd/nats-auth-manager

# Build the CLI
go build -o rule-cli ./cmd/rule-cli
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

**`rules/http_ingress.yaml`** (For the gateway feature)
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

**`rules/internal_routing.yaml`** (For the router feature)
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

If using Docker Compose, the services are already running (see [Option B](#option-b-docker)). For manual runs, you can run both features in a single process or separately.

**Option 1: Single process with both features**
```bash
RR_FEATURES_GATEWAY=true ./rule-router --config config/rule-router.yaml --rules ./rules
```

**Option 2: Separate processes**
```bash
# Terminal 1: Start the gateway
RR_FEATURES_ROUTER=false RR_FEATURES_GATEWAY=true \
  ./rule-router --config config/http-gateway.yaml --rules ./rules

# Terminal 2: Start the router
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

*   **`cmd/rule-router`**: The unified binary containing three features — **router** (NATS-to-NATS message routing), **gateway** (bidirectional HTTP-NATS integration), and **scheduler** (cron-based publishing). Enable features via config or env vars. [**» View README**](./cmd/rule-router/README.md)

*   **`cmd/nats-auth-manager`**: A standalone service that handles OAuth2 and custom API authentication, storing tokens in NATS KV for use by other services. [**» View Auth Manager README**](./cmd/nats-auth-manager/README.md)

*   **`cmd/rule-cli`**: A powerful command-line utility for linting, scaffolding, and testing your rules offline, enabling CI/CD and ensuring rule correctness. [**» View CLI README**](./cmd/rule-cli/README.md)

*   **`web`**: A visual rule builder web UI. Build rules with a guided form, preview YAML in real time, and push directly to NATS KV via WebSocket. Supports dark mode and responsive mobile layout. [**» View Web UI README**](./web/README.md)

## Monitoring

All features expose a shared Prometheus metrics endpoint (default `:2112`). Key metrics include:

**Message Processing:**
*   `messages_total`: Total messages processed by status.
*   `rule_matches_total`: Total number of times any rule has matched.
*   `actions_total`: Total actions executed by status.

**Array Operations:**
*   `forEach_iterations_total`: Total array elements processed in forEach operations.
*   `forEach_filtered_total`: Elements filtered out by forEach filter conditions.
*   `forEach_actions_generated_total`: Actions successfully generated by forEach.

**Throttle:**
*   `throttle_suppressed_total`: Messages suppressed by per-rule debounce, by phase (trigger/action).

**HTTP Gateway:**
*   `http_inbound_requests_total`: (Gateway) Inbound HTTP requests.
*   `http_outbound_requests_total`: (Gateway) Outbound HTTP requests.

**Scheduler:**
*   `rules_active`: Number of schedule rules registered.
*   `action_publish_failures_total`: Failed scheduled publish attempts.

**Auth Manager:**
*   `authmgr_auth_success_total`: Successful authentications by provider.
*   `authmgr_auth_failures_total`: Failed authentications by provider.

## License

This project is licensed under the MIT License - see the [LICENSE](./LICENSE) file for details.

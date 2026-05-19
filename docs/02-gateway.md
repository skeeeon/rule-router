# Gateway

The gateway feature provides bidirectional HTTP↔NATS integration. Enable it with `features.gateway: true` or `RR_FEATURES_GATEWAY=true`. It runs alongside the router and scheduler in the same process when desired.

The gateway handles two distinct flows:

| Flow | Trigger | Action | Use case |
|------|---------|--------|----------|
| **Inbound** | `http` | `nats` | Webhook ingestion (third-party → NATS event) |
| **Outbound** | `nats` | `http` | NATS event → external API call |

Both flows use the same rule format. The trigger and action types determine which direction a rule operates in.

## Inbound: HTTP → NATS

An HTTP trigger evaluates an incoming HTTP request and publishes a NATS message if conditions pass.

```yaml
- trigger:
    http:
      path: "/webhooks/stripe"
      method: "POST"          # Optional, defaults to all methods
  conditions:
    operator: and
    items:
      - field: "{type}"
        operator: eq
        value: "payment_intent.succeeded"
  action:
    nats:
      subject: "payments.succeeded"
      payload: '{"payment_id": "{data.object.id}"}'
```

### Fire-and-forget semantics

Inbound webhooks return `200 OK` to the sender **as soon as the request is parsed**, before rule evaluation or NATS publishing. This guarantees the sender never waits on downstream processing and webhook providers won't retry due to slow responses.

If a rule fails to evaluate or the NATS publish fails, the failure is logged but the sender has already received its `200`. Design rules with this in mind: do not assume the sender can be notified of a downstream failure. If the downstream publish absolutely must succeed before acknowledging, the gateway is the wrong tool — write a normal HTTP server.

### HTTP context variables

The full set is in [04 System Variables](./04-system-variables.md#http-context-gateway-feature-only). The common ones:

| Variable | Description | Example |
|----------|-------------|---------|
| `{@path}` | Full HTTP path | `/webhooks/tenant-a/events` |
| `{@path.N}` | Path segment (zero-indexed) | `{@path.1}` → `tenant-a` |
| `{@method}` | HTTP method | `POST` |
| `{@header.HeaderName}` | Request header | `{@header.X-GitHub-Event}` |

Path segments are useful for multi-tenant webhooks: a path like `/webhooks/tenant-a/events` can route to per-tenant subjects via `{@path.1}`.

### Conditional ingestion

Conditions can filter by header, body, or path. Common shapes:

```yaml
# Filter by GitHub event type header
- field: "{@header.X-GitHub-Event}"
  operator: eq
  value: "pull_request"

# Multi-tenant by path
- field: "{@path.1}"
  operator: in
  value: ["tenant-a", "tenant-b"]

# Require a signature header
- field: "{@header.X-Webhook-Signature}"
  operator: exists
```

## Outbound: NATS → HTTP

A NATS trigger with an HTTP action calls an external API in response to an internal event.

```yaml
- trigger:
    nats:
      subject: "alerts.critical.>"
  conditions:
    operator: and
    items:
      - field: "{severity}"
        operator: gte
        value: 9
  action:
    http:
      url: "https://events.pagerduty.com/v2/enqueue"
      method: "POST"
      headers:
        Authorization: "Token ${PAGERDUTY_TOKEN}"
      payload: |
        {
          "routing_key": "...",
          "event_action": "trigger",
          "payload": {"summary": "{message}"}
        }
      retry:
        maxAttempts: 3
        initialDelay: "1s"
        maxDelay: "30s"
```

### ACK-on-success semantics

Outbound HTTP actions are ACKed back to JetStream **only on a successful HTTP response** (2xx). Non-2xx responses trigger the retry policy; if all retries are exhausted, the message is NACKed and JetStream redelivers per the stream's policy.

This means an unreachable external API does not silently drop events — JetStream keeps them queued until they succeed or the stream's retention limit is reached. This is the inverse of inbound's fire-and-forget guarantee: outbound trades delivery latency for delivery durability.

### Retry configuration

Full reference is in [01 Core Concepts](./01-core-concepts.md). Short version:

| Field | Default | Notes |
|-------|---------|-------|
| `maxAttempts` | 3 | Total including the first. `1` disables retry. |
| `initialDelay` | `"1s"` | Delay before second attempt. |
| `maxDelay` | `"30s"` | Cap on the backoff. |

Backoff doubles each attempt with ≤100ms jitter. In-flight retries are cancelled on graceful shutdown.

### Payload modes

| Mode | Field | Use when |
|------|-------|----------|
| **Templated** (default) | `payload: "..."` | You need a different shape for the external API |
| **Passthrough** | `passthrough: true` | The external API accepts the message as-is |
| **Merge** | `merge: true` + `payload: "..."` | Add a few fields to the original message |

### publishResponse: capture HTTP responses back into NATS

A successful HTTP response can be published to a NATS subject for downstream rules to react to:

```yaml
action:
  http:
    url: "https://api.example.com/devices/{device_id}/status"
    method: GET
    publishResponse:
      subject: "poll.devices.{device_id}.status"
```

The response body is published on 2xx (capped at 1 MB). Non-2xx responses retry per `retry` config and never publish. The subject is templated against the trigger context only — response fields are not available in the subject. See [09 Patterns — Polling-to-eventing bridge](./09-patterns.md#13-polling-to-eventing-bridge) for the full recipe.

## Authentication

Environment variables work for static tokens:

```yaml
headers:
  Authorization: "Bearer ${API_TOKEN}"
```

For tokens that refresh (OAuth2, time-limited API keys), pair with the `nats-auth-manager` companion binary. It manages token refresh, stores them in NATS KV, and rules read them with `{@kv.tokens.<provider>:access_token}`. See the [nats-auth-manager README](../README.md) for setup.

## Path matching

Inbound HTTP paths are handled differently depending on rule source:

| Mode | Handler registration | 404 source | New paths require |
|------|----------------------|------------|-------------------|
| **File-based** | One `ServeMux` handler per configured path at startup | `ServeMux` exact-match | Process restart |
| **KV-backed** | Single catch-all handler at `/` with O(1) path-existence check | Path check in catch-all handler | No restart — takes effect on next KV update |

Both modes return a real `404 Not Found` for paths with no matching rule. In KV mode the path-existence check happens **before** the request body is read or enqueued, so path-scan traffic does not consume queue capacity or worker time.

To bound Prometheus metric cardinality under path-scan traffic, 404 responses in KV mode are recorded with the sentinel path label `_unknown_` rather than the actual request path. Matched-path responses (2xx, 503) still use the real path.

Outbound rules (NATS trigger + HTTP action) benefit from KV mode similarly: a new NATS trigger gets its JetStream consumer created automatically on KV update, no restart required. See [08 KV Rule Store](./08-kv-rule-store.md) for the full reload mechanics.

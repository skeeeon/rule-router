# Gateway

The gateway feature provides bidirectional HTTP↔NATS integration. Enable it with `features.gateway: true` or `RR_FEATURES_GATEWAY=true`. It runs alongside the router and scheduler in the same process when desired.

The gateway handles these flows:

| Flow | Trigger | Action | Use case |
|------|---------|--------|----------|
| **Inbound** | `http` | `nats` | Webhook ingestion (third-party → NATS event) |
| **Outbound** | `nats` | `http` | NATS event → external API call |
| **Synchronous response** | `http` | `respond` | Return an evaluated/enriched payload as the HTTP response |
| **HTTP↔NATS bridge** | `http` | `nats` + `request: true` | Issue a NATS request and return the reply as the HTTP response |

Both flows use the same rule format. The trigger and action types determine which direction a rule operates in. The last two flows are covered under [Request/reply](#requestreply-synchronous-responses-and-the-httpnats-bridge).

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

This fire-and-forget behavior is the default and applies to every `http`→`nats` webhook rule. The exception is **synchronous routes** — paths whose matched rule has a `respond` action or a `request: true` NATS action. Those are handled inline so they can return a real response; see [Request/reply](#requestreply-synchronous-responses-and-the-httpnats-bridge) below. A path is synchronous only because of the rule shape; ordinary webhook ingestion is unaffected.

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
| `maxAttempts` | 1 | Total including the first. Set `>1` to enable retry. Off by default — retrying POST/PATCH can double-write if a failed request actually reached the server. |
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

## Request/reply: synchronous responses and the HTTP↔NATS bridge

The flows above are fire-and-forget. Two opt-in shapes let an HTTP request receive a real, evaluated response. Conceptual overview (including the NATS-side responder) is in [01 Core Concepts — Request/Reply & responses](./01-core-concepts.md#requestreply--responses); this section covers the HTTP specifics.

### Synchronous response (`respond` action)

An HTTP-triggered rule with a `respond` action returns the evaluated payload directly — no NATS round trip. Use it for lookups, enrichment endpoints, computed acknowledgments, or any "programmable endpoint" where the rule *is* the handler.

```yaml
- trigger:
    http:
      path: "/api/quote"
      method: "POST"
  conditions:
    operator: and
    items:
      - field: "{@header.Content-Type}"
        operator: eq
        value: "application/json"
  action:
    respond:
      statusCode: 200                 # optional; defaults to 200
      headers:
        Content-Type: "application/json"
      payload: |
        {
          "symbol": "{symbol}",
          "price": {@kv.prices.{symbol}:last},
          "quoteId": "{@uuid7()}"
        }
```

`statusCode` defaults to 200. If no `Content-Type` header is set, `application/json` is assumed. Passthrough/merge payload modes work the same as other actions.

### HTTP↔NATS bridge (`request: true`)

An HTTP-triggered rule with `action.nats` and `request: true` turns the request into a NATS request (`nc.Request`): the gateway waits for a responder and returns the reply as the HTTP response body. This exposes any NATS request/reply service — including a [NATS responder rule](./01-core-concepts.md#requestreply--responses) running under the `router` feature — as an HTTP endpoint.

```yaml
- trigger:
    http:
      path: "/api/geocode"
      method: "POST"
  action:
    nats:
      subject: "services.geocode"
      request: true
      timeout: "3s"          # optional; defaults to 5s
```

`request: true` is honored **only on HTTP triggers**. The subject, payload, and headers are templated exactly like a normal NATS action before the request is sent.

### Synchronous handling and semantics

- **Inline, not queued.** A path is treated as synchronous when its matched rule has a `respond` or `request: true` action. Such requests are handled inline in the request goroutine with a context deadline — they bypass the fire-and-forget worker queue (which exists to absorb webhook bursts and return `200` immediately). Ordinary webhook routes are unaffected.
- **First match wins.** If multiple rules match, the first (in load order) with a respond/request action produces the single HTTP response; other matching rules' plain NATS publishes still fire as side-effects.
- **No match this time.** If the path has a synchronous rule but conditions don't match on this request (so no respond/request action fires), the gateway returns `404 Not Found`.
- **Status mapping for the bridge:** a successful reply → `200` with the reply body; no responder on the subject → `503 Service Unavailable`; timeout (per `timeout`, default `5s`) → `504 Gateway Timeout`.

> **Deployment note:** the HTTP server's `writeTimeout` must exceed the bridge `timeout`, or a slow responder's reply can be cut off before it is written. Size `http.server.writeTimeout` accordingly.

## Authentication

Environment variables work for static tokens:

```yaml
headers:
  Authorization: "Bearer ${API_TOKEN}"
```

For tokens that refresh (OAuth2, time-limited API keys), pair with the `nats-auth-manager` companion binary. It manages token refresh, stores them in NATS KV, and rules read them with `{@kv.tokens.<provider>:access_token}`. See the [nats-auth-manager README](../README.md) for setup.

## Path matching

Inbound requests flow through a single catch-all handler that delegates path matching to the rule engine. This applies uniformly to file-loaded and KV-loaded rules: a new rule path becomes live on the next KV update (or process restart for file-loaded rules) with no `ServeMux` re-registration.

Paths can be exact or contain NATS-style wildcards:

| Syntax | Matches | Example |
|--------|---------|---------|
| Exact | The path verbatim | `/webhooks/github` matches only `/webhooks/github` |
| `*` | Exactly one path segment | `/webhooks/*/events` matches `/webhooks/github/events`, not `/webhooks/github/pr/events` |
| `>` | One or more trailing segments (must be the last segment) | `/api/>` matches `/api/v1`, `/api/v1/users/42`, etc. |

Wildcards may appear in any segment except `>`, which must be terminal. `/api/*/>` is valid; `/api/>/legacy` is rejected at load time.

When both an exact rule and a wildcard rule match the same request, **both fire**. Method filtering applies uniformly after matching — a `POST`-only wildcard will not match a `GET` request even if the path matches.

Wildcards play directly with the `{@path.N}` template variables: a rule on `/webhooks/*/events` can publish to `tenants.{@path.1}.events` to route per-tenant.

```yaml
- trigger:
    http:
      path: "/webhooks/*/events"
      method: "POST"
  action:
    nats:
      subject: "tenants.{@path.1}.events"
      payload: "{@passthrough}"
```

### Unknown paths and metric cardinality

Requests with no matching rule receive `404 Not Found`. The path-existence check happens **before** the request body is read or enqueued, so path-scan traffic does not consume queue capacity or worker time.

To bound Prometheus metric cardinality under path-scan traffic, 404 responses are recorded with the sentinel path label `_unknown_` rather than the actual request path. Matched responses (2xx, 503) use the real request path, so wildcard rules that match high-cardinality URLs (e.g. UUIDs in segments) can grow metric series — use wildcards judiciously or scope them tightly.

Outbound rules (NATS trigger + HTTP action) benefit from KV mode similarly: a new NATS trigger gets its JetStream consumer created automatically on KV update, no restart required. See [08 KV Rule Store](./08-kv-rule-store.md) for the full reload mechanics.

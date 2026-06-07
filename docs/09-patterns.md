# Patterns

A working catalogue of rule recipes, from simple routing through stateful correlation. Each pattern shows the minimal YAML that demonstrates it; for syntax details see [Core Concepts](./01-core-concepts.md) and the [System Variables Reference](./04-system-variables.md).

Patterns are grouped by complexity, not by feature. Most patterns apply to any feature (router, gateway, scheduler) that supports the relevant trigger or action type.

---

## Simple

### 1. Subject-based routing

Use subject tokens to derive the downstream subject from the trigger subject. One subscription fans out to many output subjects without per-type rule duplication.

```yaml
- trigger:
    nats:
      subject: "sensors.*"
  action:
    nats:
      subject: "alerts.{@subject.1}"
      payload: |
        {
          "type": "{@subject.1}",
          "value": {value}
        }
```

**When to reach for it:** A single wildcard subscription needs to fan out to per-type downstream subjects, or a derived subject encodes the type.

### 2. Threshold alert

A field crosses a limit, an action fires. The bread-and-butter rule.

```yaml
- trigger:
    nats:
      subject: "sensors.temperature"
  conditions:
    operator: and
    items:
      - field: "{value}"
        operator: gt
        value: 30
  action:
    nats:
      subject: "alerts.temperature"
      payload: |
        {
          "alert": "high temperature",
          "value": {value},
          "device": "{device_id}"
        }
```

**When to reach for it:** Any "if X then emit Y" rule. Compose multiple conditions with `operator: and` / `or`.

### 3. Enrich and forward

Add KV-derived fields onto the original message without re-specifying every existing field. Uses `merge: true`.

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
          "trace_id": "{@uuid7()}"
        }
```

**When to reach for it:** You want the original payload preserved and a few fields added. Saves you from re-listing every field, and naturally tolerates upstream schema changes.

### 4. Webhook → NATS

Inbound HTTP becomes a NATS event. Conditions can filter by header or body.

```yaml
- trigger:
    http:
      path: "/webhooks/stripe"
      method: "POST"
  conditions:
    operator: and
    items:
      - field: "{type}"
        operator: eq
        value: "payment_intent.succeeded"
  action:
    nats:
      subject: "payments.succeeded"
      payload: |
        {
          "payment_id": "{data.object.id}",
          "amount": {data.object.amount}
        }
```

**When to reach for it:** Any third-party webhook integration. The gateway returns `200 OK` immediately, so even slow downstream rules don't make the sender wait.

### 5. Poll and republish

A scheduled HTTP request whose response body is published to NATS. Downstream router rules treat the API as if it were event-driven.

```yaml
- trigger:
    schedule:
      cron: "*/5 * * * *"
  action:
    http:
      url: "https://api.example.com/devices"
      method: GET
      headers:
        Authorization: "Bearer ${API_TOKEN}"
      publishResponse:
        subject: "poll.devices.raw"
```

**When to reach for it:** A vendor API has no webhook or push channel and you want to drive downstream logic from it. The result is a normal NATS subject — see Pattern 13 for the full bridge.

---

## Intermediate

### 6. Time-windowed rule

Restrict an action to a specific time window (business hours, weekdays, off-hours).

```yaml
- trigger:
    nats:
      subject: "alerts.>"
  conditions:
    operator: and
    items:
      - field: "{severity}"
        operator: gte
        value: 7
      - field: "{@time.hour}"
        operator: gte
        value: 9
      - field: "{@time.hour}"
        operator: lt
        value: 17
      - field: "{@day.number}"
        operator: lte
        value: 5
  action:
    http:
      url: "https://hooks.slack.com/..."
      method: "POST"
      payload: '{"text": "{message}"}'
```

**When to reach for it:** Noisy or page-worthy actions that should only fire during certain hours. Combine with Pattern 7 for "only fire once per hour, only during business hours."

### 7. Per-key throttle

Suppress rapid-fire events per logical key — per device, per room, per user — rather than globally.

```yaml
- trigger:
    nats:
      subject: "sensors.motion.>"
  action:
    nats:
      subject: "alerts.motion"
      payload: '{"device": "{@subject.2}"}'
      debounce:
        window: "60s"
        key: "{@subject.2}"
```

**When to reach for it:** Flappy sensors or chatty event sources. The `key` field is the lever — without it, all messages share one window; with `{@subject.N}` or `{field}`, each distinct value gets its own.

### 8. Dynamic threshold from KV

Per-device thresholds stored in KV. One rule, many devices, no duplication.

```yaml
- trigger:
    nats:
      subject: "sensors.temperature"
  conditions:
    operator: and
    items:
      - field: "{value}"
        operator: gt
        value: "{@kv.sensor_config.{device_id}:max_temp}"
  action:
    nats:
      subject: "alerts.threshold"
      payload: |
        {
          "device": "{device_id}",
          "value": {value},
          "limit": "{@kv.sensor_config.{device_id}:max_temp}"
        }
```

**When to reach for it:** Different devices need different limits, and the limits need to change without redeploying rules. Update the KV entry, the next message sees the new value.

### 9. Wildcard fan-in / normalization

Pull multiple source subjects into a single normalized output. Downstream rules subscribe to the canonical subject and ignore vendor differences.

```yaml
- trigger:
    nats:
      subject: "vendors.*.vehicles.*.location"
  action:
    nats:
      subject: "fleet.location.{@subject.3}"
      payload: |
        {
          "vendor": "{@subject.1}",
          "vehicle_id": "{@subject.3}",
          "lat": {lat},
          "lon": {lon},
          "ts": "{@timestamp.iso}"
        }
```

**When to reach for it:** Multiple vendors push similar data on different subjects or with different schemas. Normalize once, write the rest of the system against the canonical shape.

---

## Advanced

### 10. Multi-stage pipeline

Decompose a complex rule into focused stages connected by intermediate subjects. Each stage is small and readable; multiple stage-2 rules can share one normalized feed.

```yaml
# Stage 1: normalize raw vendor format
- trigger:
    nats:
      subject: "raw.vendor-a.asset.>"
  action:
    nats:
      subject: "assets.location.{@subject.3}"
      payload: |
        {
          "asset_id": "{@subject.3}",
          "lat": {gps.latitude},
          "lon": {gps.longitude},
          "speed": {gps.speed},
          "ts": "{@timestamp.iso}"
        }

# Stage 2: alert on normalized stream
- trigger:
    nats:
      subject: "assets.location.>"
  conditions:
    operator: and
    items:
      - field: "{speed}"
        operator: gt
        value: 80
  action:
    nats:
      subject: "alerts.speed-exceeded"
      passthrough: true
```

**When to reach for it:** A single rule's conditions or template is getting hard to read, or you find yourself copy-pasting the same field-extraction logic across rules. Stage the normalization once.

### 11. Writing to KV (event → state)

A rule action can update a KV bucket by publishing to the bucket's underlying subject `$KV.<bucket>.<key>`. The message body becomes the KV value.

```yaml
- trigger:
    nats:
      subject: "events.location.{user_id}"
  action:
    nats:
      subject: "$KV.user_locations.{user_id}"
      payload: |
        {
          "region": "{region}",
          "lat": {lat},
          "lon": {lon},
          "updated_at": "{@timestamp.iso}"
        }
```

**When to reach for it:** Promoting an event stream into a "current state" view. Subsequent rules read the latest value with `{@kv.user_locations.{user_id}:region}`. The bucket's history setting (`nats kv add ... --history=N`) controls how much past state is retained.

### 12. Cross-source correlation

The engine processes one message at a time and has no built-in windowed join. To correlate events from two streams, write the first stream's state into KV (Pattern 11), then read it from the second stream's rule.

```yaml
# Source A: record current asset zone in KV
- trigger:
    nats:
      subject: "assets.location.>"
  action:
    nats:
      subject: "$KV.asset_zones.{asset_id}"
      payload: |
        {
          "zone": "{zone_id}",
          "ts": "{@timestamp.iso}"
        }

# Source B: sensor event in a zone — correlate against asset in the same zone
- trigger:
    nats:
      subject: "sensors.event.*"
  conditions:
    operator: and
    items:
      - field: "{@kv.asset_zones.{asset_id}:zone}"
        operator: eq
        value: "{@subject.2}"
      - field: "{@kv.asset_zones.{asset_id}:ts}"
        operator: recent
        value: "5m"
  action:
    nats:
      subject: "events.composite"
      payload: |
        {
          "zone": "{@subject.2}",
          "sensor_id": "{sensor_id}",
          "asset_id": "{asset_id}"
        }
```

**When to reach for it:** A decision depends on the state of two distinct event sources. Always bound staleness — without a `recent` check (or equivalent timestamp comparison), an hours-old KV value will still satisfy the correlation.

### 13. Polling-to-eventing bridge

The scheduler polls an API, the response is republished to NATS, a router rule reacts. Combines Patterns 5 and the `forEach` array iteration.

```yaml
# Scheduler: poll every 5 minutes, republish response
- trigger:
    schedule:
      cron: "*/5 * * * *"
  action:
    http:
      url: "https://api.example.com/devices/status"
      method: GET
      headers:
        Authorization: "Bearer ${API_TOKEN}"
      publishResponse:
        subject: "poll.devices.batch"

# Router: fan the batch out into per-device events
- trigger:
    nats:
      subject: "poll.devices.batch"
  action:
    nats:
      forEach: "{devices}"
      subject: "events.device.{device_id}.state"
      payload: |
        {
          "device_id": "{device_id}",
          "state": "{status}",
          "battery": {battery_percent}
        }
```

**When to reach for it:** An external system has no webhook channel but you want event-driven semantics downstream. The scheduler step is also the natural place to inject `nats-auth-manager`-managed OAuth tokens — see its [README](../README.md) for the full flow.

---

### 14. Request/reply service behind an HTTP endpoint

A NATS responder answers requests from a KV cache; the gateway exposes it over HTTP. The responder can scale horizontally via a queue group while the HTTP endpoint stays a single stable URL. See [02 Gateway — Request/reply](./02-gateway.md#requestreply-synchronous-responses-and-the-httpnats-bridge).

```yaml
# Router (features.router): answer geocode requests over core NATS
- trigger:
    nats:
      subject: "services.geocode"
      reply: true
      queue: "geocoders"        # load-balance across instances
  conditions:
    operator: and
    items:
      - field: "{country}"
        operator: eq
        value: "US"
  action:
    respond:
      headers:
        Content-Type: "application/json"
      payload: |
        {
          "address": "{address}",
          "lat": {@kv.geocache.{address}:lat},
          "lng": {@kv.geocache.{address}:lng}
        }

# Gateway (features.gateway): bridge HTTP → the NATS service → HTTP response
- trigger:
    http:
      path: "/api/geocode"
      method: "POST"
  action:
    nats:
      subject: "services.geocode"
      request: true
      timeout: "3s"
```

A request to `POST /api/geocode` is turned into a `nc.Request` on `services.geocode`, answered by whichever responder instance is free, and the reply is returned as the HTTP response. No responder → `503`; slow responder past `timeout` → `504`.

**Simpler variant — skip the bridge:** if the logic is purely local (enrichment, lookup, computed acknowledgment), an HTTP-triggered rule with a `respond` action returns the evaluated payload directly, with no NATS round trip:

```yaml
- trigger:
    http:
      path: "/api/quote"
      method: "POST"
  action:
    respond:
      payload: '{"symbol": "{symbol}", "price": {@kv.prices.{symbol}:last}}'
```

**When to reach for it:** Expose internal NATS services as HTTP APIs without standing up a separate HTTP service per responder (the bridge), or turn a rule into a programmable HTTP endpoint (the `respond` variant).

---

## Composing patterns

Most real systems combine several of these. A typical event-driven setup might use:

- **Pattern 13** (polling bridge) to pull data from a poll-only vendor API
- **Pattern 9** (normalization) to merge it with push-based sources into one canonical stream
- **Pattern 11** (event → state) to maintain current state in KV
- **Pattern 12** (correlation) to fire composite events across sources
- **Pattern 7** (per-key throttle) to keep downstream actions sane

The point of the patterns is that each one is small enough to reason about in isolation, while the composition stays explicit in YAML.

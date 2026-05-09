# Core Concepts

## Unified Binary & Feature Selection

`rule-router` is a single binary that bundles three features behind feature flags:

| Feature | Purpose | Triggers it owns |
|---------|---------|------------------|
| `router` | NATS-to-NATS routing (the original behavior) | `nats` |
| `gateway` | Inbound HTTP→NATS and outbound NATS→HTTP | `http`, plus `nats` for outbound |
| `scheduler` | Cron-based publishing to NATS or HTTP | `schedule` |

Enable features explicitly in the config:

```yaml
features:
  router: true
  gateway: false
  scheduler: false
```

…or via environment variables (`RR_FEATURES_GATEWAY=true`, `RR_FEATURES_SCHEDULER=true`).

If no `features` block is present, `router` defaults to `true` for backward compatibility. Multiple features can run in the same process and share NATS connections, the rule engine, the metrics endpoint, and KV state — there is no benefit to running them as separate processes when they share infrastructure.

`nats-auth-manager` and `rule-cli` remain separate binaries.

---

All features of rule-router are configured using a shared rule syntax. A **rule** is a YAML object composed of a `trigger`, optional `conditions`, and an `action`.

```yaml
# A rule is defined by a trigger, optional conditions, and an action.
- trigger:
    # ... defines what starts the rule evaluation (e.g., a NATS message or an HTTP request)
  conditions:
    # ... defines the logic to determine if the action should run (e.g., field value checks)
  action:
    # ... defines what to do if the conditions pass (e.g., publish a NATS message or make an HTTP call)
```

## 1. Triggers (The "If")

A trigger defines the event that initiates a rule evaluation. Each rule has exactly one trigger type.

**NATS Trigger** (router, gateway features): Evaluates a message from a NATS subject.
```yaml
trigger:
  nats:
    subject: "sensors.temperature.>" # Supports wildcards
```

**HTTP Trigger** (gateway feature): Evaluates an incoming HTTP request.
```yaml
trigger:
  http:
    path: "/webhooks/github"   # Exact path match
    method: "POST"             # Optional, defaults to all methods
```

**Schedule Trigger** (scheduler feature): Fires on a cron schedule.
```yaml
trigger:
  schedule:
    cron: "0 8 * * 1-5"           # Standard 5-field cron expression
    timezone: "America/New_York"   # Optional IANA timezone, defaults to system local
```

Schedule-triggered rules have no incoming message payload. Conditions can use time variables (`{@time.*}`, `{@day.*}`, `{@date.*}`) and KV lookups (`{@kv.*}`), but not message fields or header/subject context. Schedule rules support both NATS and HTTP actions — HTTP actions include configurable retry with exponential backoff.

## 2. Conditions (The "When")

Conditions are an optional block of logic that must evaluate to `true` for the action to be executed.

### Template Syntax

**All field references must use explicit template syntax:** `{variable}`

This consistent syntax applies to both the `field` (left side) and `value` (right side) of conditions.

**Supported Variable Types:**
- Message fields: `{temperature}`, `{user.name}`
- System variables: `{@time.hour}`, `{@subject.1}`, `{@header.X-Device-ID}`
- KV lookups: `{@kv.device_status.{device_id}:status}`

**Values can be:**
- Templates (for variable comparisons): `{threshold}`, `{@kv.config:max_temp}`
- Literals: `30`, `"active"`, `true`

### Basic Condition Examples

```yaml
conditions:
  operator: and # or "or"
  items:
    # Compare message field to literal
    - field: "{temperature}"
      operator: gte
      value: 30
    
    # Check an HTTP header
    - field: "{@header.X-Device-Auth}"
      operator: "exists"
    
    # Check the time of day
    - field: "{@time.hour}"
      operator: gte
      value: 9
    
    # Check a value from a NATS KV store
    - field: "{@kv.device_status.{device_id}:status}"
      operator: "eq"
      value: "active"
```

### Variable-to-Variable Comparisons (NEW!)

The rule engine now supports comparing variables to other variables, enabling dynamic thresholds and cross-field validation:

```yaml
conditions:
  operator: and
  items:
    # Compare two message fields
    - field: "{end_timestamp}"
      operator: gt
      value: "{start_timestamp}"
    
    # Compare message field to KV value
    - field: "{temperature}"
      operator: gt
      value: "{@kv.sensor_config.{sensor_id}:max_temp}"
    
    # Compare to system variable
    - field: "{last_update_hour}"
      operator: eq
      value: "{@time.hour}"
    
    # Permission level check
    - field: "{user_level}"
      operator: gte
      value: "{@kv.permissions.{resource_id}:required_level}"
```

### Type Handling

Variables are resolved to their native types for accurate comparison:
- **Numbers remain numbers**: `{temperature}` → `25.5` (float)
- **Strings remain strings**: `{status}` → `"active"`
- **Booleans remain booleans**: `{enabled}` → `true`
- **Automatic type coercion**: When comparing different types (e.g., string `"30"` vs number `30`)

This ensures numeric comparisons work correctly:
```yaml
# These both work correctly
- field: "{count}"        # count = 42 (number)
  operator: gt
  value: "{limit}"        # limit = 50 (number)

- field: "{count}"        # count = 42 (number)
  operator: gt
  value: "40"             # String "40" coerced to number
```

### Common Use Cases

**Dynamic Thresholds:**
```yaml
# Threshold stored in KV, different per sensor
- field: "{temperature}"
  operator: gt
  value: "{@kv.sensor_config.{sensor_id}:max_temp}"
```

**Cross-Field Validation:**
```yaml
# Ensure end time is after start time
- field: "{end_timestamp}"
  operator: gt
  value: "{start_timestamp}"
```

**Range Checks:**
```yaml
# Value must be within KV-defined range
conditions:
  operator: and
  items:
    - field: "{value}"
      operator: gte
      value: "{@kv.ranges.{sensor_type}:min}"
    - field: "{value}"
      operator: lte
      value: "{@kv.ranges.{sensor_type}:max}"
```

**Access Control:**
```yaml
# User level must meet or exceed required level
- field: "{user_level}"
  operator: gte
  value: "{@kv.permissions.{resource_id}:required_level}"
```

**Rate Limiting:**
```yaml
# Current usage must be below limit
- field: "{current_requests}"
  operator: lt
  value: "{@kv.rate_limits.{user_id}:requests_per_hour}"
```

### Available Operators

**Comparison:**
- `eq` - Equals
- `neq` - Not equals
- `gt` - Greater than
- `lt` - Less than
- `gte` - Greater than or equal
- `lte` - Less than or equal
- `exists` - Field exists (not null)

**String/Array:**
- `contains` - String contains substring or array contains element
- `not_contains` - Inverse of contains
- `in` - Value is in array
- `not_in` - Value is not in array

**Array Operators:**
- `any` - At least one array element matches nested conditions
- `all` - All array elements match nested conditions
- `none` - No array elements match nested conditions

**Time-Based:**
- `recent` - Timestamp is within time window (e.g., `"5s"`, `"1m"`, `"1h"`)

## 3. Actions (The "Then")

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
      maxDelay: "30s"
```

**HTTP Retry Configuration:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxAttempts` | int | `3` | Total attempts including the first. `1` disables retry. |
| `initialDelay` | duration string | `"1s"` | Delay before the second attempt. Format: Go duration (`"500ms"`, `"2s"`, `"1m"`). |
| `maxDelay` | duration string | `"30s"` | Cap on the backoff. Prevents runaway delays on long retry chains. |

Backoff doubles each attempt (`initialDelay`, `2×`, `4×`, …) up to `maxDelay`, and a small random jitter (≤100ms) is added each round to avoid thundering herds when multiple actions retry simultaneously. Retries are issued for any non-2xx response or transport error. The retry context honors graceful shutdown — in-flight retries are cancelled when the application terminates.

**Optional `publishResponse`**: Capture the HTTP response body and republish it to a NATS subject. On a 2xx response, the body (capped at 1 MB) is published as a NATS message — typically consumed by a router rule that performs the actual evaluation. Available wherever HTTP actions are used (scheduler cron triggers, gateway outbound NATS→HTTP, router NATS-trigger + HTTP-action rules).

```yaml
action:
  http:
    url: "https://api.example.com/devices/{device_id}/status"
    method: GET
    publishResponse:
      subject: "poll.devices.{device_id}.status"
```

| Field | Type | Description |
|-------|------|-------------|
| `subject` | string (required) | NATS subject to publish the response body to. Supports template syntax. |

**Behavior:**
- **Success-only**: the response is only published on a 2xx status. Non-2xx responses retry per `retry` config and never publish.
- **Body cap**: the body is read up to 1 MB; anything beyond is truncated.
- **Trigger-context templating**: the subject is templated against the trigger context only (cron + KV + system functions for scheduler, message + subject + KV for router). Response fields are **not** available in the subject — keeping templating consistent across the rule.
- **Best-effort publish**: a publish failure logs an error but does not fail the action (the HTTP call already succeeded; retrying it is wasteful).
- **Common pattern**: pair a scheduler poll with `publishResponse` and a router rule that subscribes to that subject — this turns "poll an API for state" into a normal event-driven flow.

### Action Payload Modes

Every action supports three payload modes:

| Mode | Field | Behavior |
|------|-------|----------|
| **Templated** (default) | `payload: "..."` | You define the complete output message. All `{variables}` are resolved. |
| **Passthrough** | `passthrough: true` | Forwards the original message unchanged. No payload field needed. |
| **Merge** | `merge: true` + `payload: "..."` | Deep-merges your payload overlay onto the original message. Original fields are preserved; overlay fields are added or overwritten. |

**Merge** is ideal for enrichment workflows where you want to add a few fields (e.g., a KV-derived tier, a UUID, a timestamp) without re-specifying every existing field:

```yaml
action:
  nats:
    subject: "enriched.orders"
    merge: true
    payload: |
      {
        "customer_tier": "{@kv.customers.{customer_id}:tier}",
        "processed_at": "{@timestamp()}",
        "trace_id": "{@uuid7()}"
      }
```

Given an input message `{"customer_id": "c1", "amount": 99.50, "items": [...]}`, the output would contain all original fields plus the three overlay fields.

**Merge semantics:**
- Overlay values overwrite base values for matching keys
- Nested objects are merged recursively (not replaced wholesale)
- Arrays in the overlay replace arrays in the base entirely
- If both `passthrough` and `merge` are set, `passthrough` wins
- In `forEach` context, the merge base is the current array element (use `{@msg.field}` to pull root fields into the overlay)

## 4. Debounce / Throttle

Rules support an optional `debounce` block on triggers and/or actions to suppress rapid-fire processing. It uses **fire-first** semantics: the first message is processed immediately, and subsequent messages within the `window` are suppressed.

### Trigger Debounce

Skips rule evaluation entirely for the duration of the window. Suppressed messages are still ACKed (not redelivered).

```yaml
- trigger:
    nats:
      subject: "sensors.temperature.>"
      debounce:
        window: "5s"              # Suppress for 5 seconds after first message
        key: "{@subject}"         # Optional. Defaults to full subject/path.
  action:
    nats:
      subject: "alerts.temperature"
      payload: '{"temp": {temperature}}'
```

### Action Debounce

Conditions are still evaluated (the rule "matches"), but action execution is suppressed within the window.

```yaml
- trigger:
    nats:
      subject: "sensors.temperature.>"
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
        key: "{@subject.2}"     # Debounce per room (e.g., "room1")
```

### Debounce Key

The `key` field controls what gets debounced independently. It supports template syntax for grouping:

| Key | Behavior |
|-----|----------|
| *(omitted)* | Defaults to the full NATS subject or HTTP path |
| `"{@subject}"` | Same as default for NATS triggers |
| `"{@subject.2}"` | Debounce per subject token (e.g., per room) |
| `"{sensor_id}"` | Debounce per message field value |
| `"{@path.1}"` | Debounce per HTTP path segment |

Each rule maintains its own independent debounce state, so two rules on the same subject never interfere with each other. Within a single rule, however, **messages whose `key` template resolves to the same value share a window** — that is exactly the point of using a non-default key. For example, with `key: "{@subject.2}"`, all messages where the third subject token resolves to `"room1"` share a window, while messages resolving to `"room2"` are tracked separately.

State is held in-memory per Processor instance. A SIGHUP reload (which rebuilds the Processor) clears all debounce state — useful to know if you are watching for an alert immediately after a config change.

### Using Both Together

Trigger and action debounce can be combined on a single rule:

```yaml
- trigger:
    nats:
      subject: "sensors.temperature.>"
      debounce:
        window: "5s"           # Don't evaluate more than once per 5s
  action:
    nats:
      subject: "alerts.high_temp"
      payload: '{"alert": true}'
      debounce:
        window: "30s"          # Don't fire more than one alert per 30s
```

## Environment Variables

The rule engine supports environment variable expansion for static configuration values using `${VAR_NAME}` syntax. This enables secure secret management and environment-specific configuration without hardcoding values in rule files.

### How It Works

Environment variables are expanded **at load time** (when rules are parsed from YAML files), not at runtime. This means:

- ✅ **Performance**: Zero runtime overhead - values are substituted once during startup
- ✅ **Security**: Secrets are never stored in rule files
- ✅ **Simplicity**: Standard environment variable management (Docker, K8s, systemd, etc.)
- ✅ **Validation**: Expanded values are validated along with the rest of the rule

**Important**: Environment variable expansion is completely separate from template variable substitution:
- `${ENV_VAR}` → Expanded at **load time** (static configuration)
- `{field}` or `{@system}` → Resolved at **runtime** (per-message templating)

### Syntax

```yaml
# Use ${VARIABLE_NAME} anywhere in your rules
action:
  http:
    url: "https://api.example.com"
    headers:
      Authorization: "Bearer ${API_TOKEN}"
```

### Where Can I Use Them?

Environment variables can be used in **both conditions and actions**:

#### In Actions

**NATS Actions:**
```yaml
action:
  nats:
    subject: "alerts.${ENVIRONMENT}.critical"
    payload: |
      {
        "apiKey": "${SERVICE_API_KEY}",
        "region": "${AWS_REGION}"
      }
    headers:
      X-Service-Token: "${INTERNAL_TOKEN}"
```

**HTTP Actions:**
```yaml
action:
  http:
    url: "${API_BASE_URL}/incidents"
    method: "POST"
    headers:
      Authorization: "Token ${PAGERDUTY_TOKEN}"
      X-Environment: "${DEPLOY_ENV}"
    payload: '{"service": "${SERVICE_NAME}"}'
```

#### In Conditions

Environment variables can also be used in condition values:

```yaml
conditions:
  operator: and
  items:
    # Check if status matches expected value from env
    - field: "{status}"
      operator: eq
      value: "${EXPECTED_STATUS}"
    
    # Check if environment matches
    - field: "{environment}"
      operator: in
      value: ["${PRIMARY_ENV}", "${SECONDARY_ENV}"]
```

### Missing Variables

If an environment variable is not set, the system will:

1. **Log a warning** with details about the missing variable
2. **Substitute with an empty string**
3. **Continue loading** the rule (non-fatal)

**Best Practice**: Always set required environment variables before starting the application, or the rule may not work as intended.

## Complete Example: Variable Comparisons with KV

```yaml
# Dynamic threshold management with KV store
# KV: sensor_config["temp-001"] = {"max_temp": 35, "critical_temp": 45}
# Message: {"sensor_id": "temp-001", "temperature": 38, "location": "server_room"}

- trigger:
    nats:
      subject: "sensors.temperature"
  
  conditions:
    operator: and
    items:
      # Temperature exceeds configured max (warning level)
      - field: "{temperature}"
        operator: gt
        value: "{@kv.sensor_config.{sensor_id}:max_temp}"
      
      # But below critical level
      - field: "{temperature}"
        operator: lt
        value: "{@kv.sensor_config.{sensor_id}:critical_temp}"
      
      # Only during business hours
      - field: "{@time.hour}"
        operator: gte
        value: 9
      - field: "{@time.hour}"
        operator: lt
        value: 17
  
  action:
    nats:
      subject: "alerts.temperature.warning.{sensor_id}"
      payload: |
        {
          "alert": "Temperature warning - exceeds threshold",
          "sensor_id": "{sensor_id}",
          "location": "{location}",
          "current_temperature": {temperature},
          "thresholds": {
            "max": "{@kv.sensor_config.{sensor_id}:max_temp}",
            "critical": "{@kv.sensor_config.{sensor_id}:critical_temp}"
          },
          "triggered_at": "{@timestamp.iso}",
          "alert_id": "{@uuid7()}"
        }
```

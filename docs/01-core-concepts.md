# Core Concepts

All applications in this repository are configured using a shared rule syntax. A **rule** is a YAML object composed of a `trigger`, optional `conditions`, and an `action`.

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
```

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

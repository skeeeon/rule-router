# System Variables & Functions Reference

The rule engine provides a rich set of system variables (prefixed with `@`) that give you access to context data, time information, NATS/HTTP metadata, and more.

## Message Fields

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{fieldName}` | Access any field from the message payload | `{temperature}` → `23.5` |
| `{nested.field}` | Access nested fields using dot notation | `{user.profile.email}` |
| `{@msg.field}` | Explicitly access root message (important in forEach) | `{@msg.batchId}` |
| `{@value}` | Access primitive value (strings, numbers, booleans at root or in arrays) | `{@value}` → `"ERROR: timeout"` |
| `@items` | Access array at root level | Field reference for root arrays |

## NATS Subject Context (`rule-router` only)

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@subject}` | Full NATS subject | `sensors.temperature.room1` |
| `{@subject.0}` | First token of subject | `sensors` |
| `{@subject.1}` | Second token of subject | `temperature` |
| `{@subject.N}` | Nth token (zero-indexed) | `room1` |
| `{@subject.count}` | Number of tokens in subject | `3` |

## HTTP Context (`http-gateway` only)

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@path}` | Full HTTP path | `/webhooks/github/pr` |
| `{@path.0}` | First path segment | `webhooks` |
| `{@path.1}` | Second path segment | `github` |
| `{@path.N}` | Nth path segment (zero-indexed) | `pr` |
| `{@path.count}` | Number of path segments | `3` |
| `{@method}` | HTTP method | `POST` |

## Headers (Both NATS and HTTP)

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@header.HeaderName}` | Access any header value | `{@header.X-Request-ID}` |
| `{@header.Content-Type}` | Common header access | `application/json` |
| `{@header.Authorization}` | Auth header access | `Bearer token123` |

## Time & Date

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@time.hour}` | Current hour (0-23) | `14` |
| `{@time.minute}` | Current minute (0-59) | `30` |
| `{@day.name}` | Day of week (lowercase) | `monday` |
| `{@day.number}` | Day of week (1-7, Monday=1) | `1` |
| `{@date.year}` | Current year | `2025` |
| `{@date.month}` | Current month (1-12) | `10` |
| `{@date.day}` | Day of month (1-31) | `29` |
| `{@date.iso}` | ISO date format | `2025-10-29` |
| `{@timestamp.unix}` | Unix timestamp (seconds) | `1730217600` |
| `{@timestamp.iso}` | ISO timestamp | `2025-10-29T17:30:00Z` |

## Key-Value Store

| Variable | Description | Example |
|----------|-------------|---------|
| `@kv.bucket.key` | Lookup value from KV store | `@kv.users.username` |
| `@kv.bucket.key:field` | Lookup value from KV store with JSON path | `@kv.users.{userId}:name` |
| `@kv.bucket.key:nested.field` | Nested field access in KV value | `@kv.config.app:db.host` |

**Syntax:** `@kv.{bucketName}.{keyName}:{jsonPath}`

**Examples:**
```yaml
# Simple field access
field: "@kv.device_status.sensor-123"

# Variable in key name and JSON path
field: "@kv.users.{user_id}:permissions"

# Nested JSON path
field: "@kv.config.app:database.connection.host"
```

## Cryptographic Signatures

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@signature.valid}` | Whether signature verification passed | `true` / `false` |
| `{@signature.pubkey}` | Signer's public key | `UDXU4RCRBVXEZ...` |

**Note:** Requires signature verification to be enabled in configuration. See the [Security documentation](./05-security.md) for details.

## Template Functions

| Function | Description | Example Output |
|----------|-------------|----------------|
| `{@timestamp()}` | Generate current timestamp (RFC3339) | `2025-10-29T17:30:00Z` |
| `{@uuid7()}` | Generate time-ordered UUID v7 | `018b7e5a-f3c2-7000-8000-0123456789ab` |
| `{@uuid4()}` | Generate random UUID v4 | `550e8400-e29b-41d4-a716-446655440000` |

## Condition Operators

All system variables can be used in conditions with these operators:

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

## Complete Example: Using Multiple System Variables

```yaml
- trigger:
    nats:
      subject: "sensors.*.>"
  
  conditions:
    operator: and
    items:
      # Time-based: only during business hours
      - field: "@time.hour"
        operator: gte
        value: 9
      - field: "@time.hour"
        operator: lt
        value: 17
      
      # Day-based: weekdays only
      - field: "@day.number"
        operator: lte
        value: 5
      
      # KV lookup: check if sensor is active
      - field: "@kv.sensors.{@subject.1}:active"
        operator: eq
        value: true
      
      # Value check
      - field: "temperature"
        operator: gt
        value: 25
  
  action:
    nats:
      subject: "alerts.{@subject.1}.{@date.iso}"
      payload: |
        {
          "sensor": "{@subject.1}",
          "location": "{@subject.2}",
          "temperature": {temperature},
          "sensorName": "{@kv.sensors.{@subject.1}:name}",
          "timestamp": "{@timestamp()}",
          "alertId": "{@uuid7()}",
          "dayOfWeek": "{@day.name}",
          "hour": {@time.hour}
        }
```

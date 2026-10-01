# System Variables & Functions Reference

The rule engine provides a rich set of system variables (prefixed with `@`) that give you access to context data, time information, NATS/HTTP metadata, and more.

## Message Fields

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{fieldName}` | Access any field from the message payload | `{temperature}` → `23.5` |
| `{nested.field}` | Access nested fields using dot notation | `{user.profile.email}` |
| `{@msg.field}` | Explicitly access root message (important in forEach) | `{@msg.batchId}` |
| `{@value}` | Access primitive value (strings, numbers, booleans at root or in arrays) | `{@value}` → `"ERROR: timeout"` |
| `{@items}` | Access array at root level | Field reference for root arrays |

**Note:** Schedule-triggered rules (scheduler feature) have no incoming message, so only Time & Date, Key-Value Store, and Template Functions are available. Message fields, subject context, HTTP context, and headers are not populated.

## NATS Subject Context (router and gateway features)

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@subject}` | Full NATS subject | `sensors.temperature.room1` |
| `{@subject.0}` | First token of subject | `sensors` |
| `{@subject.1}` | Second token of subject | `temperature` |
| `{@subject.N}` | Nth token (zero-indexed) | `room1` |
| `{@subject.count}` | Number of tokens in subject | `3` |

## HTTP Context (gateway feature only)

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@path}` | Full HTTP path | `/webhooks/github/pr` |
| `{@path.0}` | First path segment | `webhooks` |
| `{@path.1}` | Second path segment | `github` |
| `{@path.N}` | Nth path segment (zero-indexed) | `pr` |
| `{@path.count}` | Number of path segments | `3` |
| `{@method}` | HTTP method | `POST` |
| `{@query.name}` | Query parameter by name | `?tenant=acme` → `acme` |

### Query parameters

Query parameters live in their own `{@query.name}` namespace and are **never merged into the message**. That separation is deliberate: anyone who can reach the URL can append a parameter, so if `?user_id=1` could shadow a body field, appending it to a webhook URL would be a privilege-escalation path. A rule that wants a query value has to name it explicitly.

- **Names are case-sensitive.** `{@query.tenant}` and `{@query.Tenant}` are different parameters. (Headers are the opposite — HTTP defines those as case-insensitive.)
- **Values are always strings**, like headers. Conditions coerce them, so `operator: gte, value: 2` works against `?version=3`.
- **A repeated name keeps only its first value.** `?tag=a&tag=b` gives `{@query.tag}` → `a`.
- **An absent parameter renders as an empty string** in templates and fails any condition except `exists`.
- **The query never affects routing.** Rule matching and the Prometheus `path` label key off the path alone, so `?anything=goes` can't turn a matching path into a 404 — nor make a non-matching one match.

```yaml
- trigger:
    http:
      path: /webhooks/acme
      method: POST
  conditions:
    operator: and
    items:
      - field: "{@query.tenant}"
        operator: exists
  action:
    nats:
      subject: "events.{@query.tenant}.ingest"
      payload: |
        {"tenant": "{@query.tenant}", "page": "{@query.page}"}
```

Test it with `rule-cli check --query 'tenant=acme&page=2'`, or the **Query Params** field in the web rule tester.

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
| `{@kv.bucket.key}` | Lookup value from KV store | `@kv.users.username` |
| `{@kv.bucket.key:field}` | Lookup value from KV store with JSON path | `@kv.users.{userId}:name` |
| `{@kv.bucket.key:nested.field}` | Nested field access in KV value | `@kv.config.app:db.host` |

**Syntax:** `{@kv.{bucketName}.{keyName}:jsonPath.nestedValue}`

**Examples:**
```yaml
# Simple field access
field: "{@kv.device_status.sensor-123}"

# Variable in key name and JSON path
field: "{@kv.users.{user_id}:permissions}"

# Nested JSON path
field: "{@kv.config.app:database.connection.host}"
```

## Cryptographic Signatures

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@signature.valid}` | Whether signature verification passed | `true` / `false` |
| `{@signature.pubkey}` | Signer's public key | `UDXU4RCRBVXEZ...` |

**Note:** Requires signature verification to be enabled in configuration. See the [Security documentation](./07-security.md) for details.

## Template Functions

| Function | Description | Example Output |
|----------|-------------|----------------|
| `{@timestamp()}` | Generate current timestamp (RFC3339) | `2025-10-29T17:30:00Z` |
| `{@uuid7()}` | Generate time-ordered UUID v7 | `018b7e5a-f3c2-7000-8000-0123456789ab` |
| `{@uuid4()}` | Generate random UUID v4 | `550e8400-e29b-41d4-a716-446655440000` |
| `{@random.int(min,max)}` | Random integer, both ends inclusive | `42` |
| `{@random.float(min,max,decimals)}` | Random float at a fixed number of decimal places | `-18.3` |
| `{@random.choice(a,b,...)}` | One of the listed values | `open` |

### Random functions are for synthetic data

```yaml
- trigger:
    schedule:
      cron: "*/5 * * * * *"
  action:
    nats:
      subject: "telemetry.probe.TP-001"
      payload: |
        {
          "celsius": {@random.float(-19.4,-17.2,1)},
          "battery": {@random.int(80,100)},
          "door": "{@random.choice(open,closed)}"
        }
```

These generate **fixture data** — demo telemetry, simulated readings, placeholder payloads. They are not general-purpose:

| If you want | Use |
|---|---|
| A nonce | `{@uuid4()}` |
| A correlation id | `{@uuid7()}` |
| A/B bucketing | A hash of a stable field — a random draw re-buckets on retry |
| Retry jitter | Already built into the publisher and HTTP client |

Four things to know:

**Quoting differs, and it has to.** `random.int` and `random.float` render bare numbers, so write them **unquoted** in JSON. `random.choice` renders a string and must be **quoted**. This is the same split as `{@time.hour}` (bare) versus `{@uuid7()}` (quoted) and is not new behaviour.

**Arguments are literals, separated by commas, with no quoting or escaping.** A value cannot contain a comma or a space: `{@random.choice(open,closed)}` works, `{@random.choice(door open,door closed)}` cannot be expressed. Nested templates *are* allowed — `{@random.int(1,{max})}` resolves `{max}` first — but a call written that way is checked at runtime rather than at load.

**Malformed calls are rejected at load.** `{@random.int(a,100)}` fails at startup (or on KV hot-reload) with the offending call quoted, rather than rendering an empty string mid-payload and producing invalid JSON at runtime.

**A redelivered message re-evaluates.** For a JetStream router rule, a redelivery renders a *new* random value, so a redelivered temperature reading differs from the first attempt. Schedule rules have no redelivery and are unaffected — which is the intended use case, so this is a footnote rather than a warning. Publish retries are also unaffected: the payload is rendered once, before the retry loop, so every attempt sends identical bytes.

**Not** an expression language: there is no arithmetic (`{@time.minute * 2}`), no random in condition evaluation, and no stateful signals (random walks, hysteresis). `random.float` over a tight range produces *bounded noise*, not a trend — right for a probe that jitters, wrong for anything expected to show a shape. That needs a device simulator, not a template function.

## Condition Operators

All system variables can be used in conditions with these operators:

**Comparison:**
- `eq` - Equals
- `neq` - Not equals
- `gt` - Greater than
- `lt` - Less than
- `gte` - Greater than or equal
- `lte` - Less than or equal
- `exists` - Field is present and not null
- `not_exists` - Field is missing or null

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

### `recent` Operator Details

`recent` compares a **field value** (interpreted as a timestamp) against the **current system clock** at the moment the rule is evaluated.

```yaml
- field: "{event_time}"   # value from the message
  operator: recent
  value: "30s"            # tolerance window (Go duration string)
```

**Field formats accepted:**
- Unix seconds as a number: `1730217600` or `1730217600.5`
- RFC3339 string: `"2025-10-29T17:30:00Z"`

**Semantics:**
- The condition matches when `now - field <= window`.
- Future timestamps are tolerated within a fixed **5-second clock-skew allowance**; anything more than 5s in the future fails.
- Invalid duration strings or unparseable timestamps cause the condition to evaluate `false` (not error).

This is most commonly used to drop stale events from queue backlogs (`recent: "1m"` to ignore anything older than a minute).

### `exists` / `not_exists` Details

Both take no `value`. They answer one question — is the field there? — and are exact inverses:

| Field in message | `exists` | `not_exists` |
|---|---|---|
| Missing (`{}`) | false | **true** |
| `null` | false | **true** |
| Present, even `""`, `0`, `false`, or `[]` | **true** | false |

The same holds for nested paths (`{device.location.zone}` is missing if any step is), header and query lookups, and KV lookups (a missing key or JSON path counts as missing).

`not_exists` is the only way to act on a field's **absence**. Every other operator — including `neq` and `not_in` — is `false` on a missing field, and condition groups have no `not`. Many sources signal state by omission: OwnTracks drops `inregions` when you are outside every region, many devices drop a sensor key when the sensor is unplugged. Pair the two operators to give each case its own rule:

```yaml
# In a region: copy the array
- field: "{inregions}"
  operator: exists
# Outside every region: the rule writes [] instead
- field: "{inregions}"
  operator: not_exists
```

See [`rules/router/owntracks.yaml`](../rules/router/owntracks.yaml) for the full pair, and [09 Patterns — Writing to KV](./09-patterns.md#11-writing-to-kv-event--state) for why an optional array needs it.

## Worked examples

For end-to-end examples combining time, KV, subject context, and template functions, see [09 Patterns](./09-patterns.md).

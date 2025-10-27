# Rule Router

Rule Router is a high-performance NATS-to-NATS message router that evaluates JSON messages against configurable rules and publishes templated actions. It is a core component of the Rule-Based Messaging Platform and shares its powerful rule engine with the `http-gateway`.

This application is purpose-built for internal, high-throughput message routing, filtering, enrichment, and security validation within your NATS infrastructure.

## Features

*   **High Performance** - Microsecond rule evaluation, thousands of messages per second.
*   **Array Processing** - Native support for batch messages with array operators and forEach iteration.
*   **NATS JetStream Native** - Built on pull consumers for durable, scalable subscriptions.
*   **Intelligent Stream Selection** - Automatically selects the optimal stream for consumption, preferring memory storage and specific subject filters.
*   **Cryptographic Security** - NKey signature verification and replay protection for secure workflows.
*   **Key-Value Store Integration** - Dynamic lookups with JSON path traversal and a local cache for ~25x faster lookups.
*   **Time-Based Rules** - Schedule-aware evaluation without external schedulers.
*   **Pattern Matching** - NATS wildcards (`*` and `>`) with subject token access.
*   **Template Engine** - Powerful variable substitution with nested field support and built-in functions.
*   **Action Headers** - Add, override, and template NATS headers in rule actions.
*   **Zero-Copy Passthrough** - Forward messages unchanged for high-performance filtering.
*   **Production Ready** - Prometheus metrics, structured logging, graceful shutdown, and full NATS authentication support.

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                     NATS JetStream                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────┐  │
│  │ Streams  │  │ Consumers│  │ KV Stores│  │ KV Changes│  │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └─────┬─────┘  │
└───────┼─────────────┼─────────────┼──────────────┼────────┘
        │             │             │              │
        ▼             ▼             ▼              ▼
┌───────────────────────────────────────────────────────────┐
│                      Rule Router                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │ Subscription │  │ Rule Engine  │  │ Local Cache  │     │
│  │ Manager      │──│ + Templates  │──│ (KV Mirror)  │     │
│  │              │  │ + Conditions │  │ (Real-time)  │     │
│  │              │  │ + Security   │  │              │     │
│  │              │  │ + Arrays     │  │              │     │
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘     │
│         └─────────────────┴───► Publish NATS Actions      │
└───────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

*   Go 1.23+
*   NATS Server with JetStream enabled
*   **JetStream streams must exist before starting `rule-router`**

### Setup

1.  **Start NATS with JetStream**
    ```bash
    docker run -d --name nats-js -p 4222:4222 nats:latest -js
    ```

2.  **Create Streams**
    `rule-router` requires streams for both consuming messages (triggers) and publishing messages (actions).

    ```bash
    # Stream for rule triggers (input)
    nats stream add SENSORS --subjects "sensors.>"

    # Stream for rule actions (output)
    nats stream add ALERTS --subjects "alerts.>"
    ```

3.  **Create a Rule**
    Create a file at `rules/temperature.yaml`:

    ```yaml
    - trigger:
        nats:
          subject: sensors.temperature
      conditions:
        operator: and
        items:
          - field: temperature
            operator: gt
            value: 30
      action:
        nats:
          subject: alerts.high-temperature
          payload: |
            {
              "alert": "High temperature detected",
              "temperature": {temperature},
              "timestamp": "{@timestamp()}",
              "alert_id": "{@uuid7()}"
            }
    ```

4.  **Run `rule-router`**
    ```bash
    # Assumes a config file exists at config/rule-router.yaml
    ./rule-router -config config/rule-router.yaml -rules rules/
    ```

5.  **Test the Rule**
    Publish a message that should match the rule:
    ```bash
    nats pub sensors.temperature '{"temperature": 35}'
    ```

    Verify the output by subscribing to the action subject:
    ```bash
    nats sub alerts.high-temperature
    ```

## Configuration

See `config/rule-router.yaml` for a fully documented example. Key sections include:

```yaml
nats:
  urls: ["nats://localhost:4222"]
  
  consumers:
    subscriberCount: 8       # Workers per subscription
    fetchBatchSize: 64       # Messages per fetch
    ackWaitTimeout: 30s      # Redelivery delay
    maxDeliver: 3            # Max redelivery attempts

security:
  verification:
    enabled: false           # Enable signature verification
    publicKeyHeader: "Nats-Public-Key"
    signatureHeader: "Nats-Signature"

kv:
  enabled: true
  buckets: ["device_status", "customer_data"]
  localCache:
    enabled: true            # ~25x faster lookups

forEach:
  maxIterations: 100         # Max array elements per forEach

logging:
  level: info
  encoding: json

metrics:
  enabled: true
  address: :2112
```

## Rule Syntax

### Basic Structure

A rule is defined by a `trigger`, optional `conditions`, and an `action`. The `rule-router` specifically looks for rules with a `nats` trigger and a `nats` action.

```yaml
- trigger:
    nats:
      subject: input.subject          # Supports wildcards: *, >
  conditions:
    operator: and                     # "and" or "or"
    items:
      - field: fieldName
        operator: eq                  # eq, neq, gt, lt, gte, lte, exists, contains, etc.
        value: expectedValue
  action:
    nats:
      subject: output.subject
      payload: "JSON template"          # Mutually exclusive with 'passthrough'
      headers:                          # Optional: Add/override NATS headers
        X-Custom-Header: "static value"
        X-Dynamic-Header: "{field_from_message}"
```

### Action Configuration

Actions can either **template** a new message or **passthrough** the original message payload without modification.

**Templated Action (default):**
```yaml
action:
  nats:
    subject: output.subject
    payload: |
      {
        "transformed_field": "{original_field}",
        "timestamp": "{@timestamp()}"
      }
    headers:
      X-Priority: "high"
```

**Passthrough Action:**
```yaml
action:
  nats:
    subject: output.subject
    passthrough: true  # Forwards original message payload
    headers:
      X-Processed-By: "rule-router"
```

**Important:**
*   You must specify either `payload` OR `passthrough: true`. They are mutually exclusive.
*   Subject and header templating works with both modes.
*   Passthrough preserves the exact original message bytes.

### Array Processing

The rule-router supports powerful array operations for batch message processing.

#### Array Operators in Conditions

Check if any/all/none of the array elements match conditions:

```yaml
conditions:
  operator: and
  items:
    # Check if ANY notification is critical
    - field: "notifications"
      operator: any
      conditions:
        - field: "severity"
          operator: eq
          value: "critical"
```

**Available Operators:**
- `any`: At least one element matches
- `all`: All elements match  
- `none`: No elements match

#### ForEach Actions

Generate **one action per array element**:

```yaml
action:
  forEach: "notifications"   # Path to array field
  filter:                     # Optional: only process matching elements
    - field: "severity"
      operator: eq
      value: "critical"
  nats:
    subject: "alerts.{id}"
    payload: |
      {
        "id": "{id}",                    # From notifications[i]
        "message": "{message}",          # From notifications[i]
        "deviceId": "{@msg.deviceId}",   # From root message
        "processedAt": "{@timestamp()}"
      }
```

**Template Context Rules:**
- `{field}` → Resolves to current array element field
- `{@msg.field}` → Resolves to root message field (use for batch-level data)

**Complete Example:**

```yaml
- trigger:
    nats:
      subject: "device.batch.>"
    
  conditions:
    operator: and
    items:
      # Pre-filter: Check if ANY event is motion-related
      - field: "events"
        operator: any
        conditions:
          - field: "type"
            operator: eq
            value: "motion"
  
  action:
    # Generate one alert per motion event
    forEach: "events"
    filter:
      - field: "type"
        operator: eq
        value: "motion"
    nats:
      subject: "alerts.motion.{deviceId}"
      payload: |
        {
          "deviceId": "{deviceId}",
          "location": "{location}",
          "timestamp": "{timestamp}",
          "batchId": "{@msg.batchId}",
          "receivedAt": "{@msg.receivedAt}"
        }
```

**Performance:**
- Default limit: 100 iterations per forEach
- Configure via `forEach.maxIterations` in config
- Efficient zero-copy element context creation
- Comprehensive metrics for monitoring

For complete examples, see [examples/forEach/](../../examples/forEach/).

### Condition Operators & System Fields

The `rule-router` uses the shared rule engine, which supports a rich set of operators and system variables for building complex logic.

*   **Comparison**: `eq`, `neq`, `gt`, `lt`, `gte`, `lte`, `exists`
*   **String/Array**: `contains`, `not_contains`, `in`, `not_in`
*   **Array**: `any`, `all`, `none` (with nested conditions)
*   **Time-Based**: `recent` (for replay protection)
*   **System Fields**:
    *   `@time.hour`, `@day.name`, `@date.iso`
    *   `@subject`, `@subject.0`, `@subject.count`
    *   `@header.Nats-Msg-Id`
    *   `@signature.valid`, `@signature.pubkey`
    *   `@kv.bucket.key:path`
    *   `@msg.field` (in forEach context)
*   **Template Functions**: `{@timestamp()}`, `{@uuid7()}`, `{@uuid4()}`

For a complete reference on these features, please see the main project README.

## Security Features

`rule-router` supports cryptographic message verification using NATS NKeys, enabling secure workflows like privileged command execution and non-repudiable event logging.

### Example: Physical Access Control

This rule demonstrates signature verification, replay protection, and KV-based authorization.

**KV Setup:**
```bash
nats kv add access_control
nats kv put access_control UDXU4RCRBVXEZ... \
  '{"active": true, "doors": ["front", "back"], "name": "alice"}'
```

**Rule:**
```yaml
- trigger:
    nats:
      subject: cmds.door.unlock
  conditions:
    operator: and
    items:
      # 1. Verify cryptographic signature is valid
      - field: "@signature.valid"
        operator: eq
        value: true
      
      # 2. Prevent replay attacks (5 second window)
      - field: timestamp # Unix seconds from payload
        operator: recent
        value: "5s"
      
      # 3. Check if the user's key is active in the KV store
      - field: "@kv.access_control.{@signature.pubkey}:active"
        operator: eq
        value: true
      
      # 4. Verify the user has access to the requested door
      - field: "@kv.access_control.{@signature.pubkey}:doors"
        operator: contains
        value: "{door}"
  
  action:
    nats:
      subject: hardware.door.{door}.trigger
      payload: |
        {
          "door": "{door}",
          "action": "unlock",
          "authorized_by": "{@kv.access_control.{@signature.pubkey}:name}",
          "timestamp": "{@timestamp()}",
          "request_id": "{@uuid7()}"
        }
```

For details on client-side implementation and security best practices, please refer to the main project README.

## Testing Rules

This project includes a standalone `rule-tester` utility for offline validation of rule logic. It allows you to lint, scaffold, and run test suites against your rules with mocked dependencies.

**Example Usage:**
```bash
# Scaffold tests for a new rule (auto-detects forEach)
rule-tester --scaffold ./rules/my-new-rule.yaml

# Run all tests
rule-tester --test --rules ./rules

# Quick check a single message
rule-tester --rule ./rules/my-rule.yaml \
            --message ./test-data/message.json
```

**ForEach Rule Testing:**
The tester automatically detects forEach operations and generates:
- Array input examples
- Multiple action output validation
- Filter condition test cases
- Empty array edge cases

For complete documentation, see the [**`rule-tester` README**](../rule-tester/README.md).

## Monitoring

The rule-router exposes Prometheus metrics on port `:2112` (configurable).

### Key Metrics

**Message Processing:**
```
messages_total{status="received|processed|error"}
rule_matches_total
actions_total{status="success|error"}
message_processing_backlog
```

**Array Operations:**
```
# ForEach processing
forEach_iterations_total{rule_file="batch_notifications"}
forEach_filtered_total{rule_file="batch_notifications"}
forEach_actions_generated_total{rule_file="batch_notifications"}
forEach_duration_seconds{rule_file="batch_notifications"}

# Array operators
array_operator_evaluations_total{operator="any|all|none",result="true|false"}
```

**Performance:**
```
# Rule evaluation and template processing times
signature_verification_duration_seconds
```

**NATS:**
```
nats_connection_status
nats_reconnects_total
```

**KV Operations:**
```
kv_cache_hits_total
kv_cache_misses_total
kv_cache_size
```

### Example Queries

**Average forEach processing time:**
```promql
rate(forEach_duration_seconds_sum[5m]) / rate(forEach_duration_seconds_count[5m])
```

**ForEach filter efficiency:**
```promql
rate(forEach_filtered_total[5m]) / rate(forEach_iterations_total[5m])
```

**Actions per forEach operation:**
```promql
rate(forEach_actions_generated_total[5m]) / rate(forEach_iterations_total[5m])
```

## Troubleshooting

### Common Issues

**"No stream found for subject"**
This means a rule's trigger subject (e.g., `sensors.data`) does not match any subject filter in any existing JetStream stream.
```bash
# Solution: Create a stream that covers the subject
nats stream add SENSORS --subjects "sensors.>"
```

**ForEach array exceeds limit**
```
Error: forEach array exceeds limit: 150 elements > 100 max iterations
```
Solution: Increase the limit in configuration:
```yaml
forEach:
  maxIterations: 200
```

**Signature verification always returns false**
*   Ensure `security.verification.enabled: true` in your config.
*   Check that the `Nats-Public-Key` and `Nats-Signature` headers are present on the message.
*   Verify the signature is computed over the **raw payload bytes only**.

**Recent operator always returns false**
*   Ensure a `timestamp` field exists in the payload in Unix seconds format.
*   Check for clock skew between the publisher and the `rule-router` machine.

**ForEach not generating expected actions**
*   Check if filter conditions are too restrictive
*   Verify array field exists and contains objects
*   Use `@msg` prefix for root message fields
*   Monitor `forEach_filtered_total` metric to see how many elements were filtered

## CLI Options

```bash
rule-router [options]

Options:
  -config string
        Path to config file (default "config/rule-router.yaml")
  -rules string
        Path to rules directory (default "rules")
```

## Performance

**Benchmarks** (Intel i7, 16GB RAM):
- Rule evaluation: ~50-100µs per message
- Template processing: ~20-50µs per action
- ForEach (10 elements): ~400µs
- ForEach (100 elements): ~3.5ms
- Throughput: 10,000+ messages/second (single instance)

**Optimizations:**
- Zero-copy message forwarding (passthrough mode)
- Short-circuit condition evaluation
- Efficient pattern matching with pre-compiled patterns
- Local KV cache (~25x faster than direct NATS KV)
- Optimized forEach element context creation

## Best Practices

### Rule Design
1. ✅ Use array operators to pre-filter messages before forEach
2. ✅ Apply forEach filters to limit iterations
3. ✅ Use `@msg` prefix explicitly in forEach templates
4. ✅ Configure appropriate `maxIterations` limit
5. ✅ Monitor forEach metrics for performance

### Performance
1. ✅ Use passthrough mode when possible (zero-copy)
2. ✅ Enable KV local cache for frequent lookups
3. ✅ Use wildcards efficiently (prefer exact matches when possible)
4. ✅ Monitor `message_processing_backlog` metric

### Security
1. ✅ Enable signature verification for privileged operations
2. ✅ Use `recent` operator to prevent replay attacks
3. ✅ Combine signature verification with KV-based authorization
4. ✅ Secure rule files with filesystem permissions

## Examples

See the [examples directory](../../examples/) for complete, working examples:
- **forEach/**: Batch notification processing with array operations
- Basic routing and filtering
- KV enrichment
- Time-based routing
- Signature verification

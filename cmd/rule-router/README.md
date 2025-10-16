# Rule Router

A high-performance NATS JetStream message router that evaluates JSON messages against configurable rules and publishes templated actions. Designed for edge deployment with intelligent local caching, real-time rule evaluation, and cryptographic message verification.

## Features

- **High Performance** - Microsecond rule evaluation, thousands of messages per second
- **NATS JetStream Native** - Pull consumers with durable subscriptions
- **Intelligent Stream Selection** - Automatically prefers memory streams and optimal subject filters
- **Cryptographic Security** - NKey signature verification and replay protection for secure workflows
- **Key-Value Store** - Dynamic lookups with JSON path traversal and local caching (~25x faster)
- **Time-Based Rules** - Schedule-aware evaluation without external schedulers
- **Pattern Matching** - NATS wildcards (`*` and `>`) with subject token access
- **Template Engine** - Variable substitution with nested field support
- **Action Headers** - Add, override, and template NATS headers in rule actions
- **Zero-Copy Passthrough** - Forward messages unchanged for high-performance filtering and routing
- **Full Authentication** - Username/password, token, NKey, and `.creds` files
- **Production Ready** - Prometheus metrics, structured logging, graceful shutdown
- **Auto-Retry** - Exponential backoff for action publishing

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
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘     │
│         └─────────────────┴──► Publish Actions            │
└───────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

- Go 1.23+
- NATS Server with JetStream enabled
- **JetStream streams must exist before starting rule-router**

### Understanding Stream Requirements

Rule-router requires JetStream streams for both:

1. **Consuming messages** - Streams that match rule subjects (input)
2. **Publishing actions** - Streams that match action subjects (output)

Before starting rule-router, ensure streams exist for both your rule subjects and action subjects. If a rule input subject cannot be mapped to a stream, rule-router will fail to start with a clear error message listing available streams.

### Installation

```bash
go install github.com/skeeeon/rule-router/cmd/rule-router@latest
```

### Setup

```bash
# Start NATS with JetStream
docker run -d --name nats-js -p 4222:4222 nats:latest -js

# Create streams for rule subjects (input) and action subjects (output)
# Rule subject: sensors.temperature
nats stream add SENSORS --subjects "sensors.>"

# Action subject: alerts.high-temperature
nats stream add ALERTS --subjects "alerts.>"

# Create a rule
cat > rules/temperature.yaml <<EOF
- subject: sensors.temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
  action:
    subject: alerts.high-temperature
    payload: |
      {
        "alert": "High temperature detected",
        "temperature": {temperature},
        "timestamp": "{@timestamp()}",
        "alert_id": "{@uuid7()}"
      }
EOF

# Run rule-router
rule-router -config config/config.yaml -rules rules/

# Test - publish to input stream
nats pub sensors.temperature '{"temperature": 35}'

# Verify - check output stream
nats sub alerts.high-temperature
```

## Configuration

```yaml
nats:
  urls: ["nats://localhost:4222"]
  
  consumers:
    subscriberCount: 8       # Workers per subscription
    fetchBatchSize: 64       # Messages per fetch
    fetchTimeout: 5s         # Fetch timeout (>1.5s for heartbeat)
    maxAckPending: 1000      # Unacked message limit
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

logging:
  level: info
  encoding: json

metrics:
  enabled: true
  address: :2112
```

## Rule Syntax

### Basic Structure

```yaml
- subject: input.subject          # Supports wildcards: *, >
  conditions:
    operator: and                 # and/or
    items:
      - field: fieldName
        operator: eq              # eq, neq, gt, lt, gte, lte, exists, contains, recent
        value: expectedValue
  action:
    subject: output.subject
    payload: "JSON template"      # Mutually exclusive with 'passthrough'
    headers:                      # Optional: Add/override NATS headers
      X-Custom-Header: "static value"
      X-Dynamic-Header: "{field_from_message}"
```

### Action Configuration

Actions can either **template** a new message or **passthrough** the original message payload without modification.

**Templated Action (default):**
```yaml
action:
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
  subject: output.subject
  passthrough: true  # Forwards original message payload
  headers:
    X-Processed-By: "rule-router"
```

**Important Rules:**
- You must specify either `payload` OR `passthrough: true`. They are mutually exclusive.
- Subject and header templating works with both modes.
- Passthrough preserves the exact original message bytes.
- When using passthrough, configured headers are merged with original headers (configured values take precedence).

### Condition Operators

**Comparison:**
- `eq`, `neq` - Equality/Inequality
- `gt`, `lt`, `gte`, `lte` - Numeric comparisons
- `exists` - Field presence check

**String/Array:**
- `contains` - String substring OR array membership
- `not_contains` - Inverse of contains

**Array Membership:**
- `in` - Field value is IN array of allowed values
- `not_in` - Field value is NOT IN array of forbidden values

**Time-Based:**
- `recent` - Checks if timestamp is within specified duration (for replay protection)

### System Fields

**Time Fields:**
```yaml
- field: "@time.hour"         # 0-23
- field: "@time.minute"       # 0-59
- field: "@day.name"          # monday, tuesday, etc.
- field: "@day.number"        # 1-7 (1=Monday)
- field: "@date.iso"          # YYYY-MM-DD
```

**Subject Fields** (for wildcard patterns):
```yaml
# Subject: sensors.temp-001.reading
- field: "@subject"           # Full: "sensors.temp-001.reading"
- field: "@subject.0"         # Token: "sensors"
- field: "@subject.1"         # Token: "temp-001"
- field: "@subject.last"      # Token: "reading"
- field: "@subject.count"     # Count: 3
```

**Header Fields:**
```yaml
- field: "@header.Nats-Msg-Id"
- field: "@header.X-Device-Firmware"
```

**Security Fields** (see Security Features section):
```yaml
- field: "@signature.valid"   # Boolean: signature verification result
- field: "@signature.pubkey"  # String: signer's public key
```

**KV Lookups** (with JSON path traversal):
```yaml
# Simple lookup
- field: "@kv.device_status.{device_id}:status"
  operator: eq
  value: "active"

# JSON path traversal
- field: "@kv.customer_data.{customer_id}:tier"
  operator: eq
  value: "premium"

# Keys with dots (using colon delimiter)
- field: "@kv.device_config.sensor.temp.001:threshold"
  operator: gt
  value: 30
```

**Template Functions:**
```yaml
{@timestamp()}   # Current ISO timestamp
{@uuid7()}       # Time-ordered UUID
{@uuid4()}       # Random UUID
```

## Security Features

Rule-router supports cryptographic message verification using NATS NKeys, enabling secure workflows such as physical access control, privileged command execution, and non-repudiable event logging.

### Overview

Three simple primitives provide powerful security capabilities:

1. **`@signature.valid`** - Boolean indicating if message signature is cryptographically valid
2. **`@signature.pubkey`** - Public key of the message signer
3. **`recent` operator** - Time-based condition to prevent replay attacks

These compose with existing features (KV lookups, time conditions, headers) to build sophisticated security logic.

### Message Format

Clients must include two headers when sending signed messages:

```yaml
Headers:
  Nats-Public-Key: "UDXU4RCRBVXEZ..."  # Signer's NKey public key
  Nats-Signature: "dGhpcyBpcyBhIHN..."  # Base64-encoded signature

Payload:
  {
    "timestamp": 1735689234,  # Unix seconds (required for replay protection)
    "action": "unlock",
    "user": "alice"
  }
```

**Important:** The signature is computed over the **payload only** (raw bytes). Subject and headers are not signed.

### Signature Verification

Enable in config:
```yaml
security:
  verification:
    enabled: true
    publicKeyHeader: "Nats-Public-Key"
    signatureHeader: "Nats-Signature"
```

Use in rules:
```yaml
conditions:
  - field: "@signature.valid"
    operator: eq
    value: true
  - field: "@signature.pubkey"
    operator: eq
    value: "UDXU4RCRBVXEZ..."  # Or use KV lookup for authorization
```

**Performance:** Verification (~50-100μs) only occurs when rules request `@signature.valid` (lazy evaluation).

### Replay Protection

The `recent` operator prevents replay attacks by checking if a timestamp is within a specified duration:

```yaml
conditions:
  - field: timestamp  # Unix seconds
    operator: recent
    value: "5s"       # Allow messages within last 5 seconds
```

**Clock Skew Handling:**
- Allows messages up to 5 seconds in the future (clock skew tolerance)
- Rejects messages beyond 5 seconds in the future
- Accepts messages within the specified past duration

**Supported Timestamp Formats:**
- **Unix seconds** (integer): `1735689234` (recommended)
- **Unix seconds** (float): `1735689234.123`
- **RFC3339 string**: `"2025-10-14T10:30:00Z"`

### Complete Example: Physical Access Control

This example demonstrates signature verification, replay protection, and KV-based authorization.

**KV Setup:**
```bash
nats kv add access_control
nats kv put access_control UDXU4RCRBVXEZ... \
  '{"active": true, "doors": ["front", "back"], "name": "alice"}'
```

**Rule:**
```yaml
- subject: cmds.door.unlock
  conditions:
    operator: and
    items:
      # 1. Verify cryptographic signature
      - field: "@signature.valid"
        operator: eq
        value: true
      
      # 2. Prevent replay attacks (5 second window)
      - field: timestamp
        operator: recent
        value: "5s"
      
      # 3. Check user is active in system
      - field: "@kv.access_control.{@signature.pubkey}:active"
        operator: eq
        value: true
      
      # 4. Verify user has access to requested door
      - field: "@kv.access_control.{@signature.pubkey}:doors"
        operator: contains
        value: "{door}"
  
  action:
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

**Client (Go):**
```go
import (
    "encoding/base64"
    "encoding/json"
    "time"
    "github.com/nats-io/nats.go"
    "github.com/nats-io/nkeys"
)

func publishSignedCommand(nc *nats.Conn, userKey nkeys.KeyPair, subject string, payload interface{}) error {
    payloadBytes, _ := json.Marshal(payload)
    signature, _ := userKey.Sign(payloadBytes)
    publicKey, _ := userKey.PublicKey()
    
    msg := nats.NewMsg(subject)
    msg.Data = payloadBytes
    msg.Header.Set("Nats-Public-Key", publicKey)
    msg.Header.Set("Nats-Signature", base64.StdEncoding.EncodeString(signature))
    
    return nc.PublishMsg(msg)
}

// Usage
userKey, _ := nkeys.FromSeed([]byte("SUABC..."))
payload := map[string]interface{}{
    "door":      "front",
    "timestamp": time.Now().Unix(),
}
publishSignedCommand(nc, userKey, "cmds.door.unlock", payload)
```

### Security Best Practices

**What is Protected:**
- ✅ Message Integrity - Payload cannot be modified
- ✅ Authentication - Cryptographically proves sender identity
- ✅ Replay Protection - `recent` operator prevents old message replay
- ✅ Authorization - Combine with KV lookups for access control

**What is NOT Protected:**
- ⚠️ Subject is not signed - Subject is routing metadata only
- ⚠️ Transport security - Use NATS TLS for encryption in transit
- ⚠️ Key management - Protect private keys (NKey seeds) appropriately

**Recommendations:**
1. Keep replay window tight (5s for high-security, 30s for normal operations)
2. Rotate keys regularly and update KV store
3. Log verification failures and monitor for attacks
4. Use TLS for defense in depth
5. Store NKey seeds securely (never commit to git)

## Key-Value Store Integration

### KV Field Syntax

**Format**: `@kv.bucket.key:json.path`

The colon (`:`) delimiter separates the key name from the JSON path, eliminating ambiguity since NATS KV allows dots in key names.

**Examples:**
```yaml
# Simple lookup
- field: "@kv.device_status.{device_id}:status"
  operator: eq
  value: "active"

# Nested JSON path
- field: "@kv.customer_data.{customer_id}:profile.tier"
  operator: eq
  value: "premium"

# Array access
- field: "@kv.config.{device_id}:thresholds.0.max"
  operator: gt
  value: 100

# Key with dots
- field: "@kv.device_config.sensor.temp.001:threshold"
  operator: gt
  value: 30
```

### Setup

```bash
# Create KV buckets
nats kv add device_status
nats kv add customer_data

# Add data
nats kv put device_status device-001 '{"status": "active", "battery": 85}'
nats kv put customer_data cust-123 '{"tier": "premium", "credits": 1500}'
```

### Configuration

```yaml
kv:
  enabled: true
  buckets:
    - "device_status"
    - "customer_data"
  localCache:
    enabled: true  # Recommended for production
```

### Local Cache Performance

- **Lookup Speed**: 50μs (NATS KV) → 2μs (local cache) = **~25x faster**
- **CPU Usage**: 10-15% reduction
- **Updates**: Real-time via KV watch streams
- **Memory**: ~1MB per 1000 entries

### JSON Path Traversal

Access nested JSON data in KV values using dot notation after the colon:

```yaml
# KV value: {"profile": {"tier": "premium"}, "billing": {"credits": 1500}}

# Access in conditions:
- field: "@kv.customer_data.{customer_id}:profile.tier"
  operator: eq
  value: "premium"

# Access in templates:
payload: |
  {
    "credits": "{@kv.customer_data.{customer_id}:billing.credits}"
  }
```

## Deployment

### Docker

```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o rule-router ./cmd/rule-router

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/rule-router /usr/local/bin/
COPY rules/ /etc/rule-router/rules/
COPY config/config.yaml /etc/rule-router/
CMD ["rule-router", "-config", "/etc/rule-router/config.yaml", "-rules", "/etc/rule-router/rules/"]
```

### Docker Compose

```yaml
version: '3.8'
services:
  nats:
    image: nats:latest
    command: "-js -m 8222"
    ports:
      - "4222:4222"
      - "8222:8222"

  rule-router:
    build: .
    depends_on:
      - nats
    environment:
      - NATS_URL=nats://nats:4222
    volumes:
      - ./config:/etc/rule-router
      - ./rules:/etc/rule-router/rules
    ports:
      - "2112:2112"
```

## Monitoring

### Prometheus Metrics

Available at `http://localhost:2112/metrics`:

**Message Processing:**
- `messages_total{status="received|processed|error"}`
- `message_processing_backlog`

**Rule Evaluation:**
- `rule_matches_total`
- `rules_active`

**Action Publishing:**
- `actions_total{status="success|error"}`
- `action_publish_failures_total`
- `actions_by_type_total{type="templated|passthrough"}`

**Security:**
- `signature_verifications_total{result="success|failure"}`
- `signature_verification_duration_seconds`

**NATS Connection:**
- `nats_connection_status`
- `nats_reconnects_total`

**System:**
- `process_goroutines`
- `process_memory_bytes`

### Health Checks

```bash
# Check metrics endpoint
curl http://localhost:2112/metrics

# Monitor signature verification failures (potential attacks)
watch 'curl -s http://localhost:2112/metrics | grep signature_verifications_total'
```

## Performance

### Stream Selection

Rule-router intelligently selects the optimal JetStream stream for each rule subject.

**Selection Criteria (Priority Order):**
1. **Subject Specificity** - More specific patterns score higher
2. **Storage Type** - Memory streams preferred (2x multiplier) over file
3. **Stream Type** - Primary streams slightly preferred over mirrors (0.95x penalty)
4. **System Streams** - KV/system streams de-prioritized (0.1x penalty)

**Benefits:**
- Rules automatically consume from optimal streams
- Memory streams provide ~5x lower latency
- Filtered mirrors improve consumer distribution

### Characteristics

- **Rule Evaluation**: Microseconds per message
- **Signature Verification**: ~50-100μs (only when accessed)
- **KV Cache Lookups**: ~2μs (cached), ~50μs (NATS KV fallback)
- **Message Throughput**: Thousands of messages per second per instance
- **Latency**: Sub-millisecond for co-located NATS

### Scaling Strategy

**Vertical Scaling** (single instance):
- Increase `subscriberCount` (workers)
- Increase `fetchBatchSize` for throughput
- Enable KV cache for performance
- Use memory streams for hot paths

**Horizontal Scaling** (multiple instances):
- Deploy multiple rule-router instances
- JetStream distributes messages across consumers
- Linear scalability

**Resource Requirements:**
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB base + ~1MB per 1000 KV entries
- **Network**: Low bandwidth

## Testing Rules

This project includes a standalone `rule-tester` utility for offline validation of rule logic.

**Key Features:**
- **Lint**: Check rule syntax
- **Scaffold**: Generate test directories
- **Batch Test**: Run full test suites
- **Mock Dependencies**: Simulate KV, headers, time, and signatures

**Example Usage:**
```bash
# Scaffold tests for a new rule
rule-tester --scaffold ./rules/my-new-rule.yaml

# Run all tests
rule-tester --test --rules ./rules
```

**Mocking Signatures:**
```json
// _test_config.json
{
  "subject": "cmds.door.unlock",
  "mockSignature": {
    "valid": true,
    "publicKey": "UDXU4RCRBVXEZ..."
  }
}
```

For complete testing documentation, see the project documentation.

## Troubleshooting

### Common Issues

**"No stream found for subject"**
```bash
# Create required streams
nats stream add STREAM_NAME --subjects "your.subject.>"
```

**"Consumer already exists with different config"**
```bash
# Delete and recreate consumer
nats consumer rm STREAM_NAME rule-router-your-subject
# Restart rule-router
```

**Signature verification always returns false**
- Check `Nats-Public-Key` and `Nats-Signature` headers are present
- Verify signature is Base64-encoded
- Ensure signing the raw payload bytes (not subject or headers)
- Confirm `security.verification.enabled: true` in config
- Enable debug logging to see verification details

**Recent operator always returns false**
- Ensure `timestamp` field exists in payload
- Use Unix seconds (integer) format
- Check clock skew (<5 seconds difference)
- Increase tolerance duration if needed (e.g., "30s")

**KV cache not updating**
```bash
# Verify KV watch streams
nats consumer ls '$KV_{bucket}'

# Check cache stats in logs
grep "KV cache initialized" /var/log/rule-router.log
```

## Examples

Complete working examples in the rules directory:

- `basic.yaml` - Simple conditions and actions
- `wildcard-examples.yaml` - Pattern matching
- `time-based.yaml` - Schedule-aware rules
- `kv-json-path.yaml` - KV enrichment with JSON paths
- `security-examples.yaml` - Signature verification and replay protection
- `nested-fields.yaml` - Deep object access
- `passthrough.yaml` - High-performance message forwarding
- `advanced.yaml` - Complex nested condition groups

## CLI Options

```bash
rule-router [options]

Options:
  -config string
        Path to config file (default "config/config.yaml")
  -rules string
        Path to rules directory (default "rules")
  -metrics-addr string
        Override metrics server address
  -metrics-path string
        Override metrics endpoint path
  -metrics-interval duration
        Override metrics collection interval
```

## License

MIT License - see license file for details.

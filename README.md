# Rule Router

A high-performance NATS JetStream message router that evaluates JSON messages against configurable rules and publishes templated actions. Designed for edge deployment with intelligent local caching and real-time rule evaluation.

## Features

- **High Performance** - Microsecond rule evaluation, thousands of messages per second
- **NATS JetStream Native** - Pull consumers with durable subscriptions
- **Intelligent Stream Selection** - Automatically prefers memory streams and optimal subject filters
- **Key-Value Store** - Dynamic lookups with JSON path traversal and local caching (~25x faster)
- **Time-Based Rules** - Schedule-aware evaluation without external schedulers
- **Pattern Matching** - NATS wildcards (`*` and `>`) with subject token access
- **Template Engine** - Variable substitution with nested field support
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
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘     │
│         └─────────────────┴─► Publish Actions             │
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

Before starting rule-router, ensure streams exist for both your rule subjects and action subjects. If a rule input subject cannot be mapped to a stream, rule-router will fail to start with a clear error message listing available streams. It will not fail to start if you do not have publish streams, but you will never receive an ACK for your publish causing a retry storm. If you do not care about publish ACK's, use the core publish mode instead.

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

# Run rule-router (will validate streams exist for both subjects)
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
        operator: eq              # eq, neq, gt, lt, gte, lte, exists, contains
        value: expectedValue
  action:
    subject: output.subject
    payload: "JSON template"
```
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

**KV Lookups** (with JSON path traversal using colon delimiter):
```yaml
# Simple lookup
- field: "@kv.device_status.{device_id}:status"
  operator: eq
  value: "active"

# JSON path traversal
- field: "@kv.customer_data.{customer_id}:tier"
  operator: eq
  value: "premium"

# Array access
- field: "@kv.config.{device_id}:thresholds.0.max"
  operator: gt
  value: 100

# Keys with dots (this is why we use the colon!)
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

### Complete Example

```yaml
- subject: sensors.*.temperature
  conditions:
    operator: and
    items:
      - field: value
        operator: gt
        value: 30
      - field: "@kv.device_config.{@subject.1}:enabled"
        operator: eq
        value: true
  action:
    subject: alerts.{@subject.1}.temperature
    payload: |
      {
        "device_id": "{@subject.1}",
        "temperature": {value},
        "threshold": "{@kv.device_config.{@subject.1}:thresholds.max}",
        "alert_id": "{@uuid7()}",
        "timestamp": "{@timestamp()}"
      }
```

## Key-Value Store Integration

### KV Field Syntax

**Format**: `@kv.bucket.key:json.path`

The colon (`:`) delimiter separates the key name from the JSON path. This eliminates ambiguity since NATS KV allows dots in key names.

**Examples**:
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

# Key with dots (this is why we need the colon!)
- field: "@kv.device_config.sensor.temp.001:threshold"
  operator: gt
  value: 30
```

### Setup

```bash
# Create KV buckets
nats kv add device_status
nats kv add customer_data

# Add data (keys can contain dots!)
nats kv put device_status device-001 '{"status": "active", "battery": 85}'
nats kv put device_status sensor.temp.001 '{"threshold": 35, "location": "room-A"}'
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
# KV bucket "customer_data" with key "cust123":
# {
#   "profile": {"tier": "premium", "name": "Acme Corp"},
#   "billing": {"credits": 1500},
#   "addresses": [
#     {"type": "primary", "city": "Seattle"},
#     {"type": "secondary", "city": "Portland"}
#   ]
# }

# Access in conditions:
- field: "@kv.customer_data.{customer_id}:profile.tier"
  operator: eq
  value: "premium"

# Access in templates:
payload: |
  {
    "customer_name": "{@kv.customer_data.{customer_id}:profile.name}",
    "credits": "{@kv.customer_data.{customer_id}:billing.credits}",
    "primary_city": "{@kv.customer_data.{customer_id}:addresses.0.city}"
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

# Verify message processing
curl -s http://localhost:2112/metrics | grep messages_total

# Monitor action failures
watch 'curl -s http://localhost:2112/metrics | grep action_publish_failures'
```

## Performance

### Stream Selection

Rule-router intelligently selects the optimal JetStream stream for each rule subject, supporting primary streams, mirrors, and sourced streams.

#### Selection Criteria (Priority Order)

1. **Subject Specificity** - More specific patterns score higher
2. **Storage Type** - Memory streams preferred (2x multiplier) over file
3. **Stream Type** - Primary streams slightly preferred over mirrors (0.95x penalty)
4. **System Streams** - KV/system streams de-prioritized (0.1x penalty)

**Benefits:**
- Rules automatically consume from optimal streams
- Memory streams provide ~5x lower latency
- Filtered mirrors improve consumer distribution
- File mirrors preserve history without impacting performance

### Characteristics

- **Rule Evaluation**: Microseconds per message
- **KV Cache Lookups**: ~2μs (cached), ~50μs (NATS KV fallback)
- **Message Throughput**: Thousands of messages per second per instance
- **Latency**: Sub-millisecond for co-located NATS
- **Stream Selection**: ~1-5μs per lookup

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

## Troubleshooting

### Common Issues

**"No stream found for subject"**
```bash
# Create required streams
nats stream add STREAM_NAME --subjects "your.subject.>"

# Or create a mirror with filter
nats stream add STREAM_MIRROR --mirror SOURCE --mirror-filter "your.subject.>"
```

**"Consumer already exists with different config"**
```bash
# Delete and recreate consumer
nats consumer rm STREAM_NAME rule-router-your-subject
# Restart rule-router
```

**High action publish failures**
```bash
# Check NATS connectivity
nats pub test.subject "test"

# Check metrics
curl http://localhost:2112/metrics | grep action_publish_failures_total
```

**KV cache not updating**
```bash
# Verify KV watch streams
nats consumer ls '$KV_{bucket}'

# Check cache stats in logs
grep "KV cache initialized" /var/log/rule-router.log
```

**"KV field must use ':' delimiter" errors**
```
# Old syntax (incorrect):
@kv.bucket.key.path

# New syntax (correct):
@kv.bucket.key:path
```

**Stream selection debugging**
```bash
# Enable debug logging
logging:
  level: debug

# Check which stream was selected
grep "selected optimal stream" rule-router.log | jq .
```

## Examples

Complete working examples in the [rules/](rules/) directory:
- `basic.yaml` - Simple conditions and actions
- `wildcard-examples.yaml` - Pattern matching
- `time-based.yaml` - Schedule-aware rules
- `kv-json-path.yaml` - KV enrichment with JSON paths
- `nested-fields.yaml` - Deep object access
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

MIT License - see [LICENSE](LICENSE) file for details.

# Rule Router

A high-performance NATS JetStream message router that evaluates JSON messages against configurable rules and publishes templated actions. Designed for edge deployment with intelligent local caching and real-time rule evaluation.

## Features

- 🚀 **High Performance** - Microsecond rule evaluation, thousands of messages per second per instance
- 🔗 **NATS JetStream Native** - Direct JetStream integration with pull consumers and durable subscriptions
- 🗄️ **Key-Value Store** - Dynamic lookups with JSON path traversal for enrichment
- ⚡ **Local KV Cache** - In-memory caching (~25x faster) with real-time stream updates
- ⏰ **Time-Based Rules** - Schedule-aware evaluation without external schedulers
- 🎯 **Pattern Matching** - NATS wildcards (`*` and `>`) with subject token access
- 📝 **Template Engine** - Variable substitution with nested field support
- 🔐 **Full Authentication** - Username/password, token, NKey, and `.creds` files
- 📊 **Production Ready** - Prometheus metrics, structured logging, graceful shutdown
- 🔄 **Auto-Retry** - Exponential backoff for action publishing with JetStream redelivery

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                     NATS JetStream                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────┐  │
│  │ Streams  │  │ Consumers│  │ KV Stores│  │ KV Changes│  │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └─────┬─────┘  │
└───────┼─────────────┼─────────────┼──────────────┼────────┘
        │             │             │              │
        │ Pull Fetch  │ Durable     │ Lookup       │ Subscribe
        ▼             ▼             ▼              ▼
┌───────────────────────────────────────────────────────────┐
│                      Rule Router                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │ Subscription │  │ Rule Engine  │  │ Local Cache  │     │
│  │ Manager      │──│ + Templates  │──│ (KV Mirror)  │     │
│  │ (workers)    │  │ + Conditions │  │ (Real-time)  │     │
│  └──────┬───────┘  └──────┬───────┘  └──────────────┘     │
│         │                  │                              │
│         └──────────────────┴─► Publish Actions            │
└───────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌──────────────────┐
                    │ Output Subjects  │
                    │ (NATS JetStream) │
                    └──────────────────┘
```

### How It Works

1. **Stream Discovery** - At startup, discovers all JetStream streams and validates rule subjects can be routed
2. **Consumer Creation** - Creates durable pull consumers for each rule subject (survives restarts)
3. **Worker Pool** - Launches configurable worker goroutines per subscription 
4. **Message Processing** - Workers fetch messages in batches, evaluate rules, publish actions
5. **KV Cache** - Loads all KV data into memory, subscribes to `$KV.{bucket}.>` for real-time updates
6. **Action Publishing** - Publishes to NATS with exponential backoff retry (3 attempts)

## Quick Start

### Prerequisites

- Go 1.21+
- NATS Server with JetStream enabled
- **JetStream streams must be created before starting rule-router**

### Installation

```bash
# Clone and build
git clone https://github.com/skeeeon/rule-router
cd rule-router
go build -o rule-router ./cmd/rule-router

# Or install directly
go install github.com/skeeeon/rule-router/cmd/rule-router@latest
```

### Setup JetStream Streams

**Critical**: Rule-router requires JetStream streams to exist before startup. Create streams that match your rule subjects:

```bash
# Start NATS with JetStream
docker run -d --name nats-js -p 4222:4222 nats:latest -js

# Create streams for your subjects
nats stream add SENSORS --subjects "sensors.>"
nats stream add ALERTS --subjects "alerts.>"
nats stream add EVENTS --subjects "events.*"

# Verify streams
nats stream list
```

### Create a Rule

Create `rules/temperature.yaml`:

```yaml
- subject: sensors.temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
      - field: location
        operator: exists
  action:
    subject: alerts.high-temperature
    payload: |
      {
        "alert": "High temperature detected",
        "temperature": {temperature},
        "location": {location},
        "timestamp": "{@timestamp()}",
        "alert_id": "{@uuid7()}"
      }
```

### Run the Router

```bash
# Minimal config
cat > config/config.yaml <<EOF
nats:
  urls: ["nats://localhost:4222"]

logging:
  level: info
  outputPath: stdout
  encoding: json

metrics:
  enabled: true
  address: :2112
EOF

# Start rule-router
./rule-router -config config/config.yaml -rules rules/

# Test it
nats pub sensors.temperature '{"temperature": 35, "location": "server-room"}'

# Check alerts stream
nats stream view ALERTS
```

## Configuration

### Complete Example

```yaml
nats:
  # Connection
  urls:
    - nats://server1:4222
    - nats://server2:4222
  credsFile: "/etc/nats/rule-router.creds"  # Recommended for production
  
  # TLS
  tls:
    enable: true
    certFile: "/etc/ssl/nats/client-cert.pem"
    keyFile: "/etc/ssl/nats/client-key.pem"
    caFile: "/etc/ssl/nats/ca.pem"
  
  # JetStream Consumers
  consumers:
    subscriberCount: 8           # Workers per subscription (2-4x CPU cores)
    fetchBatchSize: 1            # Messages per fetch (1=low latency, 10+=throughput)
    fetchTimeout: 5s             # Fetch wait time
    maxAckPending: 1000          # Unacked message limit
    ackWaitTimeout: 30s          # Redelivery delay
    maxDeliver: 3                # Max redelivery attempts
    deliverPolicy: all           # all, new, last
    replayPolicy: instant        # instant, original
  
  # Connection behavior
  connection:
    maxReconnects: -1            # -1 = unlimited
    reconnectWait: 50ms

kv:
  enabled: true
  buckets:
    - "device_status"
    - "customer_data"
  localCache:
    enabled: true                # ~25x faster KV lookups

logging:
  level: info
  outputPath: stdout
  encoding: json

metrics:
  enabled: true
  address: :2112
  path: /metrics
  updateInterval: 15s
```

### Performance Tuning

**High Throughput (>5000 msg/sec)**:
```yaml
consumers:
  subscriberCount: 16      # More workers
  fetchBatchSize: 512       # Batch fetching
  fetchTimeout: 256ms         # Aggressive fetching
  maxAckPending: 4096      # Larger buffer
```

**Low Latency (<10ms)**:
```yaml
consumers:
  subscriberCount: 4       # Moderate workers
  fetchBatchSize: 1        # Immediate processing
  fetchTimeout: 5s         # Conservative
  maxAckPending: 1000      # Standard buffer
```

**Memory Constrained**:
```yaml
consumers:
  subscriberCount: 2       # Minimal workers
  fetchBatchSize: 1        # Single message
  maxAckPending: 100       # Small buffer

kv:
  localCache:
    enabled: false         # Disable cache
```

### Authentication

Choose one method:

```yaml
# Username/Password
nats:
  username: "user"
  password: "pass"

# Token
nats:
  token: "your-token"

# NKey
nats:
  nkey: "your-nkey"

# .creds file (recommended)
nats:
  credsFile: "/etc/nats/app.creds"
```

## Rule Syntax

### Basic Structure

```yaml
- subject: input.subject          # NATS subject (supports wildcards)
  conditions:                     # Optional
    operator: and                 # and/or
    items:
      - field: fieldName
        operator: eq
        value: expectedValue
  action:
    subject: output.subject       # Can include variables
    payload: "template"           # JSON template
```

### Condition Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `eq` | Equal | `value: 25` |
| `neq` | Not equal | `value: "error"` |
| `gt` | Greater than | `value: 30` |
| `lt` | Less than | `value: 10` |
| `gte` | Greater or equal | `value: 25` |
| `lte` | Less or equal | `value: 50` |
| `exists` | Field exists | (no value) |
| `contains` | String contains | `value: "warning"` |

### System Fields

System fields provide access to time, subject metadata, and KV store values. All system fields are prefixed with `@`.

#### Time Fields

Access current time information for schedule-aware rules:

| Field | Type | Description | Example Value |
|-------|------|-------------|---------------|
| `@time.hour` | int | Hour of day (0-23) | `14` |
| `@time.minute` | int | Minute of hour (0-59) | `30` |
| `@day.name` | string | Day of week (lowercase) | `monday` |
| `@day.number` | int | Day of week (1-7, Sunday=7) | `1` |
| `@date.year` | int | Current year | `2024` |
| `@date.month` | int | Month (1-12) | `10` |
| `@date.day` | int | Day of month (1-31) | `15` |
| `@date.iso` | string | ISO date format | `2024-10-15` |
| `@timestamp.unix` | int | Unix timestamp (seconds) | `1697385600` |
| `@timestamp.iso` | string | ISO8601 timestamp | `2024-10-15T14:30:00Z` |

**Example - Business Hours Rule:**
```yaml
- subject: api.requests
  conditions:
    operator: and
    items:
      - field: "@time.hour"
        operator: gte
        value: 9              # After 9 AM
      - field: "@time.hour"
        operator: lt
        value: 17             # Before 5 PM
      - field: "@day.number"
        operator: lte
        value: 5              # Weekdays only (Mon-Fri)
  action:
    subject: processing.business-hours
    payload: |
      {
        "request_id": {request_id},
        "received_at": "{@timestamp.iso}",
        "day": "{@day.name}",
        "hour": "{@time.hour}"
      }
```

#### Subject Fields

Access tokens from the NATS subject for pattern-based routing:

| Field | Type | Description | Example (subject: `sensors.temp-001.reading`) |
|-------|------|-------------|-----------------------------------------------|
| `@subject` | string | Full subject string | `sensors.temp-001.reading` |
| `@subject.count` | int | Number of tokens | `3` |
| `@subject.first` | string | First token | `sensors` |
| `@subject.last` | string | Last token | `reading` |
| `@subject.0` | string | First token (indexed) | `sensors` |
| `@subject.1` | string | Second token | `temp-001` |
| `@subject.2` | string | Third token | `reading` |
| `@subject.N` | string | Nth token (0-based) | *(out of bounds = empty)* |

**Example - Wildcard Pattern with Token Access:**
```yaml
- subject: sensors.*.temperature
  conditions:
    operator: and
    items:
      - field: value
        operator: gt
        value: 30
      - field: "@subject.1"      # Device ID from subject
        operator: neq
        value: "test-device"
  action:
    subject: alerts.{@subject.1}.temperature
    payload: |
      {
        "device_id": "{@subject.1}",
        "sensor_type": "{@subject.2}",
        "full_subject": "{@subject}",
        "temperature": {value},
        "alert_id": "{@uuid7()}"
      }
```

#### KV Fields

Access NATS Key-Value stores with JSON path traversal:

| Format | Description | Example |
|--------|-------------|---------|
| `@kv.{bucket}.{key}` | Simple KV lookup | `@kv.device_status.sensor-001` |
| `@kv.{bucket}.{key}.{path}` | JSON path traversal | `@kv.customer_data.cust-123.tier` |
| `@kv.{bucket}.{key}.{path}.N` | Array index access | `@kv.config.app-1.servers.0.host` |
| `@kv.{bucket}.{var}.{path}` | Variable substitution | `@kv.device_config.{device_id}.max` |

**Supports:**
- **Variable substitution** in keys: `{device_id}`, `{customer_id}`, `{@subject.1}`
- **Nested JSON paths**: `profile.name`, `billing.credits`
- **Array indexing**: `addresses.0.city`, `readings.2.value`
- **Deep nesting**: `config.thresholds.sensors.0.limits.max`

**Example - KV Enrichment with JSON Paths:**
```yaml
# KV: customer_data["cust-123"] = {
#   "tier": "premium",
#   "profile": {"name": "Acme Corp"},
#   "billing": {"credits": 1500}
# }

- subject: orders.created
  conditions:
    operator: and
    items:
      - field: "@kv.customer_data.{customer_id}.tier"
        operator: eq
        value: "premium"
      - field: "@kv.customer_data.{customer_id}.billing.credits"
        operator: gt
        value: 1000
  action:
    subject: fulfillment.premium
    payload: |
      {
        "order_id": {order_id},
        "customer": {
          "name": "{@kv.customer_data.{customer_id}.profile.name}",
          "tier": "{@kv.customer_data.{customer_id}.tier}",
          "credits": "{@kv.customer_data.{customer_id}.billing.credits}"
        }
      }
```

**Local Cache Performance:**
- First lookup: ~50μs (NATS KV)
- Subsequent lookups: ~2μs (local cache)
- Real-time updates via `$KV.{bucket}.>` subscriptions
- Enable with `kv.localCache.enabled: true` (default)

### Template Functions

| Function | Description | Output |
|----------|-------------|--------|
| `{@timestamp()}` | Current ISO timestamp | `2024-01-15T14:30:00Z` |
| `{@uuid7()}` | Time-ordered UUID | `01234567-89ab-...` |
| `{@uuid4()}` | Random UUID | `a1b2c3d4-e5f6-...` |

### Complete Examples

**Wildcard Pattern with Token Access**:
```yaml
- subject: sensors.*.temperature
  conditions:
    operator: and
    items:
      - field: value
        operator: gt
        value: 30
      - field: "@subject.1"      # Device ID from subject
        operator: neq
        value: "test-device"
  action:
    subject: alerts.{@subject.1}.temperature
    payload: |
      {
        "device_id": "{@subject.1}",
        "sensor_type": "{@subject.2}",
        "temperature": {value},
        "alert_id": "{@uuid7()}"
      }
```

**KV Enrichment with JSON Paths**:
```yaml
- subject: orders.created
  conditions:
    operator: and
    items:
      - field: "@kv.customer_data.{customer_id}.tier"
        operator: eq
        value: "premium"
      - field: "@kv.customer_data.{customer_id}.billing.credits"
        operator: gt
        value: 1000
  action:
    subject: fulfillment.premium
    payload: |
      {
        "order_id": {order_id},
        "customer": {
          "id": {customer_id},
          "name": "{@kv.customer_data.{customer_id}.profile.name}",
          "tier": "{@kv.customer_data.{customer_id}.tier}",
          "credits": "{@kv.customer_data.{customer_id}.billing.credits}"
        },
        "priority": "high",
        "timestamp": "{@timestamp()}"
      }
```

**Time-Based Rules**:
```yaml
- subject: sensors.motion
  conditions:
    operator: and
    items:
      - field: motion_detected
        operator: eq
        value: true
      - field: "@time.hour"
        operator: gte
        value: 22              # After 10 PM
      - field: "@day.number"
        operator: lte
        value: 5               # Weekdays only
  action:
    subject: alerts.after-hours
    payload: |
      {
        "alert": "After-hours motion detected",
        "location": {location},
        "time": "{@time.hour}:{@time.minute}",
        "day": "{@day.name}"
      }
```

**Nested Field Access**:
```yaml
- subject: api.requests
  conditions:
    operator: and
    items:
      - field: response.status.code    # Nested condition
        operator: gte
        value: 500
      - field: request.endpoint
        operator: contains
        value: "/api/v1"
  action:
    subject: monitoring.errors
    payload: |
      {
        "error": "API error detected",
        "endpoint": {request.endpoint},
        "method": {request.method},
        "status_code": {response.status.code},
        "error_message": {response.body.error}
      }
```

## Key-Value Store Integration

### Setup KV Buckets

```bash
# Create KV buckets
nats kv add device_status
nats kv add customer_data

# Add data
nats kv put device_status device-001 '{"status": "active", "battery": 85}'
nats kv put customer_data cust-123 '{"tier": "premium", "credits": 1500}'

# Configure rule-router
```

```yaml
kv:
  enabled: true
  buckets:
    - "device_status"
    - "customer_data"
  localCache:
    enabled: true
```

### Local Cache Performance

The local KV cache provides dramatic performance improvements:

- **Lookup Speed**: 50μs (NATS KV) → 2μs (local cache) = **~25x faster**
- **CPU Usage**: 10-15% reduction
- **Cache Hit Rate**: >95% in normal operation
- **Updates**: Real-time via `$KV.{bucket}.>` subscriptions

**How it works**:
1. At startup: Loads all KV data into memory
2. During operation: Serves lookups from memory
3. In background: Subscribes to `$KV.{bucket}.>` for updates
4. On changes: Updates cache in real-time

**Memory usage**: ~1MB per 1000 KV entries

### JSON Path Traversal

Access nested JSON data in KV values:

```yaml
# KV bucket "customer_data" with key "cust123":
# {
#   "profile": {"tier": "premium", "name": "Acme Corp"},
#   "billing": {"credits": 1500},
#   "shipping": {
#     "addresses": [
#       {"type": "primary", "city": "Seattle"},
#       {"type": "secondary", "city": "Portland"}
#     ]
#   }
# }

# Access in rules:
conditions:
  items:
    - field: "@kv.customer_data.{customer_id}.profile.tier"
      operator: eq
      value: "premium"
    - field: "@kv.customer_data.{customer_id}.billing.credits"
      operator: gt
      value: 1000

action:
  payload: |
    {
      "customer_name": "{@kv.customer_data.{customer_id}.profile.name}",
      "credits": "{@kv.customer_data.{customer_id}.billing.credits}",
      "primary_city": "{@kv.customer_data.{customer_id}.shipping.addresses.0.city}"
    }
```

## Deployment

### Docker

```dockerfile
FROM golang:1.21-alpine AS builder
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

**Message Processing**:
- `messages_total{status="received|processed|error"}` - Message counts by status
- `message_queue_depth` - Current queue depth
- `message_processing_backlog` - Pending messages

**Rule Evaluation**:
- `rule_matches_total` - Successful rule matches
- `rules_active` - Number of loaded rules

**Action Publishing**:
- `actions_total{status="success|error"}` - Actions published by status
- `action_publish_failures_total` - Publish failures (before retry)

**NATS Connection**:
- `nats_connection_status` - Connection status (0/1)
- `nats_reconnects_total` - Reconnection attempts

**System**:
- `process_goroutines` - Active goroutines
- `process_memory_bytes` - Memory usage

### Health Checks

```bash
# Check metrics endpoint
curl http://localhost:2112/metrics

# Verify connection
curl -s http://localhost:2112/metrics | grep nats_connection_status

# Check message processing
curl -s http://localhost:2112/metrics | grep messages_total

# Monitor action failures
watch 'curl -s http://localhost:2112/metrics | grep action_publish_failures'
```

### Grafana Dashboard

Example queries:

```promql
# Message throughput (msg/sec)
rate(messages_total{status="processed"}[1m])

# Rule match rate
rate(rule_matches_total[1m])

# Action publish success rate
rate(actions_total{status="success"}[1m]) / rate(actions_total[1m])

# Publish failure rate
rate(action_publish_failures_total[1m])

# Memory usage
process_memory_bytes / 1024 / 1024
```

## Performance

### Characteristics

- **Rule Evaluation**: Microseconds per message (40-50k msg/sec per core in benchmarks)
- **KV Cache Lookups**: ~2μs (cached), ~50μs (NATS KV fallback)
- **Message Throughput**: Thousands of messages per second per instance
- **Latency**: Sub-millisecond for co-located NATS

### Scaling Strategy

**Vertical Scaling** (single instance):
- Increase `subscriberCount` (workers)
- Increase `fetchBatchSize` for throughput
- Enable KV cache for performance
- 2+ CPU cores recommended

**Horizontal Scaling** (multiple instances):
- Deploy multiple rule-router instances
- Each instance creates its own durable consumer
- JetStream distributes messages across consumers
- Linear scalability with instance count

**Resource Requirements**:
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB base + ~1MB per 1000 KV entries
- **Network**: Low bandwidth (compressed NATS protocol)

## Troubleshooting

### Common Issues

**"No stream found for subject"**:
```bash
# Create required streams
nats stream add STREAM_NAME --subjects "your.subject.>"
```

**"Consumer already exists with different config"**:
```bash
# Delete and recreate consumer
nats consumer rm STREAM_NAME rule-router-your-subject
# Restart rule-router
```

**High action publish failures**:
```bash
# Check NATS connectivity
nats pub test.subject "test"

# Check metrics
curl http://localhost:2112/metrics | grep action_publish_failures_total

# Check logs for retry messages
journalctl -u rule-router | grep "action publish failed"
```

**KV cache not updating**:
```bash
# Verify KV change subscriptions
nats consumer ls '$KV_{bucket}'

# Check cache stats in logs
# Look for "KV cache initialized successfully"
```

### Debug Mode

```yaml
logging:
  level: debug    # Enable detailed logging
```

```bash
# Watch debug logs
./rule-router -config config.yaml -rules rules/ 2>&1 | grep DEBUG
```

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

## Examples

Complete working examples in the [rules/](rules/) directory:
- [basic.yaml](rules/basic.yaml) - Simple condition and action
- [wildcard-examples.yaml](rules/wildcard-examples.yaml) - Pattern matching
- [time-based.yaml](rules/time-based.yaml) - Schedule-aware rules
- [kv-json-path.yaml](rules/kv-json-path.yaml) - KV enrichment with JSON paths
- [nested-fields.yaml](rules/nested-fields.yaml) - Deep object access

## License

MIT License - see [LICENSE](LICENSE) file for details.

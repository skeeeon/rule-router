# Rule Router

A high-performance NATS message router that evaluates JSON messages against configurable rules and publishes templated actions. Designed for edge deployment with NATS JetStream and intelligent local caching.

## Features

- 🚀 **High Performance** - 3,000-6,000 messages/second per instance
- 🔗 **NATS JetStream** - Native integration with streams and consumers  
- 🗄️ **Key-Value Store** - Dynamic lookups with JSON path traversal
- ⚡ **Local KV Cache** - In-memory caching with real-time stream updates
- ⏰ **Time-Based Rules** - Schedule-aware evaluation without cron
- 🎯 **Pattern Matching** - NATS wildcards (`*` and `>`) with subject token access
- 📝 **Template Engine** - Variable substitution with nested field support
- 🔐 **Full Authentication** - Username/password, token, NKey, and `.creds` files
- 📊 **Production Ready** - Prometheus metrics, structured logging, graceful shutdown

## Quick Start

### Prerequisites
- Go 1.21+
- NATS Server with JetStream enabled

### Installation

```bash
# Clone and build
git clone https://github.com/yourusername/rule-router
cd rule-router
go build -o rule-router ./cmd/rule-router

# Or install directly
go install github.com/yourusername/rule-router/cmd/rule-router@latest
```

### Basic Usage

1. **Start NATS JetStream**
```bash
docker run -d --name nats-js -p 4222:4222 nats:latest -js
```

2. **Create a simple rule** (`rules/temperature.yaml`)
```yaml
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
        "value": {temperature},
        "location": {location},
        "timestamp": "{@timestamp()}"
      }
```

3. **Run the router**
```bash
./rule-router -config config/config.yaml -rules rules/
```

4. **Test with a message**
```bash
nats pub sensors.temperature '{"temperature": 32, "location": "server-room"}'
# Router publishes to alerts.high-temperature
```

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   NATS          │◄──►│  Watermill       │───▶│  Rule Engine    │
│   JetStream     │    │  Router          │    │  + Time Context │
│   + Key-Value   │    │  + Middleware    │    │  + Local Cache  │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │
         ▼                       ▼
┌─────────────────┐    ┌──────────────────┐
│   Local KV      │    │   Actions        │
│   Cache         │    │   Published      │
│   (In-Memory)   │    │   to NATS        │
└─────────────────┘    └──────────────────┘
```

### Edge Deployment Architecture

Designed for co-location with NATS on edge nodes:
- **Local Processing** - Microsecond latency with in-memory KV cache
- **Real-Time Updates** - KV stream subscriptions for cache consistency
- **Resilient** - Continues operating during WAN outages
- **Stateless** - All state in NATS KV stores with local caching
- **Multi-Tenant** - Account isolation via `.creds` files

## Configuration

### Minimal Configuration

```yaml
nats:
  urls: ["nats://localhost:4222"]
  
kv:
  enabled: true
  buckets: ["device_status", "config"]
  localCache:
    enabled: true    # High-performance in-memory caching
  
logging:
  level: info
  outputPath: stdout
```

### Production Configuration

```yaml
nats:
  urls: ["nats://server1:4222", "nats://server2:4222"]
  credsFile: "/etc/nats/rule-router.creds"
  
kv:
  enabled: true
  buckets: ["device_status", "config", "user_data"]
  localCache:
    enabled: true    # Default: true when KV enabled

watermill:
  nats:
    subscriberCount: 8
    maxPendingAsync: 2000
    
metrics:
  enabled: true
  address: :2112
```

See [config/config.yaml](config/config.yaml) for complete example with all options.

## Rule Syntax

### Basic Structure

```yaml
- subject: input.subject          # NATS subject to subscribe
  conditions:                     # Optional conditions
    operator: and                 # Logical operator: and/or
    items:
      - field: fieldName          # Message field
        operator: eq              # Comparison operator
        value: expectedValue      # Expected value
  action:
    subject: output.subject       # NATS subject to publish
    payload: "template"           # Message template
```

### Supported Operators

**Comparison Operators**

| Operator | Description | Example |
|----------|-------------|---------|
| `eq` | Equal | `value: 25` |
| `neq` | Not equal | `value: "error"` |
| `gt`, `lt`, `gte`, `lte` | Numeric comparison | `value: 30` |
| `exists` | Field exists | (no value needed) |
| `contains` | String contains | `value: "warning"` |

**System Fields for Conditions**

| Field | Type | Description | Values/Range |
|-------|------|-------------|--------------|
| `@time.hour` | int | Current hour | 0-23 |
| `@time.minute` | int | Current minute | 0-59 |
| `@day.name` | string | Day name | monday, tuesday, etc. |
| `@day.number` | int | Day number | 1-7 (Mon=1, Sun=7) |
| `@date.year` | int | Current year | e.g., 2024 |
| `@date.month` | int | Current month | 1-12 |
| `@date.day` | int | Day of month | 1-31 |
| `@date.iso` | string | ISO date | YYYY-MM-DD |
| `@timestamp.unix` | int | Unix timestamp | e.g., 1705344000 |
| `@timestamp.iso` | string | ISO timestamp | RFC3339 format |
| `@subject` | string | Full subject | e.g., "sensors.temp.room1" |
| `@subject.count` | int | Token count | Number of dot-separated parts |
| `@subject.N` | string | Token at index N | e.g., `@subject.0`, `@subject.1` |
| `@subject.first` | string | First token | Same as `@subject.0` |
| `@subject.last` | string | Last token | Final subject token |

### Advanced Features

**Wildcard Patterns**
```yaml
- subject: sensors.*              # Matches sensors.temperature, sensors.humidity
- subject: building.>             # Matches building.floor1.room2.temperature
```

**Time-Based Conditions**
```yaml
conditions:
  items:
    - field: "@time.hour"         # Current hour (0-23)
      operator: gte
      value: 9
    - field: "@day.name"          # Day name (monday, tuesday...)
      operator: eq
      value: "monday"
```

**Key-Value Lookups with Local Cache**
```yaml
conditions:
  items:
    - field: "@kv.device_status.{device_id}"  # Fast cached lookup
      operator: eq
      value: "active"

action:
  payload: |
    {
      "device_status": "{@kv.device_status.{device_id}}",
      "config": "{@kv.config.{device_id}.settings.threshold}"
    }
```

**Nested Field Access**
```yaml
conditions:
  items:
    - field: user.profile.settings.notifications  # Nested JSON field
      operator: eq
      value: true

action:
  payload: |
    {
      "user_name": {user.profile.name},          # Nested template variable
      "preference": {user.profile.settings.theme}
    }
```

**Subject Token Access**
```yaml
# For subject: devices.sensor123.temperature
action:
  subject: monitoring.{@subject.1}               # monitoring.sensor123
  payload: |
    {
      "device_type": "{@subject.0}",            # devices
      "device_id": "{@subject.1}",              # sensor123
      "metric": "{@subject.2}"                  # temperature
    }
```

### Template Functions

| Function | Description | Output Example |
|----------|-------------|----------------|
| `{@timestamp()}` | ISO timestamp | `2024-01-15T14:30:00Z` |
| `{@uuid7()}` | Time-ordered UUID | `01234567-89ab-...` |
| `{@uuid4()}` | Random UUID | `a1b2c3d4-e5f6-...` |

## Key-Value Store Integration

### Local Cache Performance

The local KV cache provides dramatic performance improvements:

- **Lookup Latency**: ~50μs → ~2μs (25x faster)
- **CPU Reduction**: 10-15 percentage points
- **Cache Hit Rate**: >95% in normal operation
- **Real-Time Updates**: Via NATS KV stream subscriptions

### JSON Path Traversal

Access nested JSON data in KV values:

```yaml
# KV bucket "customer_data" with key "cust123":
# {
#   "profile": {"tier": "premium", "name": "Acme Corp"},
#   "billing": {"credits": 1500}
# }

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
      "customer": {
        "name": "{@kv.customer_data.{customer_id}.profile.name}",
        "tier": "{@kv.customer_data.{customer_id}.profile.tier}",
        "credits": "{@kv.customer_data.{customer_id}.billing.credits}"
      }
    }
```

### Cache Behavior

- **Startup**: All configured KV buckets loaded into memory
- **Updates**: Real-time via NATS KV change streams (`$KV.{bucket}.>`)
- **Fallback**: Automatic fallback to NATS KV on cache miss
- **Durability**: Durable stream subscriptions survive restarts

## Examples

Find complete working examples in the [rules/](rules/) directory:
- [basic.yaml](rules/basic.yaml) - Simple temperature alerts
- [wildcards.yaml](rules/wildcard-examples.yaml) - Pattern matching examples
- [time-based.yaml](rules/time-based.yaml) - Scheduled and time-aware rules
- [kv-advanced.yaml](rules/kv-json-path.yaml) - Complex KV lookups with JSON paths
- [nested-fields.yaml](rules/nested-fields.yaml) - Deep object field access

## Performance

### Throughput
- **Single Instance**: 3,000-6,000 messages/second
- **With KV Cache**: faster KV lookups vs direct NATS KV
- **Scaling**: Linear with NATS cluster size
- **Bottlenecks**: Pattern matching (20%), field access (15%), JSON processing (12%)

### Latency
- **Co-located NATS**: Sub-millisecond rule evaluation
- **Remote NATS**: 1-5ms depending on network latency
- **KV Lookups**: <2μs (cached), 20-50μs (cache miss + NATS KV)

### Resource Requirements
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB base + ~1MB per 1000 KV entries in cache
- **Storage**: Minimal (stateless design)

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
CMD ["rule-router", "-config", "/etc/rule-router/config.yaml", "-rules", "/etc/rule-router/rules/"]
```

### Docker Compose with KV Cache

```yaml
version: '3.8'
services:
  rule-router:
    build: .
    network_mode: host  # Best NATS performance
    deploy:
      resources:
        limits:
          cpus: '2.0'
          memory: 1G
    environment:
      - KV_LOCAL_CACHE_ENABLED=true
    volumes:
      - ./config:/etc/rule-router
      - ./rules:/etc/rule-router/rules
```

## Monitoring

### Prometheus Metrics

Available at `http://localhost:2112/metrics`:

- `messages_total{status}` - Messages processed by status
- `rule_matches_total` - Successful rule evaluations
- `actions_total{status}` - Actions published
- `nats_connection_status` - Connection health (0/1)
- `rules_active` - Number of loaded rules
- KV cache metrics via `/metrics` endpoint

### Health Checks

```bash
# Check metrics endpoint
curl http://localhost:2112/metrics | grep messages_total

# Verify NATS connection and KV cache
curl -s http://localhost:2112/metrics | grep -E "(nats_connection_status|kv_cache)"
```

### KV Cache Monitoring

Monitor cache performance through metrics:
- Cache hit rate (should be >95%)
- Cache population status
- Real-time update processing

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
  -metrics-interval duration
        Override metrics collection interval
```

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with:
- [Watermill](https://watermill.io) - Event-driven messaging framework
- [NATS](https://nats.io) - High-performance messaging system
- [Prometheus](https://prometheus.io) - Metrics and monitoring

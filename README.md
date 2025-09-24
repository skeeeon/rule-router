# Rule Router

A high-performance NATS message router that evaluates JSON messages against configurable rules and publishes templated actions. Designed for edge deployment with NATS JetStream.

## Features

- 🚀 **High Performance** - 3,000-6,000 messages/second per instance
- 🔗 **NATS JetStream** - Native integration with streams and consumers  
- 🗄️ **Key-Value Store** - Dynamic lookups with JSON path traversal
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
│   + Key-Value   │    │  + Middleware    │    │  + KV Lookups   │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌──────────────────┐
                       │   Actions        │
                       │   Published      │
                       │   to NATS        │
                       └──────────────────┘
```

### Edge Deployment Architecture

Designed for co-location with NATS on edge nodes:
- **Local Processing** - Microsecond latency with in-process KV access
- **Resilient** - Continues operating during WAN outages
- **Stateless** - All state in NATS KV stores
- **Multi-Tenant** - Account isolation via `.creds` files

## Configuration

### Minimal Configuration

```yaml
nats:
  urls: ["nats://localhost:4222"]
  
kv:
  enabled: true
  buckets: ["device_status", "config"]
  
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

| Operator | Description | Example |
|----------|-------------|---------|
| `eq` | Equal | `value: 25` |
| `neq` | Not equal | `value: "error"` |
| `gt`, `lt`, `gte`, `lte` | Numeric comparison | `value: 30` |
| `exists` | Field exists | (no value needed) |
| `contains` | String contains | `value: "warning"` |

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

**Key-Value Lookups**
```yaml
conditions:
  items:
    - field: "@kv.device_status.{device_id}"  # KV lookup with variable
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
- **Scaling**: Linear with NATS cluster size
- **Bottlenecks**: KV operations (20-25%), field access (25-30%), template processing (12-15%)

### Latency
- **Co-located NATS**: Sub-millisecond rule evaluation
- **Remote NATS**: 1-5ms depending on network latency
- **KV Lookups**: <1ms co-located, 1-5ms remote

### Resource Requirements
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB depending on rule complexity
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

## Monitoring

### Prometheus Metrics

Available at `http://localhost:2112/metrics`:

- `messages_total{status}` - Messages processed by status
- `rule_matches_total` - Successful rule evaluations
- `actions_total{status}` - Actions published
- `nats_connection_status` - Connection health (0/1)
- `rules_active` - Number of loaded rules

### Health Checks

```bash
# Check metrics endpoint
curl http://localhost:2112/metrics | grep messages_total

# Verify NATS connection
curl -s http://localhost:2112/metrics | grep nats_connection_status
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

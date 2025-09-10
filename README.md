# Rule Router

A high-performance NATS message router built on [Watermill.io](https://watermill.io) that processes messages through sophisticated rule conditions with time-based evaluation and publishes templated actions back to NATS.

## Features

- 🚀 **High-Performance Processing**: Built on Watermill for 2,000-4,000 messages/second capability
- 🔗 **NATS JetStream Integration**: Connect to existing NATS JetStream infrastructure
- 🔐 **Comprehensive Authentication**: Support for various NATS authentication methods and TLS
- ⏰ **Time-Based Rule Evaluation**: Rules can evaluate based on current time, day of week, date
- 📝 **Sophisticated Rule Engine**: Complex condition evaluation with AND/OR logic and nested groups
- 🗂️ **Nested Field Support**: Deep object field access in both conditions and templates
- 📋 **Flexible Configuration**: YAML and JSON rule files with recursive directory loading
- 📊 **Production Monitoring**: Comprehensive Prometheus metrics and structured logging
- 🛡️ **Production Middleware**: Retry, circuit breaker, correlation ID, recovery, poison queue handling

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   NATS          │◄──►│  Watermill       │───▶│  Rule Engine    │
│   JetStream     │    │  Handlers        │    │  + Time-Based   │
│                 │    │  + Middleware    │    │  Evaluation     │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌──────────────────┐
                       │   Actions        │
                       │   Published      │
                       │   to NATS        │
                       └──────────────────┘
```

## Quick Start

### Prerequisites
- NATS JetStream server
- Go 1.21 or higher

### Installation

1. **Clone and Build**:
```bash
git clone https://github.com/skeeeon/rule-router
cd rule-router
go build -o rule-router ./cmd/rule-router
```

2. **Start NATS JetStream**:
```bash
# NATS JetStream
docker run -d --name nats-js -p 4222:4222 nats:latest -js
```

3. **Configure**:
```bash
# Copy sample configuration
cp config/config.yaml config/my-config.yaml
# Edit with your NATS server details
```

4. **Run**:
```bash
./rule-router -config config/my-config.yaml -rules rules/
```

## Configuration

### NATS JetStream Configuration

```yaml
# NATS Server Connection Configuration
nats:
  urls:
    - nats://server1:4222
    - nats://server2:4222
  
  # Authentication (choose one)
  username: "service-user"                 # Basic auth username
  password: "secure-password"              # Basic auth password
  token: "auth-token"                      # Token authentication
  nkey: "nkey-string"                      # NKey authentication
  credsFile: "/path/to/service.creds"      # JWT credentials file
  
  # TLS Configuration
  tls:
    enable: true                           # Enable TLS
    certFile: "/path/to/client.pem"        # Client certificate
    keyFile: "/path/to/client-key.pem"     # Client private key
    caFile: "/path/to/ca.pem"              # CA certificate
    insecure: false                        # Skip certificate verification
```

### Watermill Configuration

```yaml
watermill:
  # NATS JetStream Settings
  nats:
    maxReconnects: -1                      # Reconnection attempts (-1 = unlimited)
    reconnectWait: 50ms                    # Wait between reconnection attempts
    publishAsync: true                     # Enable async publishing (recommended)
    maxPendingAsync: 2000                  # Max pending async messages
    subscriberCount: 8                     # Number of parallel consumers
    ackWaitTimeout: 30s                    # Message acknowledgment timeout
    maxDeliver: 3                          # Max redelivery attempts
    writeBufferSize: 2097152               # Write buffer size (2MB)
    reconnectBufSize: 16777216             # Reconnection buffer size (16MB)
  
  # Router Settings
  router:
    closeTimeout: 30s                      # Graceful shutdown timeout
  
  # Middleware Settings
  middleware:
    retryMaxAttempts: 3                    # Max retry attempts for failed messages
    retryInterval: 100ms                   # Initial retry interval
    metricsEnabled: true                   # Enable metrics collection
    tracingEnabled: false                  # Enable distributed tracing
```

### Application Configuration

```yaml
# Logging
logging:
  level: info                              # Log level: debug, info, warn, error
  outputPath: stdout                       # Output: stdout or file path
  encoding: json                           # Format: json or console

# Metrics
metrics:
  enabled: true                            # Enable Prometheus metrics
  address: :2112                           # Metrics server address
  path: /metrics                           # Metrics endpoint path
  updateInterval: 15s                      # Metrics update frequency
```

## Rule Syntax

### Basic Rule Structure

```yaml
- topic: "input.subject"                   # NATS subject to subscribe to
  conditions:                              # Optional conditions
    operator: and                          # Logical operator: and, or
    items:                                 # Individual conditions
      - field: fieldName                   # Message field name
        operator: eq                       # Comparison operator
        value: expectedValue               # Expected value
    groups:                                # Nested condition groups
      - operator: or
        items: [...]
  action:                                  # Action to execute
    topic: "output.subject"                # NATS subject to publish to
    payload: "message template"            # Message template
```

### Evaluation Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `eq` | Equal to | `value: 25` |
| `neq` | Not equal to | `value: "error"` |
| `gt` | Greater than | `value: 30` |
| `lt` | Less than | `value: 10` |
| `gte` | Greater than or equal | `value: 25` |
| `lte` | Less than or equal | `value: 100` |
| `exists` | Field exists | (no value needed) |
| `contains` | String contains | `value: "warning"` |

### Nested Field Access

Access deep object fields using dot notation:

```yaml
# Message: {"user": {"profile": {"name": "John", "settings": {"theme": "dark"}}}}
conditions:
  operator: and
  items:
    - field: user.profile.name             # ✅ Nested field access
      operator: eq
      value: "John"
    - field: user.profile.settings.theme   # ✅ Deep nested access
      operator: eq
      value: "dark"
action:
  topic: user.preferences
  payload: |
    {
      "user": {user.profile.name},         # ✅ Nested in templates too
      "theme": {user.profile.settings.theme}
    }
```

### Time-Based Evaluation

Access current time information in rule conditions using `@` system fields:

| Field | Description | Type | Example Values |
|-------|-------------|------|----------------|
| `@time.hour` | Current hour (24h) | integer | 0-23 |
| `@time.minute` | Current minute | integer | 0-59 |
| `@day.name` | Day name (lowercase) | string | "monday", "friday" |
| `@day.number` | Day number (Mon=1) | integer | 1-7 |
| `@date.year` | Current year | integer | 2024 |
| `@date.month` | Current month | integer | 1-12 |
| `@date.day` | Day of month | integer | 1-31 |
| `@date.iso` | ISO date | string | "2024-01-15" |
| `@timestamp.unix` | Unix timestamp | integer | 1705344000 |
| `@timestamp.iso` | ISO timestamp | string | "2024-01-15T14:30:00Z" |

### Template Functions

Use these functions in action templates:

| Function | Description | Example Output |
|----------|-------------|----------------|
| `@{uuid4()}` | Random UUID v4 | `a1b2c3d4-e5f6-...` |
| `@{uuid7()}` | Time-ordered UUID v7 | `01234567-89ab-...` |
| `@{timestamp()}` | Current ISO timestamp | `2024-01-15T14:30:00Z` |

### Template Variables

Access message fields and time data in templates:

```yaml
payload: |
  {
    "messageField": {fieldName},           # Message data
    "nestedField": {user.profile.name},    # Nested message data
    "currentHour": "{@time.hour}",         # Time field
    "generatedId": "@{uuid7()}",           # Function
    "timestamp": "@{timestamp()}"          # Function
  }
```

## Complete Rule Examples

### Basic Temperature Alert
```yaml
- topic: sensors.temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
  action:
    topic: alerts.temperature
    payload: |
      {
        "alert": "High temperature detected!",
        "temperature": {temperature},
        "location": {location},
        "detectedAt": "@{timestamp()}",
        "alertId": "@{uuid7()}"
      }
```

### Business Hours Alert with Time Conditions
```yaml
- topic: sensors.temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
      - field: "@time.hour"                # Only during business hours
        operator: gte
        value: 9
      - field: "@time.hour"
        operator: lt
        value: 17
      - field: "@day.number"               # Monday through Friday
        operator: lte
        value: 5
  action:
    topic: alerts.business-hours
    payload: |
      {
        "alert": "High temperature during business hours",
        "temperature": {temperature},
        "location": {location},
        "detectedAt": "{@timestamp.iso}",
        "businessDay": "{@day.name}",
        "currentTime": "{@time.hour}:{@time.minute}",
        "alertId": "@{uuid7()}"
      }
```

### Complex Nested Conditions with Deep Field Access
```yaml
# Message: {"user": {"profile": {"tier": "premium"}}, "order": {"value": 1250, "items": {"count": 3}}}
- topic: events.orders
  conditions:
    operator: and
    items:
      - field: user.profile.tier             # ✅ Nested condition
        operator: eq
        value: "premium"
      - field: order.value                   # ✅ Nested numeric condition
        operator: gte
        value: 1000
      - field: order.items.count             # ✅ Deep nested condition
        operator: gt
        value: 2
  action:
    topic: processing.premium-orders
    payload: |
      {
        "message": "Premium customer large order",
        "customer_tier": {user.profile.tier},    # ✅ Nested template
        "order_value": {order.value},            # ✅ Nested template
        "item_count": {order.items.count},       # ✅ Deep nested template
        "processing": {
          "priority": "high",
          "assigned_at": "{@timestamp()}",
          "processor_id": "{@uuid7()}"
        }
      }
```

## Monitoring

### Prometheus Metrics
- `messages_total{status}` - Messages processed by status
- `rule_matches_total` - Rule evaluation statistics
- `actions_total{status}` - Actions executed by result
- `template_operations_total{status}` - Template processing stats
- `nats_connection_status` - NATS connection health
- `process_memory_bytes` - Memory usage

### Health Checks
```bash
# Metrics endpoint
curl http://localhost:2112/metrics

# Key indicators
curl -s http://localhost:2112/metrics | grep messages_total
```

## Command Line Options

```bash
./rule-router [options]

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

## Performance Characteristics

### Throughput
- **Expected**: 2,000-4,000 messages/second per instance
- **Bottlenecks**: JSON processing (60-70%), template processing (15-20%), I/O (10-15%)
- **Scaling**: Linear with NATS cluster size

### Resource Requirements
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB depending on rule complexity
- **Storage**: Minimal (stateless design)

## Production Deployment

### High Availability Setup
```yaml
# NATS Cluster Configuration
nats:
  urls:
    - nats://nats-1:4222
    - nats://nats-2:4222
    - nats://nats-3:4222
  
watermill:
  nats:
    publishAsync: true
    maxPendingAsync: 2000
    subscriberCount: 16                    # Scale with load
```

### Docker Deployment
```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o rule-router ./cmd/rule-router

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/rule-router .
COPY config/ ./config/
COPY rules/ ./rules/
CMD ["./rule-router", "-config", "config/config.yaml", "-rules", "rules/"]
```

## Known Limitations

- **Exact Topic Matching**: Cannot subscribe to wildcard patterns like `sensors.*` (coming in future update)
- **JSON Only**: Message payloads must be valid JSON
- **Startup Rule Loading**: Rules only loaded at application start (hot reloading planned)
- **Single Instance**: No built-in clustering support

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) file for details.

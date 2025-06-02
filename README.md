# Rule Router

A high-performance message router built on [Watermill.io](https://watermill.io) that connects to external NATS JetStream or MQTT brokers, processes messages through sophisticated rule conditions with time-based evaluation, and publishes actions back to the brokers.

## Features

- 🚀 **High-Performance Processing**: Built on Watermill for 2,000-4,000 messages/second capability
- 🔗 **External Broker Integration**: Connect to existing NATS JetStream or MQTT infrastructure
- 🔐 **Comprehensive Authentication**: Support for various authentication methods and TLS
- ⏰ **Time-Based Rule Evaluation**: Rules can evaluate based on current time, day of week, date
- 📝 **Sophisticated Rule Engine**: Complex condition evaluation with AND/OR logic and nested groups
- 📋 **Flexible Configuration**: YAML and JSON rule files with recursive directory loading
- 📊 **Production Monitoring**: Comprehensive Prometheus metrics and structured logging
- 🛡️ **Production Middleware**: Retry, circuit breaker, correlation ID, recovery, poison queue handling

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   External      │◄──►│  Watermill       │───▶│  Rule Engine    │
│   NATS/MQTT     │    │  Router +        │    │  + Time-Based   │
│   Brokers       │    │  Middleware      │    │  Evaluation     │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌──────────────────┐
                       │   Actions        │
                       │   Published      │
                       │   to Brokers     │
                       └──────────────────┘
```

## Quick Start

### Prerequisites
- External NATS JetStream server or MQTT broker
- Go 1.21 or higher

### Installation

1. **Clone and Build**:
```bash
git clone https://github.com/skeeeon/rule-router
cd rule-router
go build -o rule-router ./cmd/rule-router
```

2. **Start External Broker**:
```bash
# NATS JetStream (Recommended)
docker run -d --name nats-js -p 4222:4222 nats:latest -js

# OR MQTT Broker
docker run -d --name mosquitto -p 1883:1883 eclipse-mosquitto:latest
```

3. **Configure**:
```bash
# Copy sample configuration
cp config/watermill-nats.yaml config/config.yaml
# Edit with your broker details
```

4. **Run**:
```bash
./rule-router -config config/config.yaml -rules rules/
```

## Configuration

### Broker Configuration

**NATS JetStream (Recommended)**:
```yaml
brokerType: nats
nats:
  urls:                                    # NATS server URLs
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

**MQTT**:
```yaml
brokerType: mqtt
mqtt:
  broker: "ssl://broker:8883"              # MQTT broker URL
  clientId: "rule-router"                  # Unique client identifier
  username: "mqtt-user"                    # MQTT username
  password: "mqtt-password"                # MQTT password
  qos: 0                                   # Quality of Service (0, 1, 2)
  
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
  
  # Performance Tuning
  performance:
    batchSize: 100                         # Message batch size
    batchTimeout: 1s                       # Max batch wait time
    bufferSize: 8192                       # Processing buffer size
  
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

# Processing
processing:
  workers: 4                               # Number of processing workers
  queueSize: 1000                          # Internal queue size
  batchSize: 100                           # Processing batch size
```

## Rule Syntax

### Basic Rule Structure

```yaml
- topic: "input/topic"                     # Topic to subscribe to
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
    topic: "output/topic"                  # Topic to publish to
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
    "currentHour": "{@time.hour}",         # Time field
    "generatedId": "@{uuid7()}",           # Function
    "timestamp": "@{timestamp()}"          # Function
  }
```

## Complete Rule Examples

### Business Hours Alert
```yaml
- topic: sensors/temperature
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
    topic: alerts/business_hours
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

### Complex Nested Conditions
```yaml
- topic: system/monitoring
  conditions:
    operator: and
    items:
      - field: status
        operator: eq
        value: "active"
    groups:
      - operator: or
        items:
          - field: cpu_usage
            operator: gt
            value: 80
        groups:
          - operator: and
            items:
              - field: memory_usage
                operator: gt
                value: 90
              - field: "@time.hour"        # Night hours escalation
                operator: gte
                value: 22
  action:
    topic: alerts/critical
    payload: |
      {
        "alert": "Critical system condition",
        "metrics": {
          "cpu": {cpu_usage},
          "memory": {memory_usage},
          "status": {status}
        },
        "escalation": "immediate",
        "detectedAt": "@{timestamp()}",
        "incidentId": "@{uuid7()}"
      }
```

### Weekend Processing
```yaml
- topic: tasks/batch
  conditions:
    operator: and
    items:
      - field: task_type
        operator: eq
        value: "report"
      - field: "@day.number"               # Weekend days
        operator: gt
        value: 5
  action:
    topic: processing/weekend
    payload: |
      {
        "task": {task_data},
        "priority": "low",
        "sla": "24 hours",
        "weekendProcessing": true,
        "scheduledDay": "{@day.name}",
        "queuedAt": "{@timestamp.iso}",
        "batchId": "@{uuid4()}"
      }
```

## Monitoring

### Prometheus Metrics
- `messages_total{status}` - Messages processed by status
- `rule_matches_total` - Rule evaluation statistics
- `actions_total{status}` - Actions executed by result
- `template_operations_total{status}` - Template processing stats
- `process_memory_bytes` - Memory usage
- `worker_pool_active` - Active workers

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
  -broker-type string
        Override broker type (mqtt or nats)
  -workers int
        Override number of worker threads
  -metrics-addr string
        Override metrics server address
```

## Production Deployment

### Resource Requirements
- **CPU**: 2+ cores for high throughput
- **Memory**: 4GB+ RAM
- **Storage**: SSD recommended for NATS JetStream persistence

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

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) file for details.

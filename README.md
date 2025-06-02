# Rule Router

A high-performance message router built on [Watermill.io](https://watermill.io) that connects to external NATS JetStream or MQTT brokers, processes messages through sophisticated rule conditions with **time-based evaluation**, and publishes actions back to the brokers. Completely refactored for enhanced stability, simplified broker management, and production-ready middleware.

## 🚀 What's New in Watermill Version

This is a **complete architectural modernization** that replaces ~1000 lines of custom broker code with Watermill's proven abstractions while preserving all sophisticated rule engine capabilities.

### ✨ Major Improvements

- **🛡️ Enhanced Stability**: Built on Watermill's battle-tested Publisher/Subscriber interfaces
- **🔗 External Broker Connections**: Connects to your existing NATS JetStream or MQTT infrastructure  
- **⚡ NATS JetStream First**: High-performance async messaging optimized for 2,000-4,000 msg/sec
- **🔧 Simplified Configuration**: Reduced complexity with standardized Watermill patterns
- **🎯 Production Middleware**: Comprehensive stack with retry, circuit breaker, metrics, recovery
- **🔐 Full Authentication Support**: Username/password, TLS, NATS NKeys, .creds files
- **⏰ Time-Based Rule Evaluation**: NEW! Rules can now evaluate based on current time, day, date
- **📝 Enhanced Template Syntax**: New `{variable}` and `@{function()}` syntax with backward compatibility
- **🔄 Improved Functions**: Added `@{timestamp()}` alongside UUID generation

### 🔗 Template Syntax Evolution

**New Syntax (Recommended)**:
```yaml
payload: |
  {
    "alert": "High temperature!",
    "value": {temperature},           # Message data variables
    "timestamp": "@{timestamp()}",    # System functions  
    "id": "@{uuid7()}",
    "currentHour": "{@time.hour}"     # Time-based fields
  }
```

**Legacy Syntax (Still Supported)**:
```yaml
payload: '{"alert":"High temp!","value":${temperature},"id":"${uuid7()}"}'
```

## Features

- 🚀 **High-Performance Processing**: Built on Watermill for 2,000-4,000 messages/second capability
- 🔗 **External Broker Integration**:
  - 🚀 **NATS JetStream** with async publishing and parallel consumers (Primary)
  - 🔌 **MQTT** with full TLS support (Secondary)
- 🔐 **Comprehensive Authentication**:
  - **NATS**: Username/password, Token, NKeys, JWT with .creds files, TLS
  - **MQTT**: Username/password, TLS client certificates
- ⏰ **Time-Based Rule Evaluation**: Rules can evaluate based on current time, day of week, date, and timestamps
- 📝 **Sophisticated Rule Engine**: Complex condition evaluation with AND/OR logic and nested groups
- 📋 **Flexible Configuration**: YAML and JSON rule files with recursive directory loading
- 📊 **Production Monitoring**: Comprehensive Prometheus metrics and structured logging
- 🛡️ **Production Middleware**: Retry, circuit breaker, correlation ID, recovery, poison queue handling
- ⚙️ **Enhanced Template Processing**: New syntax with backward compatibility
- 🔍 **Fast Rule Indexing**: Optimized rule matching with object pooling

## Architecture Overview

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
- **External NATS JetStream server** or **MQTT broker** running and accessible
- Go 1.21 or higher
- SSL certificates (if using TLS)

### Setup

1. **Clone and Build**:
```bash
git clone https://github.com/skeeeon/rule-router
cd rule-router
go build -o rule-router ./cmd/rule-router
```

2. **Start External Broker** (choose one):

   **Option A: NATS JetStream (Recommended)**
   ```bash
   # Using Docker
   docker run -d --name nats-js -p 4222:4222 nats:latest -js
   ```

   **Option B: MQTT Broker**
   ```bash
   # Using Docker with Eclipse Mosquitto
   docker run -d --name mosquitto -p 1883:1883 eclipse-mosquitto:latest
   ```

3. **Configure for Your Broker**:
```bash
# For NATS JetStream
cp config/watermill-nats.yaml config/config.yaml

# For MQTT
cp config/watermill-mqtt.yaml config/config.yaml

# Edit config.yaml with your broker connection details
```

4. **Start the Router**:
```bash
./rule-router -config config/config.yaml -rules rules/
```

## Time-Based Rule Evaluation

### Available Time Fields

The rule engine supports time-based conditions and templates using system time fields:

| Field | Description | Example Values | Type |
|-------|-------------|----------------|------|
| `@time.hour` | Hour in 24-hour format | 0-23 | integer |
| `@time.minute` | Minute | 0-59 | integer |
| `@day.name` | Day name (lowercase) | "monday", "tuesday", "sunday" | string |
| `@day.number` | Day number (Monday=1) | 1-7 | integer |
| `@date.year` | Current year | 2024 | integer |
| `@date.month` | Month number | 1-12 | integer |
| `@date.day` | Day of month | 1-31 | integer |
| `@date.iso` | ISO date format | "2024-01-15" | string |
| `@timestamp.unix` | Unix timestamp | 1705344000 | integer |
| `@timestamp.iso` | ISO 8601 timestamp | "2024-01-15T14:30:00Z" | string |

### Time-Based Rule Examples

**Business Hours Alert (9 AM - 5 PM, Weekdays)**:
```yaml
- topic: sensors/temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
      - field: "@time.hour"
        operator: gte
        value: 9
      - field: "@time.hour"
        operator: lt
        value: 17
      - field: "@day.number"
        operator: lte
        value: 5
  action:
    topic: alerts/business_hours
    payload: |
      {
        "alert": "High temperature during business hours",
        "temperature": {temperature},
        "detectedAt": "{@timestamp.iso}",
        "businessDay": "{@day.name}",
        "currentTime": "{@time.hour}:{@time.minute}"
      }
```

**Weekend Escalation**:
```yaml
- topic: system/errors
  conditions:
    operator: and
    items:
      - field: severity
        operator: eq
        value: "critical"
      - field: "@day.number"
        operator: gt
        value: 5  # Saturday (6) or Sunday (7)
  action:
    topic: alerts/weekend_critical
    payload: |
      {
        "alert": "WEEKEND CRITICAL - Page On-Call",
        "error": {error_message},
        "weekendDay": "{@day.name}",
        "escalation": "immediate"
      }
```

**Maintenance Window Suppression**:
```yaml
- topic: system/alerts
  conditions:
    operator: and
    items:
      - field: alert_type
        operator: neq
        value: "critical"
      - field: "@day.name"
        operator: eq
        value: "sunday"
      - field: "@time.hour"
        operator: gte
        value: 2
      - field: "@time.hour"
        operator: lt
        value: 4
  action:
    topic: alerts/suppressed
    payload: "Alert suppressed during Sunday 2-4 AM maintenance window"
```

## Rule Configuration

### Enhanced Template Processing

**Available Template Functions**:
- `@{uuid4()}`: Random UUID v4
- `@{uuid7()}`: Time-ordered UUID v7
- `@{timestamp()}`: ISO8601 timestamp

**Template Variables**:
- `{fieldName}`: Message field values
- `{@time.field}`: Time-based system fields

**Complete Example**:
```yaml
- topic: sensors/environment
  conditions:
    operator: and
    items:
      - field: status
        operator: eq
        value: active
    groups:
      - operator: or
        items:
          - field: temperature
            operator: gt
            value: 32
        groups:
          - operator: and
            items:
              - field: humidity
                operator: gt
                value: 85
              - field: pressure
                operator: lt
                value: 990
  action:
    topic: alerts/environment
    payload: |
      {
        "alert": "Critical environmental conditions!",
        "conditions": {
          "temperature": {temperature},
          "humidity": {humidity},
          "pressure": {pressure}
        },
        "timestamp": "@{timestamp()}",
        "alertId": "@{uuid7()}",
        "detectedAt": "{@timestamp.iso}",
        "currentHour": "{@time.hour}"
      }
```

## Configuration

### NATS JetStream (Recommended)

```yaml
# High-Performance NATS Configuration
brokerType: nats

nats:
  urls:
    - nats://nats-server:4222
  
  # Authentication (choose one)
  username: "rule-router-service"      # Username/password
  password: "secure-password"
  # token: "your-nats-token"           # OR Token auth
  # nkey: "your-nkey"                  # OR NKey auth  
  # credsFile: "/path/to/app.creds"    # OR JWT with .creds file
  
  tls:
    enable: true
    certFile: "/etc/ssl/nats/client.pem"
    keyFile: "/etc/ssl/nats/client-key.pem"
    caFile: "/etc/ssl/nats/ca.pem"

watermill:
  nats:
    publishAsync: true              # Critical for high throughput
    maxPendingAsync: 2000          # Support 2000+ msg/sec
    subscriberCount: 8             # Parallel consumers
```

### MQTT Configuration

```yaml
# MQTT Configuration
brokerType: mqtt

mqtt:
  broker: ssl://mqtt-broker:8883
  clientId: rule-router
  username: "mqtt-user"
  password: "mqtt-password"
  qos: 0                         # High throughput
  
  tls:
    enable: true
    certFile: "/etc/ssl/mqtt/client.pem"
    keyFile: "/etc/ssl/mqtt/client-key.pem"  
    caFile: "/etc/ssl/mqtt/ca.pem"
```

## Performance Characteristics

### Benchmarks
- **Target Throughput**: 2,000-4,000 messages/second with complex rules
- **Watermill NATS Capability**: 50,668 msg/s publish, 34,713 msg/s subscribe
- **Latency**: Sub-100ms rule evaluation maintained
- **Memory**: Efficient with object pooling

### Time Evaluation Performance
- **Time Field Lookup**: Pre-computed fields for O(1) access
- **Time Context Creation**: Once per message batch for consistency
- **Overhead**: Minimal (~5% for time-heavy rules)

### Optimization Features
- **NATS JetStream**: Async publishing with high pending message limits
- **Parallel Processing**: Multiple consumers based on CPU cores
- **Batch Processing**: Configurable message batching
- **Connection Pooling**: Optimized connection management
- **Object Pooling**: Memory-efficient message processing

## Monitoring & Observability

### Prometheus Metrics
- `messages_total` - Message processing by status
- `rule_matches_total` - Rule evaluation statistics  
- `actions_total` - Action execution by result
- `template_operations_total` - Template processing stats
- `watermill_handler_execution_duration_seconds` - Processing latency
- `process_memory_bytes` - Memory usage

### Structured Logging
```json
{
  "timestamp": "2024-02-14T12:00:00Z",
  "level": "info",
  "msg": "message processing complete",
  "uuid": "01HV123...",
  "topic": "sensors/temperature",
  "actionsGenerated": 2,
  "duration": "15ms"
}
```

## Command Line Options

```bash
./rule-router \
  -config config/watermill-nats.yaml \
  -rules rules/ \
  -broker-type nats \
  -workers 8 \
  -metrics-addr :2112
```

## Production Deployment

### Infrastructure Requirements
- **NATS JetStream**: 3+ node cluster with persistent storage (SSD recommended)
- **Monitoring**: Prometheus + Grafana with Watermill dashboard
- **Resources**: 2+ CPU cores, 4GB+ RAM for high throughput

### Production Configuration
```yaml
watermill:
  nats:
    publishAsync: true
    maxPendingAsync: 2000
    subscriberCount: 16        # Scale with load
  performance:
    batchSize: 100
    batchTimeout: 1s
  middleware:
    retryMaxAttempts: 5
    metricsEnabled: true
```

### Health Checks
- HTTP endpoint: `http://localhost:2112/metrics`
- Key metrics: `watermill_handler_execution_duration_seconds`
- Alert on: Circuit breaker trips, high error rates, queue depth

## Contributing

1. Fork the repository
2. Create feature branch (`git checkout -b feature/amazing-feature`)  
3. Commit changes (`git commit -m 'Add amazing feature'`)
4. Push to branch (`git push origin feature/amazing-feature`)
5. Open Pull Request

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) file for details.

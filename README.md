# Rule Router

A high-performance message router built on [Watermill.io](https://watermill.io) that connects to external NATS JetStream or MQTT brokers, processes messages through sophisticated rule conditions, and publishes actions back to the brokers. Completely refactored for enhanced stability, simplified broker management, and production-ready middleware.

## 🚀 What's New in Watermill Version

This is a **complete architectural modernization** that replaces ~1000 lines of custom broker code with Watermill's proven abstractions while preserving all sophisticated rule engine capabilities.

### ✨ Major Improvements

- **🛡️ Enhanced Stability**: Built on Watermill's battle-tested Publisher/Subscriber interfaces
- **🔗 External Broker Connections**: Connects to your existing NATS JetStream or MQTT infrastructure  
- **⚡ NATS JetStream First**: High-performance async messaging optimized for 2,000-4,000 msg/sec
- **🔧 Simplified Configuration**: Reduced complexity with standardized Watermill patterns
- **🎯 Production Middleware**: Comprehensive stack with retry, circuit breaker, metrics, recovery
- **🔐 Full Authentication Support**: Username/password, TLS, NATS NKeys, .creds files
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
    "id": "@{uuid7()}"
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
- 📝 **Sophisticated Rule Engine**: Complex condition evaluation with AND/OR logic (Preserved)
- 📋 **Flexible Configuration**: YAML and JSON rule files with recursive directory loading
- 📊 **Production Monitoring**: Comprehensive Prometheus metrics and structured logging
- 🛡️ **Production Middleware**: Retry, circuit breaker, correlation ID, recovery, poison queue handling
- ⚙️ **Enhanced Template Processing**: New syntax with backward compatibility
- 🔍 **Fast Rule Indexing**: Optimized rule matching with object pooling (Preserved)

## Architecture Overview

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   External      │◄──►│  Watermill       │───▶│  Rule Engine    │
│   NATS/MQTT     │    │  Router +        │    │  (Preserved)    │
│   Brokers       │    │  Middleware      │    │                 │
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

## Project Structure

```
rule-router/
├── cmd/
│   └── rule-router/
│       └── main.go                   # Watermill-based application
├── config/
│   ├── config.go                     # Enhanced configuration with Watermill support  
│   ├── watermill-nats.yaml          # NATS JetStream configuration
│   └── watermill-mqtt.yaml          # MQTT configuration
├── internal/
│   ├── broker/                       # NEW: Watermill broker setup
│   │   ├── nats_watermill.go         # High-performance NATS JetStream
│   │   └── mqtt_watermill.go         # MQTT wrapper
│   ├── handler/                      # NEW: Watermill message handlers
│   │   ├── message_processor.go      # Rule engine integration
│   │   └── middleware.go             # Production middleware stack
│   ├── rule/                         # PRESERVED: Sophisticated rule engine
│   │   ├── processor.go              # Enhanced template processing
│   │   ├── evaluator.go              # Complex condition evaluation
│   │   ├── index.go                  # Fast rule indexing
│   │   ├── pool.go                   # Object pooling
│   │   └── loader.go                 # Rule file loading
│   ├── logger/                       # PRESERVED: Structured logging
│   └── metrics/                      # PRESERVED: Prometheus metrics
├── rules/                            # Enhanced rule examples
│   ├── temperature.yaml              # New template syntax examples
│   └── complex.yaml                  # Complex nested conditions
└── go.mod                            # Updated with Watermill dependencies
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

## Rule Configuration

### Enhanced Template Syntax

**New Recommended Syntax**:
```yaml
- topic: sensors/temperature
  conditions:
    operator: and
    items:
      - field: temperature
        operator: gt
        value: 30
  action:
    topic: alerts/temperature
    payload: |
      {
        "alert": "High temperature detected!",
        "value": {temperature},        # Message data
        "deviceId": {deviceId},
        "timestamp": "@{timestamp()}",  # System function
        "alertId": "@{uuid7()}",       # Time-ordered UUID
        "correlationId": "@{uuid4()}"  # Random UUID
      }
```

**Available Functions**:
- `@{uuid4()}`: Random UUID v4
- `@{uuid7()}`: Time-ordered UUID v7 (NEW)
- `@{timestamp()}`: ISO8601 timestamp (NEW)

**Legacy Syntax** (Still Supported):
```yaml
payload: '{"alert":"High temp!","value":${temperature},"id":"${uuid7()}"}'
```

### Complex Nested Conditions

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
        "alertId": "@{uuid7()}"
      }
```

## Performance Characteristics

### Benchmarks
- **Target Throughput**: 2,000-4,000 messages/second with complex rules
- **Watermill NATS Capability**: 50,668 msg/s publish, 34,713 msg/s subscribe
- **Latency**: Sub-100ms rule evaluation maintained
- **Memory**: Efficient with preserved object pooling

### Optimization Features
- **NATS JetStream**: Async publishing with high pending message limits
- **Parallel Processing**: Multiple consumers based on CPU cores
- **Batch Processing**: Configurable message batching
- **Connection Pooling**: Optimized connection management
- **Object Pooling**: Memory-efficient message processing (preserved)

## Monitoring & Observability

### Prometheus Metrics
- `messages_total` - Message processing by status
- `rule_matches_total` - Rule evaluation statistics  
- `actions_total` - Action execution by result
- `template_operations_total` - Template processing stats
- `watermill_handler_execution_duration_seconds` - Processing latency
- `process_memory_bytes` - Memory usage

### Grafana Dashboard
Use community Watermill dashboard ID: **9777** for comprehensive monitoring.

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

## Migration from Custom Broker

### What Changed
- ✅ **Rule Engine**: Completely preserved - no changes to business logic
- ✅ **Configuration**: Enhanced with Watermill options, backward compatible
- ✅ **Rule Files**: All existing rules work with new template syntax optional
- 🔄 **Infrastructure**: Replaced custom brokers with Watermill abstractions
- ➕ **New Features**: Enhanced functions, production middleware

### Upgrade Path
1. **Update Dependencies**: `go mod tidy` to get Watermill packages
2. **Update Binary Name**: `watermill-router` instead of `mqtt-mux-router`
3. **Optional Config**: Add Watermill section for enhanced features
4. **Optional Templates**: Use new `{var}/@{func()}` syntax for new rules

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

## Development

### Running Tests
```bash
go test ./...
```

### Load Testing
```bash
# Test with expected message rates
go run cmd/load-test/main.go -rate 3000 -duration 5m
```

## Contributing

1. Fork the repository
2. Create feature branch (`git checkout -b feature/amazing-feature`)  
3. Commit changes (`git commit -m 'Add amazing feature'`)
4. Push to branch (`git push origin feature/amazing-feature`)
5. Open Pull Request

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) file for details.

---

## 📊 Performance Comparison

| Metric | Before (Custom) | After (Watermill) | Improvement |
|--------|----------------|-------------------|-------------|
| Code Complexity | ~1000 lines broker code | ~200 lines integration | 80% reduction |
| Throughput | 2,000-4,000 msg/sec | 2,000-4,000+ msg/sec | Maintained/Enhanced |
| Reliability | Custom reconnection | Battle-tested Watermill | Significant improvement |
| Middleware | Basic retry | Comprehensive stack | Production-ready |
| Monitoring | Custom metrics | Watermill + custom | Enhanced observability |

**Bottom Line**: Dramatically simplified infrastructure while maintaining sophisticated rule processing capabilities and enhancing production readiness.

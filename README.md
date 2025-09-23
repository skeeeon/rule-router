# Rule Router

A high-performance NATS message router built on [Watermill.io](https://watermill.io) that processes messages through sophisticated rule conditions with time-based evaluation, NATS Key-Value integration, and publishes templated actions back to NATS.

## Features

- 🚀 **High-Performance Processing**: Built on Watermill for 2,000-4,000 messages/second capability
- 🔗 **NATS JetStream Integration**: Connect to existing NATS JetStream infrastructure
- 🗄️ **NATS Key-Value Support**: Dynamic lookups with JSON path traversal for stateful rules
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
# NATS JetStream with Key-Value support
docker run -d --name nats-js -p 4222:4222 nats:latest -js
```

3. **Configure**:
```bash
# Copy sample configuration
cp config/config.yaml config/my-config.yaml
# Edit with your NATS server details and KV buckets
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

### NATS Key-Value Configuration

```yaml
# NATS Key-Value Store Configuration
kv:
  enabled: true                            # Enable KV support
  buckets:                                 # Pre-configured KV buckets
    - "device_status"                      # Device operational status
    - "device_config"                      # Device configuration data
    - "user_preferences"                   # User settings and preferences
    - "system_config"                      # System configuration values
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

### Subject Token Access

Access NATS subject components using subject fields:

| Field | Description | Example |
|-------|-------------|---------|
| `@subject` | Full subject | "sensors.temperature.room1" |
| `@subject.count` | Token count | 3 |
| `@subject.0` | First token | "sensors" |
| `@subject.1` | Second token | "temperature" |
| `@subject.first` | First token | "sensors" |
| `@subject.last` | Last token | "room1" |

### NATS Key-Value Lookups

Access NATS Key-Value stores with optional JSON path traversal:

#### Basic KV Syntax
```yaml
# Simple key lookup
- field: "@kv.device_status.sensor-001"
  operator: eq
  value: "active"

# Dynamic key from message field
- field: "@kv.device_status.{device_id}"
  operator: eq  
  value: "active"

# Dynamic key from subject
- field: "@kv.device_status.{@subject.1}"
  operator: eq
  value: "active"
```

#### JSON Path Traversal
```yaml
# KV Value: {"tier": "premium", "shipping": {"method": "next_day"}}
# Access nested JSON fields with dot notation

- field: "@kv.customer_data.{customer_id}.tier"
  operator: eq
  value: "premium"

- field: "@kv.customer_data.{customer_id}.shipping.method"  
  operator: eq
  value: "next_day"

# Array access
# KV Value: {"addresses": [{"city": "Seattle"}, {"city": "Portland"}]}
- field: "@kv.customer_data.{customer_id}.addresses.0.city"
  operator: eq
  value: "Seattle"
```

#### KV Template Usage
```yaml
payload: |
  {
    "device_status": "{@kv.device_status.{device_id}}",
    "customer_tier": "{@kv.customer_data.{customer_id}.tier}",
    "shipping_method": "{@kv.customer_data.{customer_id}.shipping.method}",
    "primary_address": "{@kv.customer_data.{customer_id}.addresses.0.city}",
    "config_value": "{@kv.system_config.max_temperature}"
  }
```

### Template Functions

Use these functions in action templates:

| Function | Description | Example Output |
|----------|-------------|----------------|
| `{@uuid4()}` | Random UUID v4 | `a1b2c3d4-e5f6-...` |
| `{@uuid7()}` | Time-ordered UUID v7 | `01234567-89ab-...` |
| `{@timestamp()}` | Current ISO timestamp | `2024-01-15T14:30:00Z` |

### Template Variables

Access message fields, time data, subject data, and KV data in templates:

```yaml
payload: |
  {
    "messageField": {fieldName},                    # Message data
    "nestedField": {user.profile.name},             # Nested message data
    "currentHour": "{@time.hour}",                  # Time field
    "subjectToken": "{@subject.1}",                 # Subject token
    "deviceStatus": "{@kv.device_status.{device_id}}", # KV lookup
    "configPath": "{@kv.system_config.email.smtp.host}", # KV JSON path
    "generatedId": "{@uuid7()}",                    # Function
    "timestamp": "{@timestamp()}"                   # Function
  }
```

## Complete Rule Examples

### Basic Temperature Alert with KV Status Check
```yaml
- topic: sensors.temperature
  conditions:
    operator: and
    items:
      - field: "@kv.device_status.{sensor_id}"      # Check device is active
        operator: eq
        value: "active"
      - field: temperature
        operator: gt
        value: 30
  action:
    topic: alerts.temperature
    payload: |
      {
        "alert": "High temperature from active device",
        "device": {
          "id": {sensor_id},
          "status": "{@kv.device_status.{sensor_id}}",
          "location": "{@kv.device_config.{sensor_id}.location.building}"
        },
        "reading": {
          "temperature": {temperature},
          "threshold": "{@kv.device_config.{sensor_id}.thresholds.max}"
        },
        "timestamp": "{@timestamp()}",
        "alertId": "{@uuid7()}"
      }
```

### Business Hours Processing with Customer Data
```yaml
- topic: orders.new
  conditions:
    operator: and
    items:
      - field: "@kv.customer_data.{customer_id}.tier"    # Premium customers only
        operator: eq
        value: "premium"
      - field: "@time.hour"                              # Business hours only
        operator: gte
        value: 9
      - field: "@time.hour"
        operator: lt
        value: 17
      - field: order_value
        operator: gt
        value: 1000
  action:
    topic: fulfillment.priority
    payload: |
      {
        "priority_order": "Premium customer large order during business hours",
        "customer": {
          "id": {customer_id},
          "tier": "{@kv.customer_data.{customer_id}.tier}",
          "name": "{@kv.customer_data.{customer_id}.profile.name}",
          "shipping_method": "{@kv.customer_data.{customer_id}.shipping.preferences.method}"
        },
        "order": {
          "value": {order_value},
          "priority": "high"
        },
        "timing": {
          "business_hours": true,
          "current_time": "{@time.hour}:{@time.minute}",
          "day": "{@day.name}"
        },
        "processing": {
          "assigned_at": "{@timestamp()}",
          "order_id": "{@uuid7()}"
        }
      }
```

### Equipment Maintenance with Complex JSON Configuration
```yaml
# Subject: equipment.pump.maintenance.pump-007.schedule
# KV: equipment_config["pump-007"] = {
#   "maintenance": {
#     "schedules": {
#       "routine": {"frequency_days": 30, "duration_hours": 2},
#       "emergency": {"response_time_hours": 4}
#     }
#   },
#   "location": {"building": "A", "floor": 2}
# }
- topic: equipment.*.maintenance.*.schedule
  conditions:
    operator: and
    items:
      - field: "@kv.equipment_config.{@subject.3}.maintenance.schedules.{maintenance_type}.frequency_days"
        operator: exists
      - field: maintenance_type
        operator: eq
        value: "routine"
  action:
    topic: maintenance.scheduled
    payload: |
      {
        "maintenance": "Equipment maintenance scheduled",
        "equipment": {
          "type": "{@subject.1}",
          "id": "{@subject.3}",
          "location": {
            "building": "{@kv.equipment_config.{@subject.3}.location.building}",
            "floor": "{@kv.equipment_config.{@subject.3}.location.floor}"
          }
        },
        "schedule": {
          "type": {maintenance_type},
          "frequency_days": "{@kv.equipment_config.{@subject.3}.maintenance.schedules.{maintenance_type}.frequency_days}",
          "duration_hours": "{@kv.equipment_config.{@subject.3}.maintenance.schedules.{maintenance_type}.duration_hours}"
        },
        "scheduling": {
          "scheduled_by": {scheduled_by},
          "scheduled_at": "{@timestamp()}",
          "maintenance_id": "{@uuid7()}"
        }
      }
```

### Multi-Bucket KV Lookup with Fallback Logic
```yaml
- topic: user.activity
  conditions:
    operator: or                                    # Fallback logic
    items:
      - field: "@kv.user_permissions.{user_id}.features.dashboard"  # Primary permissions
        operator: eq
        value: true
      - field: "@kv.user_roles.{user_id}.role"     # Fallback to role-based
        operator: eq
        value: "admin"
  action:
    topic: dashboard.access-granted
    payload: |
      {
        "access": "Dashboard access granted",
        "user": {
          "id": {user_id},
          "permissions": "{@kv.user_permissions.{user_id}.features.dashboard}",
          "role": "{@kv.user_roles.{user_id}.role}",
          "preferences": {
            "theme": "{@kv.user_settings.{user_id}.ui.theme}",
            "timezone": "{@kv.user_settings.{user_id}.locale.timezone}"
          }
        },
        "access_granted_at": "{@timestamp()}"
      }
```

## NATS Key-Value Setup

### Create and Populate KV Buckets

```bash
# Create KV buckets
nats kv add device_status
nats kv add device_config  
nats kv add customer_data
nats kv add system_config

# Populate simple values
nats kv put device_status sensor-001 "active"
nats kv put device_status pump-002 "maintenance"

# Populate JSON configuration
nats kv put device_config sensor-001 '{
  "location": {"building": "A", "floor": 3, "room": "server-room"},
  "thresholds": {"min": 10, "max": 35, "critical": 40},
  "hardware": {"model": "TempSensor-Pro", "firmware": "2.1.4"}
}'

nats kv put customer_data cust123 '{
  "tier": "premium",
  "profile": {"name": "Acme Corp", "contact": {"email": "admin@acme.com"}},
  "shipping": {
    "preferences": {"method": "next_day", "carrier": "FedEx"},
    "addresses": [
      {"type": "primary", "city": "Seattle", "zip": "98101"},
      {"type": "secondary", "city": "Portland", "zip": "97201"}
    ]
  }
}'

# System configuration
nats kv put system_config max_temperature "35"
nats kv put system_config email_settings '{
  "smtp": {"host": "smtp.company.com", "port": 587, "tls": true},
  "templates": {"alert": {"subject": "Alert: {title}", "priority": "high"}}
}'
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

# KV-specific logs
./rule-router -config config/config.yaml -rules rules/ | grep -i "kv"
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
- **Bottlenecks**: JSON processing (50-60%), KV lookups (10-15%), template processing (15-20%), I/O (10-15%)
- **Scaling**: Linear with NATS cluster size

### KV Performance
- **Co-located Deployment**: Sub-millisecond KV lookups
- **Network Deployment**: 1-5ms KV lookups depending on network latency
- **JSON Path Overhead**: Minimal (~0.1ms for typical JSON structures)
- **Caching**: Rule-router relies on NATS JetStream's built-in caching

### Resource Requirements
- **CPU**: 2+ cores for high throughput
- **Memory**: 50-200MB depending on rule complexity and KV usage
- **Storage**: Minimal (stateless design, data in NATS KV)

## Production Deployment

### High Availability Setup
```yaml
# NATS Cluster Configuration
nats:
  urls:
    - nats://nats-1:4222
    - nats://nats-2:4222
    - nats://nats-3:4222

# KV Configuration  
kv:
  enabled: true
  buckets:
    - "device_status"
    - "customer_data" 
    - "system_config"
  
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

### KV Data Management

```bash
# Backup KV data
nats kv ls device_status | while read key; do
  echo "Backing up $key"
  nats kv get device_status "$key" > "backup/${key}.json"
done

# Restore KV data  
for file in backup/*.json; do
  key=$(basename "$file" .json)
  nats kv put device_status "$key" "$(cat "$file")"
done

# Monitor KV usage
nats kv status device_status
nats kv watch device_status  # Watch for changes
```

## Advanced Features

### Wildcard Pattern Matching

```yaml
# Single-level wildcard
- topic: sensors.*                         # matches sensors.temperature, sensors.pressure
  
# Multi-level wildcard  
- topic: building.>                        # matches building.floor1.room1.temperature
  
# Mixed patterns
- topic: devices.*.data                    # matches devices.sensor001.data
```

### Complex Condition Groups

```yaml
conditions:
  operator: and
  items:
    - field: priority
      operator: eq
      value: "high"
  groups:
    - operator: or                         # Nested OR group within AND
      items:
        - field: "@time.hour"
          operator: lt
          value: 9                         # Before 9 AM
        - field: "@time.hour" 
          operator: gt
          value: 17                        # After 5 PM
```

### Dynamic Routing

```yaml
action:
  topic: "alerts.{@subject.1}.{severity}"  # Dynamic topic from subject and message
  payload: |
    {
      "routed_to": "alerts.{@subject.1}.{severity}",
      "original_subject": "{@subject}",
      "routing_logic": "dynamic based on subject and severity"
    }
```

## Troubleshooting

### Common Issues

1. **KV Bucket Not Found**
   ```
   Error: KV bucket 'device_status' not configured
   ```
   **Solution**: Add bucket to `config.yaml` kv.buckets list

2. **JSON Path Invalid**
   ```
   DEBUG: JSON path traversal failed: JSON path segment not found: nonexistent_field
   ```
   **Solution**: Verify JSON structure and path in KV data

3. **Variable Resolution Failed**
   ```
   DEBUG: KV variable not found: device_id
   ```
   **Solution**: Ensure message contains the referenced field

4. **Connection Issues**
   ```
   Error: failed to initialize KV stores
   ```
   **Solution**: Verify NATS server has JetStream enabled

### Debug Commands

```bash
# Check NATS JetStream status
nats server check jetstream

# List KV buckets
nats kv ls

# Check specific KV data
nats kv get device_status sensor-001

# Monitor rule processing
./rule-router -config config/config.yaml -rules rules/ | grep -E "(kv|KV|json)"

# Test rule validation
./rule-router -config config/config.yaml -rules rules/ --dry-run
```

## Known Limitations

- **JSON Only**: Message payloads must be valid JSON
- **Startup Rule Loading**: Rules only loaded at application start (hot reloading planned)
- **KV Bucket Limit**: Maximum recommended 100 buckets per rule-router instance
- **JSON Path Complexity**: No advanced JSONPath queries (filtering, expressions)

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) file for details.

# HTTP Gateway

Bidirectional HTTP ↔ NATS integration gateway with rule-based routing, templating, and transformation.

## Features

- **HTTP → NATS** (Inbound Webhooks): Fire-and-forget pattern, returns 200 immediately
- **NATS → HTTP** (Outbound Webhooks): ACK-on-success with exponential backoff retry
- **Array Processing**: Full support for batch webhooks with forEach iteration
- **Primitive Message Support**: Handle strings, numbers, arrays, and objects
- **Shared Rule Engine**: Code reuse with rule-router
- **Template Engine**: Variable substitution, KV lookups, time-based routing
- **Prometheus Metrics**: Complete observability
- **Graceful Shutdown**: Clean resource cleanup

## Quick Start

### Prerequisites

- Go 1.23+
- NATS Server with JetStream enabled
- JetStream streams configured for your subjects

### Installation

```bash
# Build
go build -o http-gateway ./cmd/http-gateway

# Run
./http-gateway -config config/http-gateway.yaml -rules rules/
```

### Example Configuration

```yaml
http:
  server:
    address: ":8080"
  client:
    timeout: "30s"

nats:
  urls:
    - "nats://localhost:4222"

forEach:
  maxIterations: 100  # Limit batch webhook processing
```

See `config/http-gateway.example.yaml` for full configuration.

## Usage

### Inbound Webhooks (HTTP → NATS)

Receive HTTP webhooks and publish to NATS:

```yaml
- trigger:
    http:
      path: "/webhooks/github"
      method: "POST"
  conditions:
    operator: and
    items:
      - field: "@header.X-GitHub-Event"
        operator: eq
        value: "pull_request"
  action:
    nats:
      subject: "github.events.pr"
      payload: |
        {
          "repo": "{repository.name}",
          "pr": {number},
          "author": "{user.login}"
        }
```

**Test:**
```bash
curl -X POST http://localhost:8080/webhooks/github \
  -H "X-GitHub-Event: pull_request" \
  -d '{"repository": {"name": "test"}, "number": 42}'
```

### Outbound Webhooks (NATS → HTTP)

Subscribe to NATS and make HTTP requests:

```yaml
- trigger:
    nats:
      subject: "alerts.critical.>"
  action:
    http:
      url: "https://events.pagerduty.com/v2/enqueue"
      method: "POST"
      payload: '{"alert": "{message}"}'
      retry:
        maxAttempts: 3
        initialDelay: "1s"
```

**Test:**
```bash
nats pub alerts.critical.test '{"message": "System down"}'
```

## Array Processing

The http-gateway supports powerful array operations for batch webhook processing.

### Batch Webhook Processing (HTTP → NATS)

Process multiple items from a single webhook:

```yaml
- trigger:
    http:
      path: "/webhooks/stripe/events"
      method: "POST"
  
  conditions:
    operator: and
    items:
      # Check if ANY event is a successful payment
      - field: "data"
        operator: any
        conditions:
          operator: and
          items:
            - field: "type"
              operator: eq
              value: "charge.succeeded"
  
  action:
    nats:
      # Generate one NATS message per successful charge
      forEach: "data"
      filter:
        operator: and
        items:
          - field: "type"
            operator: eq
            value: "charge.succeeded"
      subject: "payments.success.{object.customer}"
      payload: |
        {
          "customerId": "{object.customer}",
          "amount": {object.amount},
          "currency": "{object.currency}",
          "chargeId": "{id}",
          "webhookId": "{@msg.id}",
          "receivedAt": "{@msg.created}"
        }
```

**Input Webhook:**
```json
{
  "id": "evt_batch_123",
  "created": 1642598400,
  "data": [
    {
      "id": "ch_abc123",
      "type": "charge.succeeded",
      "object": {
        "customer": "cus_ABC",
        "amount": 5000,
        "currency": "usd"
      }
    },
    {
      "id": "ch_def456",
      "type": "charge.succeeded",
      "object": {
        "customer": "cus_DEF",
        "amount": 7500,
        "currency": "usd"
      }
    },
    {
      "id": "ch_xyz789",
      "type": "charge.failed",
      "object": {
        "customer": "cus_XYZ",
        "amount": 3000,
        "currency": "usd"
      }
    }
  ]
}
```

**Result**: 2 NATS messages published (failed charge filtered out)

### Batch Outbound Webhooks (NATS → HTTP)

Generate multiple HTTP requests from a NATS message:

```yaml
- trigger:
    nats:
      subject: "notifications.batch"
  
  action:
    http:
      # Send one HTTP request per recipient
      forEach: "recipients"
      filter:
        operator: and
        items:
          - field: "active"
            operator: eq
            value: true
      url: "https://api.notification.com/send/{userId}"
      method: "POST"
      payload: |
        {
          "userId": "{userId}",
          "message": "{@msg.message}",
          "priority": "{@msg.priority}",
          "timestamp": "{@timestamp()}"
        }
      headers:
        Authorization: "Bearer ${NOTIFICATION_API_KEY}"
```

**Template Context:**
- `{userId}` → From recipients[i]
- `{@msg.message}` → From root message
- `{@timestamp()}` → System function

### Performance Limits

Configure maximum iterations to prevent resource exhaustion:

```yaml
forEach:
  maxIterations: 100  # Default: 100, Max: 10000
```

**Monitoring:**
```bash
curl -s localhost:2112/metrics | grep forEach
```

## Primitive Message Support

The http-gateway supports any valid JSON type for both inbound webhooks and outbound requests, including primitives and arrays. This enables seamless integration with third-party APIs that use simple formats.

**Supported Types:**
- Objects: `{"field": "value"}` - Pass through unchanged  
- Arrays: `[...]` - Wrapped as `{"@items": [...]}`
- Strings: `"text"` - Wrapped as `{"@value": "text"}`
- Numbers: `42` - Wrapped as `{"@value": 42}`
- Booleans: `true` - Wrapped as `{"@value": true}`
- Null: `null` - Wrapped as `{"@value": null}`

### Quick Examples

**Inbound: Simple Webhook**
```yaml
- trigger:
    http:
      path: "/webhooks/status"
      method: "POST"
  
  conditions:
    operator: and
    items:
      - field: "@value"  # String body
        operator: contains
        value: "completed"
  
  action:
    nats:
      subject: "status.completed"
      payload: '{"status": "{@value}"}'
```

**Inbound: Array Webhook**
```yaml
- trigger:
    http:
      path: "/webhooks/metrics"
      method: "POST"
  
  action:
    nats:
      forEach: "@items"  # Array at root
      filter:
        operator: and
        items:
          - field: "@value"
            operator: gt
            value: 100
      subject: "metrics.high"
      payload: '{"value": {@value}}'
```

**Outbound: Primitive Array**
```yaml
- trigger:
    nats:
      subject: "notify.batch"
  
  action:
    http:
      forEach: "userIds"  # String array
      url: "https://api.example.com/notify/{@value}"
      method: "POST"
      payload: |
        {
          "userId": "{@value}",
          "message": "{@msg.message}"
        }
```

### Use Cases

**Inbound (HTTP → NATS):**
- Simple status webhooks (strings)
- Batch metric uploads (number arrays)  
- IoT sensor data (SenML arrays)
- Log aggregation (primitive messages)

**Outbound (NATS → HTTP):**
- Multi-recipient notifications (string arrays)
- Batch API calls (primitive lists)
- Webhook fan-out (array processing)

### Template Context

When using forEach with primitives:
- `{@value}` → Access primitive value
- `{@msg.field}` → Access root message fields

For complete documentation and examples, see the [main README: Primitive & Array Root Messages](../../README.md#primitive--array-root-messages).

### Troubleshooting

**Inbound:**
- Webhook sends plain text → Access with `{@value}`
- Webhook sends array → Access with `@items` field
- Mixed content types → Auto-detection handles all cases

**Outbound:**
- String array forEach → Use `{@value}` in URL/payload
- Number array → Access with `{@value}`, formats as string automatically
- Empty arrays → No actions generated (expected behavior)

**Both:**
- Reserved names (`@value`, `@items`) only used during wrapping
- Objects never wrapped - backward compatible
- Performance impact < 1%

## Rule Syntax

### HTTP Triggers

```yaml
trigger:
  http:
    path: "/webhooks/service"    # Exact path match
    method: "POST"                # Optional (defaults to all methods)
```

### HTTP Actions

```yaml
action:
  http:
    url: "https://api.example.com"
    method: "POST"
    headers:
      Authorization: "Bearer TOKEN"
    payload: '{"data": "{field}"}'
    retry:
      maxAttempts: 3              # Default: 3
      initialDelay: "1s"          # Default: 1s
      maxDelay: "30s"             # Default: 30s
```

### Array Operators

Check if any/all/none elements match conditions:

```yaml
conditions:
  operator: and
  items:
    - field: "items"
      operator: any
      conditions:
        operator: and
        items:
          - field: "status"
            operator: eq
            value: "active"
```

### ForEach Actions

Generate multiple actions from array:

```yaml
action:
  http:
    forEach: "items"
    filter:
      operator: and
      items:
        - field: "status"
          operator: eq
          value: "active"
    url: "https://api.example.com/process/{id}"
    method: "POST"
    payload: |
      {
        "id": "{id}",
        "status": "{status}",
        "batchId": "{@msg.batchId}"
      }
```

### Template Variables

#### HTTP-Specific
```yaml
{@path}          # Full path: "/webhooks/github"
{@path.0}        # Path token: "webhooks"
{@path.1}        # Path token: "github"
{@path.count}    # Token count
{@method}        # HTTP method: "POST"
```

#### Universal (NATS + HTTP)
```yaml
{field}                          # JSON field
{nested.field}                   # Nested field
{@header.X-Custom}               # Request header
{@timestamp()}                   # Current timestamp
{@uuid7()}                       # Time-ordered UUID
{@kv.bucket.key:field}           # KV lookup
{@time.hour}                     # Current hour
{@day.name}                      # Current day name
{@msg.field}                     # Root message (in forEach)
{@value}                         # Primitive value
{@items}                         # Array at root
```

## Architecture

```
┌─────────────┐         ┌─────────────────┐         ┌──────────────┐
│   Webhook   │ ─────>  │  HTTP Gateway   │ ─────>  │     NATS     │
│   Senders   │         │                 │         │  JetStream   │
│  (GitHub,   │ <─────  │  Fire-and-Forget│ <─────  │              │
│   Stripe)   │         │  ACK-on-Success │         │              │
└─────────────┘         └─────────────────┘         └──────────────┘
```

### Inbound Flow (HTTP → NATS)

1. HTTP request arrives at `/webhooks/path`
2. Return `200 OK` immediately (fire-and-forget)
3. Extract headers and body
4. Evaluate rules asynchronously
5. Publish matched actions to NATS (supports forEach)
6. Log failures (caller already has 200)

### Outbound Flow (NATS → HTTP)

1. Subscribe to NATS subject
2. Fetch messages in batches
3. Evaluate rules
4. Make HTTP request(s) with retry (supports forEach)
5. ACK on HTTP 200-299
6. NAK on failure (triggers redelivery)

## Metrics

```bash
# HTTP Inbound
http_inbound_requests_total{path="/webhooks/github",method="POST",status="200"}
http_request_duration_seconds{path="/webhooks/github",method="POST"}

# HTTP Outbound
http_outbound_requests_total{url="https://api.example.com",status_code="200"}
http_outbound_duration_seconds{url="https://api.example.com"}

# Array Operations
forEach_iterations_total{rule_file="batch_webhook"}
forEach_filtered_total{rule_file="batch_webhook"}
forEach_actions_generated_total{rule_file="batch_webhook"}
forEach_duration_seconds{rule_file="batch_webhook"}
array_operator_evaluations_total{operator="any",result="true"}

# Rule Processing (shared)
messages_total{status="received|processed|error"}
actions_total{status="success|error"}
rule_matches_total
```

## Health Checks

```bash
# Check gateway health
curl http://localhost:8080/health
# {"status":"healthy"}

# Check metrics
curl http://localhost:2112/metrics
```

## Configuration

### HTTP Server
- `address` - Listen address (default: `:8080`)
- `readTimeout` - Max request read time (default: `30s`)
- `writeTimeout` - Max response write time (default: `30s`)
- `shutdownGracePeriod` - Graceful shutdown timeout (default: `30s`)
- `inboundWorkerCount` - Concurrent webhook processors (default: `10`)
- `inboundQueueSize` - Webhook queue buffer size (default: `100`)

### HTTP Client
- `timeout` - Request timeout (default: `30s`)
- `maxIdleConns` - Connection pool size (default: `100`)
- `maxIdleConnsPerHost` - Per-host connections (default: `10`)

### Array Processing
- `forEach.maxIterations` - Max array elements (default: `100`, max: `10000`)

### NATS (Same as rule-router)
- `consumers.maxDeliver` - Max redelivery attempts (default: `5`)
- `consumers.subscriberCount` - Workers per subscription (default: `4`)

## Security

### Inbound Webhooks

**V1 Limitations:**
- ⚠️ No authentication (use reverse proxy)
- ⚠️ No rate limiting (use reverse proxy)
- ⚠️ Fire-and-forget (monitor metrics)

**Production Setup:**
```nginx
# nginx reverse proxy with auth
location /webhooks/ {
    auth_request /auth;
    proxy_pass http://localhost:8080;
}
```

### Outbound Webhooks

**Best Practices:**
- Use environment variables for API tokens
- Secure rule files with filesystem permissions
- Monitor retry exhaustion
- Configure appropriate `maxDeliver`
- Validate forEach iteration limits

## Troubleshooting

### Inbound webhook not working

```bash
# Check server is listening
curl http://localhost:8080/health

# Check metrics
curl http://localhost:2112/metrics | grep http_inbound

# Check logs
./http-gateway -config config.yaml -rules rules/ | grep webhook
```

### Outbound webhook failing

```bash
# Check NATS subscription
nats consumer ls STREAM_NAME

# Check metrics
curl http://localhost:2112/metrics | grep http_outbound

# Monitor retries
curl http://localhost:2112/metrics | grep actions_total
```

### ForEach not working

```bash
# Check iteration limit
curl http://localhost:2112/metrics | grep forEach_iterations

# Verify filter conditions
rule-tester --rule my-rule.yaml --message test.json --verbose

# Check array structure in logs
# Look for "forEach array extracted" log messages
```

### Primitive message not working

```bash
# Check if message is being wrapped
# Look for "message wrapped" debug logs

# Verify template uses correct field
# String: {@@value}, Array root: @items, Objects: {field}

# Test with rule-tester
rule-tester --rule my-rule.yaml --message '"simple string"'
```

### Messages not being delivered

```bash
# Check streams exist
nats stream ls

# Check consumers exist
nats consumer ls STREAM_NAME

# Check rules are loaded
# Look for "rules loaded successfully" in logs
```

## Examples

See `examples/` directory for complete examples:

### Inbound (HTTP → NATS)
- **GitHub webhooks**: Process PR events
- **Stripe payments**: Handle payment events
- **Batch webhooks**: Multiple items per request
- **Simple webhooks**: String/number messages

### Outbound (NATS → HTTP)
- **PagerDuty alerts**: Send alerts on critical events
- **Slack notifications**: Team notifications
- **Batch notifications**: Multiple recipients per message
- **Primitive arrays**: Process string/number lists

### Array Processing
- **Batch order processing**: Process multiple orders from one webhook
- **Multi-recipient notifications**: Send to multiple users
- **Event aggregation**: Split batch events into individual messages
- **SenML processing**: IoT sensor arrays

## Testing

Use the rule-tester for development:

```bash
# Scaffold tests (detects forEach automatically)
rule-tester --scaffold rules/batch-webhook.yaml

# Run tests
rule-tester --test --rules rules/

# Quick check
rule-tester --rule rules/my-rule.yaml \
            --message test-data/batch.json
```

The tester automatically:
- Detects forEach operations
- Generates array input examples
- Validates multiple action outputs
- Tests filter conditions
- Handles primitive messages

See [rule-tester README](../rule-tester/README.md) for details.

## Development

### Build
```bash
go build -o http-gateway ./cmd/http-gateway
```

### Test
```bash
go test ./internal/gateway/...
```

### Lint
```bash
golangci-lint run ./internal/gateway/...
```

## Comparison with rule-router

| Feature | rule-router | http-gateway |
|---------|-------------|--------------|
| NATS → NATS | ✅ | ❌ |
| HTTP → NATS | ❌ | ✅ |
| NATS → HTTP | ❌ | ✅ |
| Array Operations | ✅ | ✅ |
| ForEach | ✅ | ✅ |
| Primitives | ✅ | ✅ |
| Rule Engine | ✅ Shared | ✅ Shared |
| KV Support | ✅ Shared | ✅ Shared |
| Metrics | ✅ | ✅ Enhanced |

**Use Cases:**
- **rule-router**: Internal NATS message routing and batch processing
- **http-gateway**: External webhook integration with batch support

## Performance

**Benchmarks** (batch webhook processing):
- ForEach (10 items): ~500µs
- ForEach (100 items): ~4.5ms
- Inbound queue: 100 concurrent webhooks
- Worker pool: 10 concurrent processors
- Throughput: 1000+ webhooks/second

**Optimizations:**
- Buffered inbound queue prevents 503 errors
- Worker pool provides backpressure
- Efficient forEach element processing
- Metrics for monitoring performance

## License

Same as parent project.

## Support

See main project README and documentation.

# HTTP Gateway

Bidirectional HTTP ↔ NATS integration gateway with rule-based routing, templating, and transformation.

## Features

- **HTTP → NATS** (Inbound Webhooks): Fire-and-forget pattern, returns 200 immediately
- **NATS → HTTP** (Outbound Webhooks): ACK-on-success with exponential backoff retry
- **Shared Rule Engine**: ~70% code reuse with rule-router
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
```

## Architecture

```
┌─────────────┐         ┌─────────────────┐         ┌──────────────┐
│   Webhook   │ ──────> │  HTTP Gateway   │ ──────> │     NATS     │
│   Senders   │         │                 │         │  JetStream   │
│  (GitHub,   │ <────── │  Fire-and-Forget│ <────── │              │
│   Stripe)   │         │  ACK-on-Success │         │              │
└─────────────┘         └─────────────────┘         └──────────────┘
```

### Inbound Flow (HTTP → NATS)

1. HTTP request arrives at `/webhooks/path`
2. Return `200 OK` immediately (fire-and-forget)
3. Extract headers and body
4. Evaluate rules asynchronously
5. Publish matched actions to NATS
6. Log failures (caller already has 200)

### Outbound Flow (NATS → HTTP)

1. Subscribe to NATS subject
2. Fetch messages in batches
3. Evaluate rules
4. Make HTTP request with retry
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

### HTTP Client
- `timeout` - Request timeout (default: `30s`)
- `maxIdleConns` - Connection pool size (default: `100`)
- `maxIdleConnsPerHost` - Per-host connections (default: `10`)

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

See `rules/http-examples/` for complete examples:
- GitHub webhooks
- Stripe payments
- PagerDuty alerts
- Slack notifications
- Multi-tenant routing
- KV enrichment

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
| Rule Engine | ✅ Shared | ✅ Shared |
| KV Support | ✅ Shared | ✅ Shared |
| Metrics | ✅ | ✅ Enhanced |

**Use Cases:**
- **rule-router**: Internal NATS message routing
- **http-gateway**: External webhook integration

## License

Same as parent project.

## Support

See main project README and documentation.

# NATS Auth Manager

Standalone application that manages authentication tokens for external APIs and stores them in NATS KV buckets. Enables `rule-router` and `http-gateway` to access tokens via `@kv.*` template syntax without any code changes.

## Features

- **OAuth2 Client Credentials**: Automatic token acquisition and refresh for OAuth2 APIs
- **Custom HTTP Authentication**: Support for proprietary authentication endpoints
- **Automatic Token Refresh**: Time-based or expiry-based refresh strategies
- **NATS KV Storage**: Tokens stored in NATS Key-Value buckets for fast access
- **Environment Variable Support**: Secure credential management via `${VAR}` syntax
- **Zero-Impact Integration**: Existing apps use tokens via `@kv.tokens.*` references

## Quick Start

### 1. Create KV Bucket

```bash
nats kv add tokens
```

### 2. Set Environment Variables

```bash
export STRIPE_CLIENT_ID="ca_xxx"
export STRIPE_CLIENT_SECRET="sk_test_xxx"
```

### 3. Configure Provider

Create `config/auth-manager.yaml`:

```yaml
nats:
  urls: ["nats://localhost:4222"]
  credsFile: "/etc/nats/rule-router.creds"

storage:
  bucket: "tokens"

logging:
  level: info
  encoding: json

providers:
  - id: "stripe-api"
    type: oauth2
    tokenUrl: "https://connect.stripe.com/oauth/token"
    clientId: "${STRIPE_CLIENT_ID}"
    clientSecret: "${STRIPE_CLIENT_SECRET}"
    scopes: ["read_write"]
    refreshBefore: "5m"
    kvKey: "stripe-api"
```

### 4. Run

```bash
./nats-auth-manager -config config/auth-manager.yaml
```

### 5. Use Tokens in Rules

```yaml
# In http-gateway or rule-router rules
action:
  http:
    url: "https://api.stripe.com/v1/charges"
    headers:
      Authorization: "Bearer {@kv.tokens.stripe-api}"
```

## Provider Types

### OAuth2 Client Credentials

Authenticates using OAuth2 client credentials grant. Automatically calculates refresh interval based on token expiry.

```yaml
providers:
  - id: "my-api"
    type: oauth2
    tokenUrl: "https://api.example.com/oauth/token"
    clientId: "${CLIENT_ID}"
    clientSecret: "${CLIENT_SECRET}"
    scopes: ["read", "write"]
    refreshBefore: "5m"  # Refresh 5 minutes before expiry
    kvKey: "my-api"
```

**Fields:**
- `tokenUrl` (required): OAuth2 token endpoint
- `clientId` (required): OAuth2 client ID
- `clientSecret` (required): OAuth2 client secret
- `scopes` (optional): OAuth2 scopes
- `refreshBefore` (required): Duration before expiry to refresh (e.g., "5m", "1h")
- `kvKey` (optional): KV bucket key (defaults to provider ID)

### Custom HTTP Authentication

For proprietary authentication endpoints. Uses time-based refresh.

```yaml
providers:
  - id: "pocketbase"
    type: custom-http
    authUrl: "https://pb.example.com/api/collections/users/auth-with-password"
    method: POST
    headers:
      Content-Type: "application/json"
    body: |
      {
        "identity": "${PB_EMAIL}",
        "password": "${PB_PASSWORD}"
      }
    tokenPath: "token"
    refreshEvery: "30m"
    kvKey: "pocketbase-admin"
```

**Fields:**
- `authUrl` (required): Authentication endpoint URL
- `method` (optional): HTTP method (defaults to POST)
- `headers` (optional): HTTP headers
- `body` (optional): Request body with `${VAR}` variable substitution
- `tokenPath` (required): JSON path to extract token (e.g., "token", "data.access_token")
- `refreshEvery` (required): Fixed refresh interval (e.g., "30m", "1h")
- `kvKey` (optional): KV bucket key (defaults to provider ID)

## Environment Variables

Use `${VAR_NAME}` syntax in configuration for secure credential management:

```yaml
clientId: "${STRIPE_CLIENT_ID}"
clientSecret: "${STRIPE_CLIENT_SECRET}"
body: |
  {
    "username": "${API_USER}",
    "password": "${API_PASS}"
  }
```

**Important**: If an environment variable is not set, it will be replaced with an empty string. The application will log a warning but continue running.

## Configuration Reference

### NATS Section

```yaml
nats:
  urls: ["nats://localhost:4222"]
  
  # Choose one authentication method:
  credsFile: "/etc/nats/rule-router.creds"
  # username: "auth-manager"
  # password: "secret"
  # token: "mytoken"
  # nkey: "seed..."
  
  # Optional TLS
  tls:
    enable: true
    certFile: "/etc/ssl/nats/client-cert.pem"
    keyFile: "/etc/ssl/nats/client-key.pem"
    caFile: "/etc/ssl/nats/ca.pem"
    insecure: false
```

### Storage Section

```yaml
storage:
  bucket: "tokens"      # KV bucket name (required)
  keyPrefix: ""         # Optional key prefix
```

### Logging Section

```yaml
logging:
  level: info           # debug | info | warn | error
  encoding: json        # json | console
```

### Metrics Section

```yaml
metrics:
  enabled: true
  address: ":2113"
```

## Token Access Patterns

Tokens are stored in NATS KV using the `kvKey` from provider configuration. Access them in rules using:

```yaml
# Direct token access
headers:
  Authorization: "Bearer {@kv.tokens.stripe-api}"

# With nested JSON path (if token is JSON)
headers:
  Authorization: "Bearer {@kv.tokens.my-api:access_token}"
```

## Monitoring

### Logs

Structured JSON logs include:
- Authentication success/failure events
- Token refresh schedules
- Provider configuration details
- Error messages with context

### Metrics

Prometheus metrics available at `:2113/metrics` (if enabled):
- Authentication success/failure counts
- Token refresh timing
- Provider health status

## Troubleshooting

### Token Not Found

**Problem**: Rules report `@kv.tokens.my-api` not found

**Solutions**:
1. Check auth-manager logs for authentication failures
2. Verify KV bucket exists: `nats kv ls`
3. Check token is stored: `nats kv get tokens my-api`
4. Verify provider `kvKey` matches rule reference

### Authentication Failures

**Problem**: Provider authentication failing

**Solutions**:
1. Verify environment variables are set: `echo $CLIENT_ID`
2. Check credentials are correct
3. Test authentication manually using curl
4. Review auth-manager debug logs: `level: debug`

### Token Refresh Issues

**Problem**: Tokens not refreshing

**Solutions**:
1. Check refresh interval configuration
2. Verify auth-manager is running
3. Look for errors in logs
4. Ensure NATS connection is stable

## Best Practices

1. **Use .creds Files**: Preferred NATS authentication method for production
2. **Environment Variables**: Never hardcode secrets in config files
3. **Separate KV Buckets**: Consider separate buckets for dev/staging/prod
4. **Monitor Logs**: Set up alerts for authentication failures
5. **Test Manually**: Use `nats kv get tokens <key>` to verify tokens
6. **Refresh Buffer**: Set `refreshBefore` to at least 5 minutes for OAuth2
7. **Single Instance**: Only run one auth-manager instance per environment

## Examples

### Example 1: Stripe API

```bash
export STRIPE_CLIENT_ID="ca_xxx"
export STRIPE_CLIENT_SECRET="sk_test_xxx"

# config.yaml
providers:
  - id: "stripe"
    type: oauth2
    tokenUrl: "https://connect.stripe.com/oauth/token"
    clientId: "${STRIPE_CLIENT_ID}"
    clientSecret: "${STRIPE_CLIENT_SECRET}"
    scopes: ["read_write"]
    refreshBefore: "5m"
    kvKey: "stripe-api"

# In http-gateway rule:
action:
  http:
    url: "https://api.stripe.com/v1/charges"
    headers:
      Authorization: "Bearer {@kv.tokens.stripe-api}"
```

### Example 2: Pocketbase Admin

```bash
export PB_EMAIL="admin@example.com"
export PB_PASSWORD="secretpassword"

# config.yaml
providers:
  - id: "pocketbase"
    type: custom-http
    authUrl: "https://pb.example.com/api/collections/users/auth-with-password"
    method: POST
    headers:
      Content-Type: "application/json"
    body: |
      {
        "identity": "${PB_EMAIL}",
        "password": "${PB_PASSWORD}"
      }
    tokenPath: "token"
    refreshEvery: "30m"
    kvKey: "pocketbase-admin"

# In rule-router rule:
action:
  nats:
    subject: "webhooks.pocketbase"
    headers:
      X-Admin-Token: "{@kv.tokens.pocketbase-admin}"
```

## Security Considerations

1. **Credential Storage**: Use environment variables or secret management systems
2. **NATS Authentication**: Always use .creds files or NKeys in production
3. **TLS**: Enable TLS for NATS connections in production
4. **Token Visibility**: Tokens in KV buckets are visible to all NATS clients with access
5. **Least Privilege**: Grant only necessary OAuth2 scopes
6. **Audit Logs**: Monitor authentication events and token access patterns

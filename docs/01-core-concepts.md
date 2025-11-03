# Core Concepts

All applications in this repository are configured using a shared rule syntax. A **rule** is a YAML object composed of a `trigger`, optional `conditions`, and an `action`.

```yaml
# A rule is defined by a trigger, optional conditions, and an action.
- trigger:
    # ... defines what starts the rule evaluation (e.g., a NATS message or an HTTP request)
  conditions:
    # ... defines the logic to determine if the action should run (e.g., field value checks)
  action:
    # ... defines what to do if the conditions pass (e.g., publish a NATS message or make an HTTP call)
```

## 1. Triggers (The "If")

A trigger defines the event that initiates a rule evaluation.

**NATS Trigger**: Evaluates a message from a NATS subject.
```yaml
trigger:
  nats:
    subject: "sensors.temperature.>" # Supports wildcards
```

**HTTP Trigger**: Evaluates an incoming HTTP request.
```yaml
trigger:
  http:
    path: "/webhooks/github"   # Exact path match
    method: "POST"             # Optional, defaults to all methods
```

## 2. Conditions (The "When")

Conditions are an optional block of logic that must evaluate to `true` for the action to be executed.

```yaml
conditions:
  operator: and # or "or"
  items:
    # Check a field in the JSON payload
    - field: "temperature"
      operator: gte
      value: 30
    # Check an HTTP header
    - field: "@header.X-Device-Auth"
      operator: "exists"
    # Check the time of day
    - field: "@time.hour"
      operator: gte
      value: 9
    # Check a value from a NATS KV store
    - field: "@kv.device_status.{device_id}:status"
      operator: "eq"
      value: "active"
```

## 3. Actions (The "Then")

An action defines the work to be done when a rule's conditions are met.

**NATS Action**: Publishes a new message to a NATS subject.
```yaml
action:
  nats:
    subject: "alerts.high_temp.{device_id}"
    payload: |
      {
        "alert": "High temperature detected!",
        "temp": {temperature},
        "device": "{device_id}",
        "timestamp": "{@timestamp()}"
      }
```

**HTTP Action**: Makes an outbound HTTP request to an external service.
```yaml
action:
  http:
    url: "https://api.pagerduty.com/incidents"
    method: "POST"
    headers:
      Authorization: "Token ${PAGERDUTY_TOKEN}" # Env vars supported
    payload: '{"service": "app-alerts", "message": "{alert_message}"}'
    retry:
      maxAttempts: 3
      initialDelay: "1s"
```

## Environment Variables

The rule engine supports environment variable expansion for static configuration values using `${VAR_NAME}` syntax. This enables secure secret management and environment-specific configuration without hardcoding values in rule files.

### How It Works

Environment variables are expanded **at load time** (when rules are parsed from YAML files), not at runtime. This means:

- ✅ **Performance**: Zero runtime overhead - values are substituted once during startup
- ✅ **Security**: Secrets are never stored in rule files
- ✅ **Simplicity**: Standard environment variable management (Docker, K8s, systemd, etc.)
- ✅ **Validation**: Expanded values are validated along with the rest of the rule

**Important**: Environment variable expansion is completely separate from template variable substitution:
- `${ENV_VAR}` → Expanded at **load time** (static configuration)
- `{field}` or `{@system}` → Resolved at **runtime** (per-message templating)

### Syntax

```yaml
# Use ${VARIABLE_NAME} anywhere in your rules
action:
  http:
    url: "https://api.example.com"
    headers:
      Authorization: "Bearer ${API_TOKEN}"
```

### Where Can I Use Them?

Environment variables can be used in **both conditions and actions**:

#### In Actions

**NATS Actions:**
```yaml
action:
  nats:
    subject: "alerts.${ENVIRONMENT}.critical"
    payload: |
      {
        "apiKey": "${SERVICE_API_KEY}",
        "region": "${AWS_REGION}"
      }
    headers:
      X-Service-Token: "${INTERNAL_TOKEN}"
```

**HTTP Actions:**
```yaml
action:
  http:
    url: "${API_BASE_URL}/incidents"
    method: "POST"
    headers:
      Authorization: "Token ${PAGERDUTY_TOKEN}"
      X-Environment: "${DEPLOY_ENV}"
    payload: '{"service": "${SERVICE_NAME}"}'
```

#### In Conditions

Environment variables can also be used in condition values:

```yaml
conditions:
  operator: and
  items:
    # Check if status matches expected value from env
    - field: "status"
      operator: eq
      value: "${EXPECTED_STATUS}"
    
    # Check if environment matches
    - field: "environment"
      operator: in
      value: ["${PRIMARY_ENV}", "${SECONDARY_ENV}"]
```

### Missing Variables

If an environment variable is not set, the system will:

1. **Log a warning** with details about the missing variable
2. **Substitute with an empty string**
3. **Continue loading** the rule (non-fatal)

**Best Practice**: Always set required environment variables before starting the application, or the rule may not work as intended.

# Array Processing

The rule engine provides powerful array processing capabilities for handling batch messages. This is essential when third-party systems send multiple events in a single message, or when you need to check if any element in an array matches specific criteria.

## Array Operators in Conditions

Use array operators to check if a message is relevant by inspecting array contents.

**Available Operators:**
- `any`: At least one array element matches nested conditions
- `all`: All array elements match nested conditions
- `none`: No array elements match nested conditions

**Example**: Check if any notification is critical
```yaml
conditions:
  operator: and
  items:
    - field: "type"
      operator: eq
      value: "BATCH_NOTIFICATION"
    
    # Check if ANY notification in the array is critical
    - field: "notifications"
      operator: any
      conditions:
        operator: and  # Required for nested conditions
        items:
          - field: "severity"
            operator: eq
            value: "critical"
```

**How it works:**
- Iterates through the `notifications` array
- Evaluates nested conditions against each element
- Short-circuits on first match for `any` (performance optimization)
- Returns `true` if the operator condition is satisfied

## ForEach Actions

Generate **one action per array element** using `forEach`. This is the key feature for batch processing.

**Basic Syntax:**
```yaml
action:
  nats:
    forEach: "notifications"   # Path to array in message
    subject: "alerts.{id}"
    payload: '{"id": "{id}", "message": "{message}"}'
```

**With Filter** (recommended):
```yaml
action:
  nats:
    forEach: "notifications"
    filter:                     # Only process elements matching these conditions
      operator: and             # Required
      items:
        - field: "severity"
          operator: eq
          value: "critical"
    subject: "alerts.critical.{id}"
    payload: |
      {
        "id": "{id}",
        "message": "{message}",
        "severity": "{severity}"
      }
```

**What happens:**
1. Extracts the `notifications` array from the message
2. Applies `filter` conditions to each element (if specified)
3. For each matching element, generates one action
4. Templates subject/payload using fields from that element

## Template Context: The `@msg` Prefix

When using `forEach`, template variables can refer to either:
- **Array element fields**: Use `{fieldName}` directly
- **Root message fields**: Use `{@msg.fieldName}` explicitly

| Context | `{field}` resolves to | `{@msg.field}` resolves to |
|---------|----------------------|---------------------------|
| Normal action (no forEach) | Root message field | Root message field (explicit) |
| ForEach action | Current array element field | Root message field (explicit) |

**Example:**
```yaml
action:
  nats:
    forEach: "alerts"
    subject: "alerts.{alertId}"
    payload: |
      {
        "alertId": "{alertId}",              # From alerts[i]
        "severity": "{severity}",            # From alerts[i]
        "deviceId": "{@msg.deviceId}",       # From root message
        "timestamp": "{@msg.receivedAt}",    # From root message
        "processedAt": "{@timestamp()}"      # System function
      }
```

## Complete Example: Batch Notification Processing

**Scenario**: Security system sends batch motion alerts. Generate one alert per camera.

**Rule:**
```yaml
- trigger:
    nats:
      subject: "security.notifications"
    
  conditions:
    operator: and
    items:
      # Check message type
      - field: "type"
        operator: eq
        value: "MOTION_BATCH"
      
      # Check if ANY alert is from a camera we care about
      - field: "alerts"
        operator: any
        conditions:
          operator: and
          items:
            - field: "deviceType"
              operator: eq
              value: "camera"
  
  action:
    nats:
      # Generate one alert per camera motion event
      forEach: "alerts"
      filter:
        operator: and
        items:
          - field: "deviceType"
            operator: eq
            value: "camera"
          - field: "motionDetected"
            operator: eq
            value: true
      subject: "alerts.motion.{buildingId}.{cameraId}"
      payload: |
        {
          "cameraId": "{cameraId}",
          "location": "{location}",
          "motionDetected": true,
          "timestamp": "{timestamp}",
          "buildingId": "{@msg.buildingId}",
          "batchId": "{@msg.batchId}",
          "processedAt": "{@timestamp()}"
        }
      headers:
        X-Alert-Type: "motion"
        X-Building-Id: "{@msg.buildingId}"
```

## Performance & Limits

**Default Limits:**
- Maximum 100 iterations per forEach (configurable)
- Prevents resource exhaustion from malicious/malformed messages
- Configure via `forEach.maxIterations` in config file

**Configuration:**
```yaml
forEach:
  maxIterations: 100  # Maximum array elements to process
```

## Best Practices

✅ **DO:**
- Use `filter` to limit iterations
- Use array operators in conditions to pre-filter messages
- Use `@msg` prefix explicitly when accessing root message fields
- Test with empty arrays and non-matching elements

❌ **DON'T:**
- Process unbounded arrays without limits
- Duplicate logic between array operators and forEach filters
- Assume all array elements are objects (primitives are supported via `@value`)
- Forget that `{field}` resolves to array element in forEach context

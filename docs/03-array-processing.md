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
    - field: "{type}"                    # ✅ NEW: Explicit template syntax
      operator: eq
      value: "BATCH_NOTIFICATION"
    
    # Check if ANY notification in the array is critical
    - field: "{notifications}"           # ✅ NEW: Explicit template syntax
      operator: any
      conditions:
        operator: and  # Required for nested conditions
        items:
          - field: "{severity}"          # ✅ NEW: Explicit template syntax
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

### NEW: Consistent Brace Syntax

**All variable references now use `{braces}` for consistency:**
- Condition fields: `{field}` ✅
- Condition values: `{field}` or literal ✅
- ForEach fields: `{arrayField}` ✅ **NEW!**
- Action subjects: `{field}` ✅
- Action payloads: `{field}` ✅

**Basic Syntax:**
```yaml
action:
  nats:
    forEach: "{notifications}"           # ✅ NEW: Braces required!
    subject: "alerts.{id}"
    payload: '{"id": "{id}", "message": "{message}"}'
```

**With Filter** (recommended):
```yaml
action:
  nats:
    forEach: "{notifications}"           # ✅ NEW: Braces required!
    filter:                              # Only process elements matching these conditions
      operator: and                      # Required
      items:
        - field: "{severity}"            # ✅ Explicit template syntax
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
    forEach: "{alerts}"                  # ✅ NEW: Braces required!
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

## ForEach with Variable Comparisons (NEW!)

You can now use variable comparisons in forEach filters:

```yaml
# Message: {"min_value": 50, "readings": [{"value": 30}, {"value": 75}, {"value": 90}]}
action:
  nats:
    forEach: "{readings}"                # ✅ Braces required!
    filter:
      operator: and
      items:
        - field: "{value}"               # ✅ Element field
          operator: gt
          value: "{@msg.min_value}"      # ✅ Compare to root message field!
    subject: "sensors.high-reading.{@uuid7()}"
    payload: |
      {
        "value": {value},
        "threshold": "{@msg.min_value}",
        "exceeded_by": {value} - {min_value}
      }
```

**With KV Lookups:**
```yaml
# KV: thresholds["sensor-batch"] = {"min": 10, "max": 100}
action:
  nats:
    forEach: "{readings}"                # ✅ Braces required!
    filter:
      operator: and
      items:
        - field: "{value}"               # ✅ Element field
          operator: gte
          value: "{@kv.thresholds.{@msg.sensor_type}:min}"  # ✅ Dynamic KV threshold!
        - field: "{value}"
          operator: lte
          value: "{@kv.thresholds.{@msg.sensor_type}:max}"
    subject: "sensors.valid-reading.{@uuid7()}"
    payload: '...'
```

## Complete Example: Batch Notification Processing

**Scenario**: Security system sends batch motion alerts. Generate one alert per camera.

**Rule:**
```yaml
- trigger:
    nats:
      subject: security.notifications
    
  conditions:
    operator: and
    items:
      # Check message type
      - field: "{type}"                  # ✅ Explicit template syntax
        operator: eq
        value: "MOTION_BATCH"
      
      # Check if ANY alert is from a camera we care about
      - field: "{alerts}"                # ✅ Explicit template syntax
        operator: any
        conditions:
          operator: and
          items:
            - field: "{deviceType}"      # ✅ Explicit template syntax
              operator: eq
              value: "camera"
  
  action:
    nats:
      # Generate one alert per camera motion event
      forEach: "{alerts}"                # ✅ NEW: Braces required!
      filter:
        operator: and
        items:
          - field: "{deviceType}"        # ✅ Explicit template syntax
            operator: eq
            value: "camera"
          - field: "{motionDetected}"    # ✅ Explicit template syntax
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

## Primitive Array Elements

ForEach works with primitive arrays (strings, numbers) using the `{@value}` accessor:

**String Array:**
```yaml
# Message: {"action": "provision", "device_ids": ["device-001", "device-002"]}
action:
  nats:
    forEach: "{device_ids}"              # ✅ Braces required!
    subject: "devices.provision.{@value}"  # Access primitive with @value
    payload: |
      {
        "device_id": "{@value}",
        "action": "{@msg.action}",
        "timestamp": "{@timestamp()}"
      }
```

**Number Array with Filter:**
```yaml
# Message: {"sensor_id": "temp-001", "readings": [23.5, 24.1, 25.3, 26.0]}
action:
  nats:
    forEach: "{readings}"                # ✅ Braces required!
    filter:
      operator: and
      items:
        - field: "{@value}"              # Access primitive with @value
          operator: gt
          value: 25
    subject: "sensors.high-reading.{@msg.sensor_id}"
    payload: |
      {
        "sensor_id": "{@msg.sensor_id}",
        "reading": {@value},
        "timestamp": "{@timestamp()}"
      }
```

## Nested Array Paths

ForEach supports deeply nested array paths:

```yaml
# Message: {"data": {"sensors": {"readings": [{"value": 10}]}}}
action:
  nats:
    forEach: "{data.sensors.readings}"   # ✅ Nested path with braces
    subject: "sensors.reading.{@uuid7()}"
    payload: '{"value": {value}}'
```

## Root Array (@items)

When the entire message is an array:

```yaml
# Message: [{"device": "dev1"}, {"device": "dev2"}]
action:
  nats:
    forEach: "{@items}"                  # ✅ Root array with braces
    subject: "devices.{device}.status"
    payload: '{"device": "{device}"}'
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
- Use braces for all variable references: `{field}`, `{@items}`, `{@msg.field}`
- Use `filter` to limit iterations
- Use array operators in conditions to pre-filter messages
- Use `{@msg}` prefix explicitly when accessing root message fields
- Test with empty arrays and non-matching elements

❌ **DON'T:**
- Forget braces on forEach: `forEach: "field"` ❌ Should be `forEach: "{field}"` ✅
- Process unbounded arrays without limits
- Duplicate logic between array operators and forEach filters
- Assume all array elements are objects (primitives need `{@value}`)
- Forget that `{field}` resolves to array element in forEach context

## Migration from Old Syntax

**Before (Old):**
```yaml
forEach: "notifications"      # ❌ No braces
filter:
  items:
    - field: severity         # ❌ No braces
      operator: eq
      value: "critical"
```

**After (New):**
```yaml
forEach: "{notifications}"    # ✅ Braces required!
filter:
  items:
    - field: "{severity}"     # ✅ Braces required!
      operator: eq
      value: "critical"
```

**Migration Script:**
```bash
# Automated migration for forEach fields
./scripts/migrate-conditions.sh ./rules
```

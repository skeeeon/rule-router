# Primitive & Array Root Messages

The rule engine supports **any valid JSON** as the root message or array elements, including primitives (strings, numbers, booleans) and arrays. This enables seamless integration with IoT protocols (like SenML), simple log messages, and batch operations with primitive arrays.

## How It Works

Messages are automatically wrapped to provide consistent field access:

| Message Type | Wrapped As | Access Pattern |
|--------------|------------|----------------|
| Object | `{"field": ...}` (unchanged) | `{field}` |
| Array | `{"@items": [...]}` | `@items` |
| String | `{"@value": "text"}` | `{@value}` |
| Number | `{"@value": 42}` | `{@value}` |
| Boolean | `{"@value": true}` | `{@value}` |
| Null | `{"@value": null}` | `{@value}` |

**Key Points:**
- Objects are **never wrapped** - they pass through unchanged for backward compatibility
- Wrapping is transparent - you don't need to think about it
- Use the `@` prefix convention you're already familiar with
- Works with both root messages and array elements

## Example 1: SenML Array at Root

SenML (Sensor Markup Language) is a common IoT format that sends arrays at the root.

**Message:**
```json
[
  {"n": "temperature", "v": 23.5, "u": "Cel"},
  {"n": "humidity", "v": 65.2, "u": "%RH"}
]
```

**Rule:**
```yaml
- trigger:
    nats:
      subject: "sensors.senml"
  
  conditions:
    operator: and
    items:
      # Check if ANY measurement is temperature
      - field: "@items"
        operator: any
        conditions:
          operator: and
          items:
            - field: "n"
              operator: eq
              value: "temperature"
  
  action:
    nats:
      forEach: "@items"
      filter:
        operator: and
        items:
          - field: "v"
            operator: gt
            value: 20
      subject: "sensors.{n}"
      payload: |
        {
          "metric": "{n}",
          "value": {v},
          "unit": "{u}",
          "timestamp": "{@timestamp()}"
        }
```

## Example 2: String Message at Root

Perfect for simple log aggregation or status messages.

**Message:**
```json
"ERROR: Database connection timeout"
```

**Rule:**
```yaml
- trigger:
    nats:
      subject: "logs.raw"
  
  conditions:
    operator: and
    items:
      - field: "@value"
        operator: contains
        value: "ERROR"
  
  action:
    nats:
      subject: "alerts.error"
      payload: |
        {
          "message": "{@value}",
          "level": "error",
          "timestamp": "{@timestamp()}"
        }
```

## Example 3: String Array Elements

Process lists of device IDs, usernames, or other primitive values.

**Message:**
```json
{
  "action": "provision",
  "deviceIds": ["device-001", "device-002", "device-003"]
}
```

**Rule:**
```yaml
- trigger:
    nats:
      subject: "devices.batch"
  
  action:
    nats:
      forEach: "deviceIds"
      subject: "provision.{@value}"
      payload: |
        {
          "deviceId": "{@value}",
          "action": "{@msg.action}",
          "timestamp": "{@timestamp()}"
        }
```

## Reserved Field Names

The `@value` and `@items` field names are reserved for wrapping:
- `@value`: Used for primitive values
- `@items`: Used for arrays at root

**Important:** These fields are **only added during wrapping**. Regular objects are never wrapped, so there's no conflict with user data:

```json
// This object is NEVER wrapped - passes through unchanged
{
  "@value": "user_data",
  "@items": [1, 2, 3],
  "normalField": "works fine"
}
```

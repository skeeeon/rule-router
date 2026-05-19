# Scheduler

The scheduler feature fires rules on a cron schedule rather than in response to a message. Enable with `features.scheduler: true` or `RR_FEATURES_SCHEDULER=true`.

Use cases:
- Polling external APIs that have no webhook channel
- Periodic publishing of heartbeats, summaries, or reports
- KV-driven fan-out (broadcast a command to a managed list of targets)

## Trigger

```yaml
- trigger:
    schedule:
      cron: "0 8 * * 1-5"             # Standard 5-field cron expression
      timezone: "America/New_York"     # Optional IANA timezone, defaults to system local
  action:
    # ... NATS or HTTP action
```

The cron expression uses the standard 5-field format (minute, hour, day-of-month, month, day-of-week).

## What's available in scheduler trigger context

Scheduler-triggered rules have **no incoming message**. This restricts what can appear in conditions and templates:

| Available | Not available |
|-----------|---------------|
| `{@time.*}`, `{@day.*}`, `{@date.*}`, `{@timestamp.*}` | Message fields (`{fieldName}`) |
| `{@kv.bucket.key}` lookups | `{@subject.*}` subject tokens |
| `{@timestamp()}`, `{@uuid4()}`, `{@uuid7()}` | `{@path.*}`, `{@method}` HTTP context |
| Environment variables (`${VAR}`) | `{@header.*}` headers |

The full variable reference is in [04 System Variables](./04-system-variables.md). The scheduler-relevant subset is **time and date**, **KV**, **environment**, and **template functions**.

## Actions

The scheduler supports both NATS and HTTP actions.

### NATS action

```yaml
- trigger:
    schedule:
      cron: "*/10 * * * *"
  action:
    nats:
      subject: "heartbeat"
      payload: |
        {
          "ts": "{@timestamp.iso}",
          "id": "{@uuid7()}"
        }
```

### HTTP action

```yaml
- trigger:
    schedule:
      cron: "0 9 * * 1-5"
      timezone: "America/New_York"
  action:
    http:
      url: "https://api.example.com/reports/daily"
      method: POST
      headers:
        Authorization: "Bearer ${API_TOKEN}"
      payload: '{"date": "{@date.iso}"}'
      retry:
        maxAttempts: 3
        initialDelay: "2s"
        maxDelay: "30s"
```

HTTP actions support the full retry/backoff machinery documented in [01 Core Concepts](./01-core-concepts.md).

## Poll-and-republish: `publishResponse`

An HTTP action's response can be republished to NATS, turning a poll-based API into an event-driven flow:

```yaml
- trigger:
    schedule:
      cron: "*/5 * * * *"
  action:
    http:
      url: "https://api.example.com/devices/status"
      method: GET
      headers:
        Authorization: "Bearer ${API_TOKEN}"
      publishResponse:
        subject: "poll.devices.status"
```

The response body is published on 2xx (capped at 1 MB). Downstream router rules subscribe to `poll.devices.status` and process it like any other event. See [09 Patterns — Polling-to-eventing bridge](./09-patterns.md#13-polling-to-eventing-bridge) for the full recipe.

## KV-sourced forEach: fan-out

Because there is no message, the natural source for a `forEach` array is a KV bucket. Update the bucket to change the fan-out targets — no rule changes required.

```yaml
# KV: config["door_list"] = [{"id": "front"}, {"id": "back"}]
- trigger:
    schedule:
      cron: "0 8 * * 1-5"
  action:
    nats:
      forEach: "{@kv.config.door_list}"
      subject: "access.door.{id}.command"
      payload: '{"command": "unlock", "id": "{@uuid7()}"}'
```

Full details in [05 Array Processing — KV-Sourced Arrays](./05-array-processing.md#kv-sourced-arrays).

## Conditions

Schedule rules can use conditions, though they evaluate against time and KV context only:

```yaml
- trigger:
    schedule:
      cron: "0 * * * *"
  conditions:
    operator: and
    items:
      # Only during business hours
      - field: "{@time.hour}"
        operator: gte
        value: 9
      - field: "{@time.hour}"
        operator: lt
        value: 17
      # Only if the maintenance flag is unset
      - field: "{@kv.config.maintenance:active}"
        operator: neq
        value: true
  action:
    # ...
```

The condition acts as a gate on the cron firing — useful when you want a coarse cron expression and finer-grained runtime filtering.

## Hot reload with KV rule store

When [KV rule storage](./08-kv-rule-store.md) is enabled, scheduler behavior under hot-reload is to **rebuild the cron job set**:

1. All KV-loaded cron jobs are removed.
2. New jobs are registered from the updated rule set.
3. File-loaded jobs (if any are still in use) are untouched.
4. Jobs mid-execution are not interrupted.

KV-loaded jobs are tagged internally (`kv-rule`) so the rebuild affects only them. There is no restart and no gap in execution — the next firing of every active rule continues on schedule.

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

### Sub-minute schedules

The seconds field is optional. Supply a **6-field** expression and the leading field is seconds:

```yaml
- trigger:
    schedule:
      cron: "*/5 * * * * *"     # every 5 seconds
      timezone: "America/New_York"   # still honoured
```

| Expression | Fires |
|---|---|
| `*/5 * * * *` | every 5 **minutes** (5 fields) |
| `*/5 * * * * *` | every 5 **seconds** (6 fields) |
| `* * * * * *` | every second — the fastest expressible schedule |
| `30 * * * * *` | at 30 seconds past every minute |

Descriptors (`@hourly`, `@daily`, `@every 1h30m`) are also accepted.

**One second is the floor.** Cron cannot express anything shorter, so there is no separate limit to configure — if you need finer granularity than that, a cron trigger is the wrong tool.

> **Watch the field count.** `*/5 * * * *` and `*/5 * * * * *` differ by one character and by a factor of 60, and both are valid, so no validator can flag the typo. Two things help:
>
> - The scheduler logs the resolved cadence for every rule at registration — look for `interval` in the `registered schedule rule` line:
>   ```
>   registered schedule rule cron="*/5 * * * * *" nextRun=2026-09-01T14:32:05Z interval=5s
>   ```
> - The web rule builder shows a plain-English description under the cron input ("Every 5 seconds"). Note that a 6-field expression is edited on the builder's **Advanced** tab — the Simple visual editor only round-trips 5-field expressions and shows an "unsupported" notice otherwise. The description and next-run preview work on both tabs.

### Overlapping fires

Each rule runs as a singleton: if a fire comes due while the previous one is still running, the new fire is **dropped**, not queued. This is what stops a slow HTTP action from stacking up jobs faster than they complete, and it matters much more at second granularity — a rule on `*/1 * * * * *` whose action takes two seconds loses roughly half its fires.

Dropped fires are observable rather than silent:

- The first drop for a given schedule logs a warning (later drops for that schedule are not logged, to keep a fast rule from flooding the log).
- `scheduler_job_runs_total{cron, status}` counts every outcome; dropped fires appear as `status="singleton_rescheduled"`.
- `scheduler_job_duration_seconds{cron}` records how long fires actually take — compare it against the interval to confirm the action is the cause.

See [12 Observability](./12-observability.md) for the full metric list.

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

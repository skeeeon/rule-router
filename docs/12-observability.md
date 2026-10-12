# Metrics & Observability

`rule-router` exposes Prometheus metrics, structured logs, and basic HTTP health endpoints, and announces itself on NATS service discovery. This page is the canonical catalogue of every metric the application emits, plus the health-check surface and example queries/alerts.

For the config knobs referenced here, see [11 Configuration — metrics](./11-configuration.md#metrics).

---

## Metrics endpoint

```yaml
metrics:
  enabled: true
  address: :2112
  path: /metrics
  updateInterval: 15s
```

- The endpoint serves the standard Prometheus exposition format with **OpenMetrics** enabled.
- All features (router, gateway, scheduler) running in one process share **one** metrics endpoint.
- `updateInterval` controls only how often the system gauges (`process_goroutines`, `process_memory_bytes`) are refreshed; counters and request histograms update inline as events occur.

> **The metrics endpoint is unauthenticated.** Bind it to an internal interface or scrape it through your cluster's network policy — don't expose `:2112` to the public internet.

A label-cardinality note up front: Prometheus `CounterVec`/`HistogramVec` series **don't appear in `/metrics` until their first observation with a given label set**. Plain counters, gauges, and histograms appear immediately at zero. So a freshly started process won't list, e.g., `messages_total{status="error"}` until the first error occurs.

---

## Health checks

| Endpoint | Where | Behavior |
|----------|-------|----------|
| `/health` | Metrics server (**all features**, default `:2112`) | **Liveness.** Always returns `200 {"status":"healthy"}` once the process is up — does not reflect NATS connectivity. |
| `/ready` | Metrics server (**all features**, default `:2112`) | **Readiness.** Returns `200 {"status":"ready"}` only once startup finished (broker connected, initial KV rule sync done) **and** the NATS connection is currently live; otherwise `503 {"status":"not ready"}`. Flips back to `503` on a NATS disconnect. |
| `/health`, `/healthz` | Inbound HTTP server (**gateway feature only**, default `:8080`) | Kept for backward compatibility. Same always-`200` liveness signal as the metrics-port `/health`. |

The metrics-port `/health` and `/ready` are the recommended probe targets for **all** feature combinations (router-only, scheduler-only, gateway, or any mix) — wire them to k8s `livenessProbe` and `readinessProbe` respectively.

> **Note:** `/health` and `/ready` live on the metrics server, so they require `metrics.enabled: true` (set in the shipped config files; the built-in default is `false`). With metrics disabled there is no HTTP probe target on router-only or scheduler-only deployments.

> **TLS:** health and metrics endpoints, like the inbound gateway, are plain HTTP. Terminate TLS at a proxy/load balancer — see [02 Gateway — TLS termination](./02-gateway.md#tls--https-termination).

---

## Service discovery

Every `rule-router` process registers on the NATS micro discovery protocol (`$SRV`) as `rule-router`, so one query lists every running instance, whatever its features:

```bash
nats micro ls
nats micro info rule-router
```

It is identity only, with no endpoints. `$SRV.PING`, `$SRV.INFO`, and `$SRV.STATS` report the name, the version (a build without a version stamp reports `0.0.0-dev`), the start time, and two metadata keys: `host` (the hostname) and `features` (the enabled features, e.g. `router,gateway`). A SIGHUP reload replaces the NATS connection, so the instance re-registers with a new id and start time. The `nats-auth-manager` binary does not register.

**Permissions.** The credential needs to subscribe to `$SRV.>` and to publish replies to the requester's inbox (`_INBOX.>`, or `allow_responses`). Discovery is never fatal: without the permission the process runs normally and is simply not listed, but the server's refusals are logged as `NATS asynchronous error` lines at startup and after every reconnect, and counted in `nats_async_errors_total{kind="permissions_violation"}`. Either grant `$SRV.>` or expect them.

---

## Metric catalogue

Types: **C** = Counter, **G** = Gauge, **H** = Histogram. "Feature" indicates which feature(s) emit the metric; *all* means router, gateway, and scheduler.

### Message processing

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `messages_total` | C | `status` = `received` \| `processed` \| `error` | router, gateway | Messages handled, by outcome. |
| `rule_matches_total` | C | `trigger` = NATS subject \| HTTP path \| `schedule:<cron>` | all | Rule matches by trigger, counted once per action a matching rule produces — a `forEach` fan-out of N counts N. Rules sharing a trigger aggregate together (there is no per-rule name). |
| `rules_active` | G | — | all | Number of rules currently loaded (updates on reload). The scheduler sets it to its own schedule-rule count, so in a process that also runs the router or gateway the value depends on which feature set it last. |
| `actions_total` | C | `status` = `success` \| `error` \| `skipped` | all | Actions executed, by outcome. `skipped` = an HTTP action matched on the router but `features.gateway` is disabled, so nothing executed it. |
| `actions_by_type_total` | C | `type` = `templated` \| `passthrough` \| `merge` | all | Actions by payload mode. |
| `action_publish_failures_total` | C | — | all | NATS publish failures for actions. The scheduler also counts failed HTTP actions and recovered job panics here. |
| `template_operations_total` | C | `status` = `success` \| `error` | all | Action-evaluation outcomes: `success` once per action produced, `error` once per matched rule whose action failed to evaluate (for example a `forEach` field that is missing, is not an array, or is longer than `forEach.maxIterations`). |

### NATS connection

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `nats_connection_status` | G | — | all | `1` = connected, `0` = disconnected — but it is only set by connection *events*: it reads `0` from startup until the first disconnect/reconnect cycle, even while connected. Until then use `/ready` for live connectivity. |
| `nats_reconnects_total` | C | — | all | Cumulative reconnect events. |
| `nats_async_errors_total` | C | `kind` | all | Errors NATS reports outside any request. `kind="slow_consumer"`: a subscription fell behind and messages were **dropped** — on a core subject, usually a rule whose actions are too slow (see [Core concepts](01-core-concepts.md)). `kind="permissions_violation"`: the account may not publish or subscribe on a subject; a denied publish never acks, so this is the only signal. `kind="other"`: everything else. Any non-zero value deserves a look. |

### Key-Value cache

Emitted when the KV local cache is enabled (`kv.localCache.enabled`, on by default when `kv.enabled`). The cache is lazily populated on first read and kept fresh by the KV watcher.

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `kv_cache_hits_total` | C | — | all (KV-enabled) | Lookups served from the local cache. |
| `kv_cache_misses_total` | C | — | all (KV-enabled) | Lookups that fell through to a NATS KV fetch. |
| `kv_cache_size` | G | — | all (KV-enabled) | Current number of cached entries across all buckets. |

Hits/misses are only counted while the cache is enabled; a disabled cache emits neither.

### Signature verification

Emitted only when `security.verification.enabled` and a rule actually references a `{@signature.*}` variable (verification is lazy — one attempt per message). Skipped messages (verification disabled, or signature headers absent) are **not** counted.

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `signature_verifications_total` | C | `result` = `valid` \| `invalid` | router, gateway | Attempted verifications by outcome. `invalid` covers signature mismatch and malformed key/signature input. |
| `signature_verification_duration_seconds` | H | — | router, gateway | Time for one verification attempt (base64 decode + key parse + Ed25519 verify). |

### Array & forEach processing

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `foreach_iterations_total` | C | `rule_file` | router, gateway, scheduler | Array elements visited. |
| `foreach_filtered_total` | C | `rule_file` | same | Elements dropped by the `filter` block. |
| `foreach_actions_generated_total` | C | `rule_file` | same | Actions actually emitted (visited − filtered − errors). |
| `foreach_element_errors_total` | C | `rule_file`, `error_type` | same | Per-element processing errors (see error types below). |
| `foreach_duration_seconds` | H | `rule_file` | same | Time spent processing a `forEach` action. |
| `array_operator_evaluations_total` | C | `operator` = `any`\|`all`\|`none`, `result` = `true`\|`false` | all | Array-operator evaluations in conditions. |

Despite its name, the `rule_file` label on the `foreach_*` metrics carries the **action type** — `nats` or `http` — not the rule's file, so these series aggregate every `forEach` rule of that type. An array longer than `forEach.maxIterations` fails before iteration and appears in none of them (see `template_operations_total{status="error"}`).

`error_type` values for `foreach_element_errors_total`: `marshal_failed`, `merge_payload_failed`, `template_payload_failed`, `template_headers_failed`, `template_subject_failed`, `template_url_failed`, `template_method_failed`, `template_publish_response_subject_failed`, `action_build_failed`.

### Throttle

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `throttle_suppressed_total` | C | `phase` = `trigger` \| `action` | all | Messages dropped by a **leading**-mode per-rule throttle, by phase (schedule rules can only throttle the action). |
| `throttle_deferred_total` | C | `outcome` = `coalesced` \| `emitted` \| `dropped` \| `error` | router, gateway, scheduler | **Trailing**-mode throttle lifecycle. |

`outcome` values for `throttle_deferred_total`:

| Value | Meaning |
|-------|---------|
| `coalesced` | A pending batch was replaced by a newer one inside its window — this is the "value discarded because a later one arrived" counter. |
| `emitted` | An action fired at window close (or during a shutdown flush). Counted per action, so a `forEach` batch of 3 increments by 3. |
| `dropped` | A pending batch was discarded: submitted after shutdown began, or still unflushed when the shutdown grace period expired. Non-zero here means lost actions. |
| `error` | A deferred action failed to execute. There is no redelivery path at this point — the trigger was ACKed when the window opened. |

Useful derived signals:

- `rate(throttle_suppressed_total{phase="trigger"}[5m])` climbing on a rule that *has* conditions is a smell: a trigger throttle samples the input stream and may be discarding messages that would have matched. See [Core Concepts](./01-core-concepts.md#trigger-throttle--read-this-before-using-it).
- `coalesced / (coalesced + emitted)` is how much churn trailing mode is absorbing. Near zero means the window is doing nothing and can be removed.
- Any sustained `dropped` means shutdowns are losing settled values — raise the shutdown grace period or reconsider trailing mode for that rule.

### Scheduler

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `scheduler_job_runs_total` | C | `cron`, `status` = `success` \| `fail` \| `singleton_rescheduled` | scheduler | Scheduled rule fires by outcome. `singleton_rescheduled` means a fire was **dropped** because the previous run of that rule had not finished. |
| `scheduler_job_duration_seconds` | H | `cron` | scheduler | Wall-clock duration of a scheduled rule fire. |

Each rule runs as a singleton, so a fire that comes due while the previous one is still running is discarded rather than queued. That is deliberate — it stops a slow action from stacking up jobs — but it becomes easy to hit with [sub-minute schedules](./03-scheduler.md#sub-minute-schedules), where an action slower than the interval quietly loses most of its fires. The first drop per schedule also logs a warning; subsequent drops are counted here only.

Useful derived signals:

- `rate(scheduler_job_runs_total{status="singleton_rescheduled"}[5m]) > 0` means a rule is firing faster than it can complete. Either lengthen the cron interval or speed up the action.
- `histogram_quantile(0.95, rate(scheduler_job_duration_seconds_bucket[5m])) by (cron)` against the `interval` in the rule's `registered schedule rule` startup log confirms the diagnosis.

> Both metrics label `cron` with the rule's raw cron expression, so cardinality is bounded by the number of distinct schedules in the rule set. Rules sharing an expression aggregate into one series.

### HTTP gateway — inbound

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `http_inbound_requests_total` | C | `path`, `method`, `status` | gateway | Inbound requests. `path` is the **matched rule route**, not the raw request path — a wildcard rule reports its pattern (see cardinality note). Unmatched paths use the sentinel `path="_unknown_"`. HMAC rejections appear here as `status="401"`. |
| `http_request_duration_seconds` | H | `path`, `method` | gateway | Inbound request latency. `path` is the matched rule route, as above. |
| `webhook_hmac_verifications_total` | C | `result` = `valid` \| `invalid` \| `missing` \| `expired` \| `error` | gateway | Inbound webhook [HMAC verifications](./02-gateway.md#verifying-webhook-signatures-hmac) by outcome, for paths whose rule declares an `hmac` block. `missing` = no signature header; `expired` = a timestamped scheme's signature matched but its timestamp is more than 5 minutes off (check the server clock first); `error` = misconfiguration (empty secret, unknown algorithm/encoding, unresolvable KV ref). Only `valid` proceeds; everything else returns `401`. |
| `message_processing_backlog` | G | — | gateway | Depth of the inbound webhook worker queue, sampled on enqueue/dequeue. Climbs toward `inboundQueueSize` when workers can't keep up (a full queue returns `503`). Gateway-only — router pulls from JetStream on demand and has no in-process backlog. |

### HTTP — outbound

Emitted whenever an HTTP *action* runs (gateway outbound rules and scheduler HTTP actions).

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `http_outbound_requests_total` | C | `status_code` | gateway, scheduler | Outbound HTTP calls, by response status. |
| `http_outbound_duration_seconds` | H | `status_code` | gateway, scheduler | Outbound call latency. |

> Outbound metrics are labeled by `status_code` **only** — outbound URLs are templated from message data and would be unbounded-cardinality as a label.

### System

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `process_goroutines` | G | — | all | Live goroutine count (refreshed every `updateInterval`). |
| `process_memory_bytes` | G | — | all | Allocated heap bytes (`runtime.MemStats.Alloc`). |

### Cardinality guidance

- `http_inbound_requests_total` and `http_request_duration_seconds` label `path` with the **rule route that matched**, not the raw request path. An exact-path rule reports its own path; a wildcard rule reports its pattern, so every URL under `/api/>` collapses into a single `path="/api/>"` series. Cardinality is therefore bounded by your rule count, and wildcards matching UUIDs or per-user paths are safe. If two wildcard rules match the same request, the first in load order names the label — all matching rules still fire, and per-rule attribution lives in `rule_matches_total{trigger}`.
- `404` responses are recorded as `path="_unknown_"` so path-scanning traffic cannot blow up cardinality.
- The `rule_file` label on the `foreach_*` metrics only ever takes two values (`nats`, `http`) — it is the action type, not the source file.

---

## Example PromQL

```promql
# Message throughput (per second, 5m window)
rate(messages_total{status="processed"}[5m])

# Error ratio
sum(rate(messages_total{status="error"}[5m]))
  / sum(rate(messages_total[5m]))

# Action failure rate
rate(actions_total{status="error"}[5m])

# KV local-cache hit ratio (cache effectiveness)
sum(rate(kv_cache_hits_total[5m]))
  / (sum(rate(kv_cache_hits_total[5m])) + sum(rate(kv_cache_misses_total[5m])))

# Inbound webhook backlog approaching the queue limit (gateway)
message_processing_backlog

# Inbound HTTP p95 latency by path
histogram_quantile(0.95,
  sum by (le, path) (rate(http_request_duration_seconds_bucket[5m])))

# Outbound HTTP non-2xx rate
sum(rate(http_outbound_requests_total{status_code!~"2.."}[5m]))

# Throttle suppression rate by phase
rate(throttle_suppressed_total[5m])

# forEach elements visited per second, NATS actions (rule_file is the action type)
rate(foreach_iterations_total{rule_file="nats"}[5m])
```

## Example alerts

```yaml
groups:
  - name: rule-router
    rules:
      # nats_connection_status reads 0 from startup until the first reconnect
      # (see the catalogue), so this also fires on a healthy instance that has
      # never reconnected. Probe /ready for live connectivity.
      - alert: RuleRouterNATSDisconnected
        expr: nats_connection_status == 0
        for: 1m
        labels: { severity: critical }
        annotations:
          summary: "rule-router lost its NATS connection"

      - alert: RuleRouterNATSAsyncErrors
        expr: increase(nats_async_errors_total[5m]) > 0
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "rule-router NATS async errors ({{ $labels.kind }}) — dropped messages or denied subjects"

      - alert: RuleRouterHighErrorRate
        expr: |
          sum(rate(messages_total{status="error"}[5m]))
            / sum(rate(messages_total[5m])) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "rule-router message error rate above 5%"

      - alert: RuleRouterActionPublishFailures
        expr: rate(action_publish_failures_total[5m]) > 0
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "rule-router is failing to publish action messages"
```

## Example scrape config

```yaml
scrape_configs:
  - job_name: rule-router
    static_configs:
      - targets: ["rule-router:2112"]
  - job_name: nats-auth-manager
    static_configs:
      - targets: ["nats-auth-manager:2113"]
```

---

## Logging

Logs are structured (slog-style key/value) via the `logging` config block ([reference](./11-configuration.md#logging)). The most useful fields when triaging:

| Field | Meaning |
|-------|---------|
| `subject` / `path` | The NATS subject or HTTP path of the trigger. |
| `ruleIndex` | Which rule was evaluated (`evaluating rule`, debug level) — its position among all loaded rules. |
| `field` / `operator` | A condition whose field could not be resolved (`failed to resolve condition field`, warn level). |
| `variable` | A template variable that rendered empty (debug level). |
| `kind` | The class of a `NATS asynchronous error` (`slow_consumer`, `permissions_violation`, `other`). |
| `error` | Underlying cause for a failed action. |

Set `logging.level: debug` temporarily for a rule that isn't firing: it shows which rules were evaluated, throttle suppressions, and template variables that did not resolve. Individual condition results are not logged. See [10 Troubleshooting](./10-troubleshooting.md).

---

## Auth manager metrics

The companion `nats-auth-manager` binary runs its own metrics server (default `:2113`, when `metrics.enabled`) with `authmgr_*` series (`authmgr_auth_success_total`, `authmgr_auth_failures_total`, `authmgr_auth_duration_seconds`, `authmgr_kv_store_failures_total`, all labeled by `provider`). These are documented in the [Auth Manager README](../cmd/nats-auth-manager/README.md); it is a separate process from `rule-router`.

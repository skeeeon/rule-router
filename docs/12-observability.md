# Metrics & Observability

`rule-router` exposes Prometheus metrics, structured logs, and basic HTTP health endpoints. This page is the canonical catalogue of every metric the application emits, plus the health-check surface and example queries/alerts.

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
| `/health`, `/healthz` | Inbound HTTP server (**gateway feature only**, default `:8080`) | Always returns `200 {"status":"healthy"}`. This is a **liveness** signal (process is up and serving HTTP), not a readiness check — it does not reflect NATS connectivity. |

There is currently **no dedicated HTTP health endpoint for router-only or scheduler-only deployments** (no inbound HTTP server runs). For those, use:

- **Process liveness** — scrape `:2112/metrics` returning `200`.
- **NATS readiness** — alert on the `nats_connection_status` gauge (`1` = connected). This is the most reliable "is it actually working" signal across all feature combinations.

> **TLS:** health and metrics endpoints, like the inbound gateway, are plain HTTP. Terminate TLS at a proxy/load balancer — see [02 Gateway — TLS termination](./02-gateway.md#tls--https-termination).

---

## Metric catalogue

Types: **C** = Counter, **G** = Gauge, **H** = Histogram. "Feature" indicates which feature(s) emit the metric; *all* means router, gateway, and scheduler.

### Message processing

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `messages_total` | C | `status` = `received` \| `processed` \| `error` | router, gateway | Messages handled, by outcome. |
| `rule_matches_total` | C | — | router, gateway | Times any rule's conditions matched. |
| `rules_active` | G | — | all | Number of rules currently loaded (updates on reload). |
| `actions_total` | C | `status` = `success` \| `error` | all | Actions executed, by outcome. |
| `actions_by_type_total` | C | `type` = `templated` \| `passthrough` \| `merge` | router, gateway | Actions by payload mode. |
| `action_publish_failures_total` | C | — | all | NATS publish failures for actions. |
| `template_operations_total` | C | `status` = `success` \| `error` | router, gateway | Template-evaluation outcomes. |

### NATS connection

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `nats_connection_status` | G | — | all | `1` = connected, `0` = disconnected. Primary readiness signal. |
| `nats_reconnects_total` | C | — | all | Cumulative reconnect events. |

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
| `foreach_iterations_total` | C | `rule_file` | router, gateway, scheduler | Array elements visited (after the `maxIterations` cap). |
| `foreach_filtered_total` | C | `rule_file` | same | Elements dropped by the `filter` block. |
| `foreach_actions_generated_total` | C | `rule_file` | same | Actions actually emitted (visited − filtered − errors). |
| `foreach_element_errors_total` | C | `rule_file`, `error_type` | same | Per-element processing errors (see error types below). |
| `foreach_duration_seconds` | H | `rule_file` | same | Time spent processing a `forEach` action. |
| `array_operator_evaluations_total` | C | `operator` = `any`\|`all`\|`none`, `result` = `true`\|`false` | router, gateway | Array-operator evaluations in conditions. |

`error_type` values for `foreach_element_errors_total`: `template_subject_failed`, `template_url_failed`, `template_method_failed`, `template_publish_response_subject_failed`.

### Throttle / debounce

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `throttle_suppressed_total` | C | `phase` = `trigger` \| `action` | router, gateway | Messages suppressed by per-rule debounce, by phase. |

### HTTP gateway — inbound

| Metric | Type | Labels | Feature | Description |
|--------|------|--------|---------|-------------|
| `http_inbound_requests_total` | C | `path`, `method`, `status` | gateway | Inbound requests. Unmatched paths use the sentinel `path="_unknown_"` (see cardinality note). HMAC rejections appear here as `status="401"`. |
| `http_request_duration_seconds` | H | `path`, `method` | gateway | Inbound request latency. |
| `webhook_hmac_verifications_total` | C | `result` = `valid` \| `invalid` \| `missing` \| `error` | gateway | Inbound webhook [HMAC verifications](./02-gateway.md#verifying-webhook-signatures-hmac) by outcome, for paths whose rule declares an `hmac` block. `missing` = no signature header; `error` = misconfiguration (empty secret, unknown algorithm/encoding, unresolvable KV ref). Only `valid` proceeds; everything else returns `401`. |
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

- `http_inbound_requests_total` and `http_request_duration_seconds` carry the real request `path` for **matched** routes. A wildcard rule (e.g. `/api/>`) that matches high-cardinality URLs (UUIDs, per-user paths) grows the series count. Scope wildcards tightly, or front the gateway with a proxy that normalizes paths.
- `404` responses are recorded as `path="_unknown_"` so path-scanning traffic cannot blow up cardinality.
- `rule_file` labels track the rule's source file (or KV key). This is bounded by your rule count and is safe.

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

# forEach truncation watch: iterations approaching the cap for a rule
rate(foreach_iterations_total{rule_file="sensors/batch.yaml"}[5m])
```

## Example alerts

```yaml
groups:
  - name: rule-router
    rules:
      - alert: RuleRouterNATSDisconnected
        expr: nats_connection_status == 0
        for: 1m
        labels: { severity: critical }
        annotations:
          summary: "rule-router lost its NATS connection"

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
| `rule_file` | Which rule file (or KV key) a message hit. |
| `subject` / `path` | The NATS subject or HTTP path of the trigger. |
| `condition_result` | Whether conditions passed (debug level). |
| `error` | Underlying cause for a failed action. |

Set `logging.level: debug` temporarily to log per-condition evaluation — the single most useful signal for a rule that isn't firing. See [10 Troubleshooting](./10-troubleshooting.md).

---

## Auth manager metrics

The companion `nats-auth-manager` binary runs its own metrics server (default `:2113`) with `authmgr_*` series (e.g. `authmgr_auth_success_total`, `authmgr_auth_failures_total`, both labeled by provider). These are documented in the [Auth Manager README](../cmd/nats-auth-manager/README.md); it is a separate process from `rule-router`.

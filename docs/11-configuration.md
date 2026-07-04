# Configuration Reference

This is the canonical reference for the `rule-router` application config — every section, field, default, and validation bound. Rules themselves are documented separately (see [01 Core Concepts](./01-core-concepts.md)); this page covers the *server* config that controls connections, features, and runtime behavior.

The companion `nats-auth-manager` binary uses its own config file (`config/auth-manager.yaml`); see the [Auth Manager README](../cmd/nats-auth-manager/README.md).

---

## How config is loaded

Config is resolved through [Viper](https://github.com/spf13/viper) by merging three sources, in increasing order of precedence:

1. **Config file** — YAML, passed with `--config` (e.g. `config/rule-router.yaml`).
2. **Environment variables** — prefix `RR_`, with `.` replaced by `_` (e.g. `RR_NATS_URLS`, `RR_HTTP_SERVER_ADDRESS`).
3. **Command-line flags** — where bound.

The canonical file is `config/rule-router.yaml`. The legacy per-feature files (`config/http-gateway.yaml`, `config/rule-scheduler.yaml`) are still valid — they are just the same schema with a different `features` block.

The config file is **optional**: if it doesn't exist, the app runs entirely from defaults plus environment variables. After merging, defaults are applied for any unset field and the whole config is validated; an invalid value aborts startup with a descriptive error.

### Environment variable overrides

`AutomaticEnv` only resolves keys Viper already knows about (i.e. keys present in the config file). To guarantee the most common keys are overridable **even when absent from the file**, they are explicitly bound:

```
features.router    features.gateway    features.scheduler
nats.urls          nats.username       nats.password      nats.token
logging.level
metrics.enabled    metrics.address     metrics.path
```

These always work via `RR_*` regardless of the file. Any other key is overridable only if it also appears in the config file. Examples:

```bash
RR_FEATURES_GATEWAY=true
RR_NATS_URLS="nats://a:4222,nats://b:4222"   # comma-separated → slice
RR_LOGGING_LEVEL=debug
RR_METRICS_ADDRESS=":9090"
```

---

## `features` — feature selection

Controls which of the three features run in this process. Multiple may be enabled at once; they share the NATS connection, rule engine, metrics endpoint, and KV state.

```yaml
features:
  router: true
  gateway: false
  scheduler: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `router` | bool | `true` *(see note)* | NATS-to-NATS routing. |
| `gateway` | bool | `false` | Bidirectional HTTP↔NATS integration. Enabling this starts the inbound HTTP server. |
| `scheduler` | bool | `false` | Cron-based scheduled publishing. |

> **Backward-compat default:** if **no** `features` block is present at all, `router` defaults to `true`. If you specify *any* feature, you must explicitly enable the ones you want. **At least one feature must be enabled** or startup fails.

---

## `nats` — connection & JetStream

```yaml
nats:
  urls:
    - nats://localhost:4222
  # Authentication — specify at most ONE method
  # username: "..."
  # password: "..."
  # token: "..."
  # nkeySeedFile: "/path/to/nkey.seed"
  # credsFile: "/path/to/user.creds"
  tls:
    enable: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `urls` | []string | `["nats://localhost:4222"]` | One or more NATS server URLs. Env override is comma-separated. At least one required. |
| `username` / `password` | string | — | User/password auth. |
| `token` | string | — | Token auth. |
| `nkeySeedFile` | string | — | Path to an NKey seed file. |
| `credsFile` | string | — | Path to a `.creds` file. Must exist at load time or startup fails. |

> **At most one** authentication method may be set (`username`, `token`, `nkeySeedFile`, `credsFile`). Setting more than one is a validation error. `password` is paired with `username` and does not count as a separate method.

### `nats.tls`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enable` | bool | `false` | Enable TLS for the NATS connection. |
| `certFile` | string | — | Client certificate (mutual TLS). If set, `keyFile` is required. |
| `keyFile` | string | — | Client key. If set, `certFile` is required. |
| `caFile` | string | — | CA bundle to verify the server. |
| `insecure` | bool | `false` | Skip server certificate verification. Development only. |

### `nats.consumers` — JetStream pull consumers

Used by the **router** and **gateway** features (for NATS-triggered rules).

```yaml
nats:
  consumers:
    consumerPrefix: "rule-router"
    workerCount: 4
    fetchBatchSize: 10
    fetchTimeout: 5s
    maxAckPending: 1000
    ackWaitTimeout: 30s
    maxDeliver: 3
    deliverPolicy: "new"
    replayPolicy: "instant"
```

| Field | Type | Default | Bounds / valid values | Description |
|-------|------|---------|------------------------|-------------|
| `consumerPrefix` | string | `"rule-router"` | — | Prefix for generated durable consumer names. Give each app a unique prefix when several share a stream. |
| `workerCount` | int | `2` | `1`–`1000` | Parallel workers per subscription. |
| `fetchBatchSize` | int | `1` | `1`–`10000` | Messages fetched per pull. |
| `fetchTimeout` | duration | `5s` | `> 0` | Pull fetch timeout. |
| `maxAckPending` | int | `1000` | `1`–`100000` | Max in-flight unacknowledged messages. Effective concurrency ceiling. |
| `ackWaitTimeout` | duration | `30s` | — | Time JetStream waits for an ACK before redelivering. |
| `maxDeliver` | int | `3` | `≥ 1` | Max delivery attempts before JetStream stops redelivering (see [Dead-letter behavior](#dead-letter-behavior)). |
| `deliverPolicy` | string | `"new"` | `all`, `new`, `last`, `by_start_time`, `by_start_sequence` | Where a new consumer starts reading. |
| `replayPolicy` | string | `"instant"` | `instant`, `original` | Replay pacing. |

### `nats.connection`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxReconnects` | int | `-1` | Reconnect attempts; `-1` = infinite. |
| `reconnectWait` | duration | `50ms` | Delay between reconnect attempts. |

### `nats.publish` — action publishing

Controls how NATS *actions* publish their output.

```yaml
nats:
  publish:
    mode: jetstream
    ackTimeout: 5s
    maxRetries: 3
    retryBaseDelay: 50ms
```

| Field | Type | Default | Valid values | Description |
|-------|------|---------|--------------|-------------|
| `mode` | string | `"jetstream"` | `jetstream`, `core` | `jetstream` = reliable (waits for stream ACK); `core` = fire-and-forget, lowest latency, no delivery guarantee. |
| `ackTimeout` | duration | `5s` | — | JetStream publish-ACK timeout (`jetstream` mode). |
| `maxRetries` | int | `3` | — | Publish retry attempts on failure (`jetstream` mode). |
| `retryBaseDelay` | duration | `50ms` | — | Base delay for publish-retry backoff. |

> **Per-rule override:** a NATS action may set its own `mode: jetstream | core` to override this global default for that action only (see [Core Concepts — NATS Action](./01-core-concepts.md#3-actions-the-then)). The other knobs (`ackTimeout`, `maxRetries`, `retryBaseDelay`) always come from this config. Similarly, a NATS *trigger* may set `mode: core` to consume via a plain core subscription instead of a JetStream consumer.

---

## `http` — gateway server & shared HTTP client

The `http` block is used when **gateway** is enabled (inbound server + outbound client) and when **scheduler** is enabled (outbound client only, for HTTP actions).

### `http.server` — inbound HTTP server (gateway only)

```yaml
http:
  server:
    address: ":8080"
    readTimeout: 30s
    writeTimeout: 30s
    idleTimeout: 120s
    maxHeaderBytes: 1048576
    shutdownGracePeriod: 30s
    inboundWorkerCount: 10
    inboundQueueSize: 1000
```

| Field | Type | Default | Bounds | Description |
|-------|------|---------|--------|-------------|
| `address` | string | `":8080"` *(when gateway enabled)* | required if gateway | Listen address. |
| `readTimeout` | duration | `30s` | `≥ 0` | Max time to read the request. |
| `writeTimeout` | duration | `30s` | `≥ 0` | Max time to write the response. **Must exceed the largest request/reply bridge `timeout`** or a slow reply gets cut off — see [02 Gateway](./02-gateway.md#requestreply-synchronous-responses-and-the-httpnats-bridge). |
| `idleTimeout` | duration | `120s` | — | Keep-alive idle timeout. |
| `maxHeaderBytes` | int | `1048576` (1 MB) | — | Max request header size. |
| `shutdownGracePeriod` | duration | `30s` | — | Time allowed to drain in-flight requests on shutdown. |
| `inboundWorkerCount` | int | `10` | `1`–`1000` | Workers draining the fire-and-forget webhook queue. |
| `inboundQueueSize` | int | `1000` | `1`–`100000` | Buffered webhook queue depth. A full queue returns `503` on non-synchronous routes. |

Inbound request bodies are hard-capped at **10 MB** (`io.LimitReader`); anything beyond is truncated. This is a fixed safeguard, not configurable.

> **TLS / HTTPS:** the inbound server speaks **plain HTTP only** — there are no `tls` fields here by design. Terminate TLS at a reverse proxy or load balancer (nginx, Caddy, Envoy, an ALB/NLB, an ingress controller) in front of the gateway. See [02 Gateway — TLS termination](./02-gateway.md#tls--https-termination).

### `http.client` — outbound HTTP client (gateway + scheduler)

```yaml
http:
  client:
    timeout: 30s
    maxIdleConns: 100
    maxIdleConnsPerHost: 10
    idleConnTimeout: 90s
    tls:
      insecureSkipVerify: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `timeout` | duration | `30s` | Per-request timeout for outbound HTTP actions. |
| `maxIdleConns` | int | `100` | Connection-pool size across all hosts. |
| `maxIdleConnsPerHost` | int | `10` | Idle connections kept per host. |
| `idleConnTimeout` | duration | `90s` | How long an idle connection is kept. |
| `tls.insecureSkipVerify` | bool | `false` | Skip certificate verification on outbound calls. **Development / trusted-internal only.** |

---

## `kv` — Key-Value store

```yaml
kv:
  enabled: false
  buckets: []
  rules:
    enabled: false
    bucket: "rules"
    autoProvision: false
  localCache:
    enabled: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable KV integration (required for `{@kv.*}` lookups and KV rule storage). |
| `buckets` | list | `[]` | Buckets to make available. Each entry is a plain string (watch all keys) or an object with `name` + `keyFilter`. Duplicate names are rejected. |
| `localCache.enabled` | bool | `true` when `kv.enabled` *(see note)* | In-memory KV cache (~25× faster lookups). |

> **`localCache` default:** when `kv.enabled: true` and you do **not** explicitly set `kv.localCache.enabled`, it defaults to **`true`**. Set it to `false` explicitly to disable.

### Bucket entry formats

```yaml
kv:
  buckets:
    - "device_status"               # string → watch all keys
    - name: "customer_data"         # object → optional key filter
      keyFilter: "region-us.>"
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | — | Bucket name (required, non-empty). |
| `keyFilter` | string | `">"` | NATS-style key filter; `>` watches all keys. |

### `kv.rules` — KV-based rule storage

When enabled, rules load from a NATS KV bucket instead of the `rules/` directory, with automatic hot-reload. See [08 KV Rule Store](./08-kv-rule-store.md).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Load rules from KV instead of files. |
| `bucket` | string | `"rules"` | KV bucket holding rules. Required (non-empty) when enabled. |
| `autoProvision` | bool | `false` | Create the bucket with JetStream defaults if it doesn't exist. No-op if the bucket already exists. |

---

## `security.verification` — NATS signature verification

NKey-based payload signature checks. See [07 Security](./07-security.md).

```yaml
security:
  verification:
    enabled: false
    publicKeyHeader: "Nats-Public-Key"
    signatureHeader: "Nats-Signature"
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable signature verification (`{@signature.*}` variables). |
| `publicKeyHeader` | string | `"Nats-Public-Key"` | Header carrying the signer's NKey public key. |
| `signatureHeader` | string | `"Nats-Signature"` | Header carrying the Base64 Ed25519 signature. |

---

## `logging`

```yaml
logging:
  level: info
  encoding: json
  outputPath: stdout
```

| Field | Type | Default | Valid values | Description |
|-------|------|---------|--------------|-------------|
| `level` | string | `"info"` | `debug`, `info`, `warn`, `error` | Log verbosity. `debug` logs per-condition evaluation results — invaluable for triaging non-firing rules. |
| `encoding` | string | `"json"` | `json`, `console` | Output format. |
| `outputPath` | string | `"stdout"` | `stdout`, `stderr`, or a file path | Log destination. |

---

## `metrics`

```yaml
metrics:
  enabled: true
  address: :2112
  path: /metrics
  updateInterval: 15s
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Start the Prometheus metrics server. The address/path/interval defaults below apply **only when enabled**. |
| `address` | string | `":2112"` | Metrics server listen address. |
| `path` | string | `"/metrics"` | Metrics endpoint path. |
| `updateInterval` | duration string | `"15s"` | How often system gauges (goroutines, memory) refresh. Must parse as a Go duration. |

Full metric catalogue and health-check details are in [12 Observability](./12-observability.md).

---

## `forEach`

```yaml
forEach:
  maxIterations: 100
```

| Field | Type | Default | Bounds | Description |
|-------|------|---------|--------|-------------|
| `maxIterations` | int | `100` | `0`–`10000` | Max array elements processed per `forEach` action. `0` = unlimited (use with caution). The hard ceiling enforced at load is **10000**. |

When an array exceeds the limit, the first N elements are processed and the rest are **silently dropped**. Watch the `foreach_iterations_total` metric against expected array sizes to detect truncation. See [05 Array Processing](./05-array-processing.md#performance--limits).

---

## Complete annotated example

```yaml
# Single process running router + gateway, KV-backed rules, metrics on.
features:
  router: true
  gateway: true
  scheduler: false

nats:
  urls:
    - nats://nats-1:4222
    - nats://nats-2:4222
  credsFile: /etc/nats/creds/rule-router.creds
  tls:
    enable: true
    caFile: /etc/ssl/nats/ca.pem
  consumers:
    consumerPrefix: rule-router
    workerCount: 8
    fetchBatchSize: 50
    maxAckPending: 2000
    maxDeliver: 5
  publish:
    mode: jetstream
    ackTimeout: 5s

http:
  server:
    address: ":8080"
    writeTimeout: 30s          # keep > any bridge timeout
    inboundWorkerCount: 20
    inboundQueueSize: 5000
  client:
    timeout: 15s

kv:
  enabled: true
  buckets:
    - device_status
  rules:
    enabled: true
    bucket: rules
    autoProvision: false
  localCache:
    enabled: true

security:
  verification:
    enabled: true

logging:
  level: info
  encoding: json
  outputPath: stdout

metrics:
  enabled: true
  address: :2112
  path: /metrics
  updateInterval: 15s

forEach:
  maxIterations: 500
```

---

## Dead-letter behavior

There is no dedicated dead-letter *subject*. Delivery semantics are governed entirely by JetStream and `nats.consumers.maxDeliver`:

- A message that **panics** during processing is **terminated** (`msg.Term()`) immediately so it cannot loop.
- A message that **fails** processing is **NAK'd** and redelivered until `maxDeliver` is reached, after which JetStream stops redelivering it.
- Unparseable payloads are handled leniently by the rule engine (not rejected at the broker layer).

To inspect messages that exhaust delivery, use JetStream's advisory subjects (`$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>`) or raise `maxDeliver` and a stream-level retention policy. See [10 Troubleshooting](./10-troubleshooting.md).

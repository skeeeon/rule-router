# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Rule Router is a high-performance, rule-based messaging platform for NATS written in Go 1.26. It contains a unified binary with three selectable features, plus companion tools:

- **rule-router** (`cmd/rule-router/`) — Unified binary with three features enabled via config (`features.router`, `features.gateway`, `features.scheduler`) or env vars (`RR_FEATURES_GATEWAY=true`):
  - **Router** (default) — NATS-to-NATS message routing with rule evaluation
  - **Gateway** — Bidirectional HTTP↔NATS integration (inbound webhooks and outbound API calls)
  - **Scheduler** — Cron-based scheduled publishing to NATS or HTTP
- **nats-auth-manager** (`cmd/nats-auth-manager/`) — OAuth2/API token manager backed by NATS KV
- **rule-cli** (`cmd/rule-cli/`) — CLI for scaffolding and testing rules
- **wasm** (`cmd/wasm/`) — Rule engine compiled to WebAssembly for in-browser testing

## Build and Test Commands

```bash
# Build applications
go build ./cmd/rule-router
go build ./cmd/nats-auth-manager
go build ./cmd/rule-cli

# Build WASM test engine for web UI
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/public/tester.wasm ./cmd/wasm/
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/public/

# Build web UI
cd web && npm install && npm run build

# Run all tests
go test ./...

# Run tests for a single package
go test ./internal/rule/...
go test ./config/...

# Run a single test by name
go test ./internal/rule/... -run TestProcessorEvaluate
```

No Makefile — use standard Go tooling. No external test frameworks; all tests use the standard `testing` package with table-driven patterns.

## Architecture

### Rule Engine (`internal/rule/`)

The core of the system. Key types and flow:

1. **Loader** parses YAML rules from filesystem or NATS KV bucket
2. **Index** maps NATS subject patterns to rules for O(1) lookup. Wildcard patterns (`*`, `>`) compile through `pattern.go`; HTTP path patterns reuse the same machinery via `path_matcher.go` (slash-separated).
3. **Processor** orchestrates rule evaluation on incoming messages
4. **Evaluator** resolves template variables and checks condition operators (`eq`, `gt`, `lt`, `gte`, `lte`, `contains`, `not_contains`, `exists`, `not_exists`, `any`, `all`, `none`); `condition_resolver.go` holds shared helpers
5. **ThrottleManager** (`throttle.go`) — per-rule leading-edge suppression with a configurable window; state resets naturally when Processor is rebuilt on reload. See Throttle below for the trailing-mode path, which does *not* live here.
6. **Signature verification** (`signature.go` + `signature_verify.go`) — nkey-based payload signature checks (stubbed out in the WASM build)

Template syntax:
- Message fields: `{field}`, plus nested paths via `{data.device.id}`
- Subject tokens: `{@subject.0}`, `{@subject.1}`, …
- HTTP request: `{@path}`, `{@path.0}`, `{@path.count}`, `{@method}`, `{@header.X-Name}` (case-insensitive), `{@query.name}` (case-sensitive)
- KV lookups: `{@kv.bucket.key}` (supports nested template substitution inside the key)
- System functions: `{@timestamp()}`, `{@uuid4()}`, `{@uuid7()}`; random fixture data via `{@random.int(min,max)}`, `{@random.float(min,max,decimals)}`, `{@random.choice(a,b,…)}` (validated at load by `random.go::randomValue`, the single definition of a valid call)
- Time context (pre-computed per evaluation): `{@time.hour}`, `{@time.minute}`, `{@day.name}`, `{@day.number}`, `{@date.year}`, `{@date.month}`, `{@date.day}`, `{@date.iso}`, `{@timestamp.unix}`, `{@timestamp.iso}`

Body decoding (`context.go::NewEvaluationContext`) is the single payload→fields boundary — the gateway, `rule-cli`, and WASM all route through it. It picks a decoder from the canonicalized `Content-Type` header (canonicalization therefore runs *before* the decode):

- `application/x-www-form-urlencoded` → `decodeForm`. Values stay strings (inferring types would turn a PIN of `007` into `7`; `Evaluator.toFloat` parses strings, so numeric operators work anyway), a repeated key becomes an array, and a malformed body is an error rather than a partial parse — a silently dropped field reads as absent and can flip a condition instead of raising one. A form body sent *without* the header still takes the JSON path, so the decoder is opt-in by the sender.
- anything else → JSON with `UseNumber()`, falling back to a raw string for valid-UTF-8 non-JSON.

**The bar for a third decoder is deliberately high** and is written above `contentTypeForm`: it must need no configuration and yield the existing `map[string]any` shape. XML, multipart, CSV, and Protobuf/Avro all fail that test. Decode formats that outlive any one vendor; never decode a vendor's schema.

Query parameters live in their own `{@query.name}` namespace on `HTTPRequestContext.Query` (type `rule.QueryParams` — named so it cannot be transposed with the headers argument at a `ProcessHTTP` call site). They **never merge into the message object**: query data is attacker-supplied in a way an HMAC-gated body is not, so letting `?user_id=1` shadow a body field would be a privilege-escalation path. Names are matched verbatim (HTTP says query names are case-sensitive, unlike headers), only the first value of a repeated name is kept, and the query never affects rule matching or the metrics label — both key off `r.URL.Path` alone. `rule-cli check` takes `--query 'a=1&b=2'`; the web tester has a Query Params field on HTTP triggers.

### Broker (`internal/broker/`)

NATS connection and subscription management:
- **NATSBroker** — connection lifecycle and KV bucket initialization
- **SubscriptionManager** — JetStream pull consumer management with worker pools
- **StreamResolver** — dynamic JetStream stream discovery with hot-reload
- **RuleKVManager** — watches a KV bucket for rule changes and triggers hot-reload

### Features (`internal/app/`)

Each feature is a separate `lifecycle.Application` wired up by the AppBuilder:

- **RouterApp** (`router.go`) — subscribes to NATS subjects and runs the rule engine per message
- **GatewayApp** (`gateway.go`) — inbound HTTP→NATS + outbound NATS→HTTP (handlers in `internal/gateway/`)
- **SchedulerApp** (`scheduler.go`) — cron jobs via `go-co-op/gocron/v2` that publish to NATS or HTTP (5- or 6-field cron; `rule.CronParser` is the one dialect shared by loader and scheduler); KV-loaded jobs are tagged (`kv-rule`) so they can be swapped on hot-reload without touching file-loaded jobs

### Shared Infrastructure

- **AppBuilder** (`internal/app/builder.go`) — fluent builder pattern that wires Logger, Metrics, Broker, Processor, and KV Rule Manager together as a shared BaseApp
- **CompositeApp** (`internal/app/composite.go`) — runs multiple features concurrently under a single lifecycle, with shared resource cleanup via BaseApp.Close()
- **Lifecycle** (`internal/lifecycle/`) — SIGHUP triggers rule/stream reload; SIGTERM triggers graceful shutdown with WaitGroups
- **Logger** (`internal/logger/`) — slog frontend backed by zap; structured key-value logging
- **Metrics** (`internal/metrics/`) — Prometheus counters/histograms exposed on configurable port
- **Gateway** (`internal/gateway/`) — HTTP handlers for inbound (fire-and-forget by default, or synchronous when a matched rule has a `respond`/`request` action — see Request/Reply below) and outbound (ACK-on-success with retry) routes. The inbound server uses a single catch-all handler that delegates path matching to the Processor; both file-loaded and KV-loaded rules support exact paths and NATS-style wildcard paths (`/webhooks/*/events`, `/api/>`). Exact and wildcard rules both fire when both match. Wildcards are validated by `rule.ValidatePathPattern`. An HTTP trigger may declare an `hmac` block — generic (`header`/`secret`/`algorithm`/`encoding`/`prefix`) or a named `scheme` (`stripe`/`slack`/`standardwebhooks`, timestamp-signed with a fixed 5-minute tolerance; only `secret` is set) — which the handler enforces as a **fail-closed gate** via `Processor.CheckHTTPHMAC` before any rule fires: a bad/missing/unverifiable HMAC over the raw body → 401. The secret accepts a literal, an env ref `${VAR}` (expanded at load), or a KV ref `{@kv.bucket.key}` (resolved per request). This is transport auth, not a rule condition — it touches no evaluation/condition machinery (`hmac.go` + `verifyHMAC`, stdlib crypto, WASM-safe so no build-tag stub).
- **Deferred** (`internal/deferred/`) — the execution half of a trailing-edge action throttle. See Throttle below.
- **HTTPClient** (`internal/httpclient/`) — shared HTTP client (with retry/backoff) used by GatewayApp and SchedulerApp
- **Tester** (`internal/tester/`) — shared rule-evaluation harness used by both `rule-cli check` and the WASM build. A selected rule index (`check -n`, a `_rule_N/` test group, the WASM `ruleIndex`) loads **only that rule** into the processor (`setupTestProcessor`); loading the whole file lets a sibling rule on the same subject answer for the selected one (test-pinned by `TestSetupTestProcessor_RuleIndex`)
- **CLI helpers** (`internal/cli/`) — prompt, renderer, and validator helpers backing `rule-cli`. `rule-cli check` takes repeatable `--header 'Name: value'` so a quick check can set the `Content-Type` that selects the payload decoder; `rule-cli test` reads the same headers from `_test_config.json`
- **Auth Manager** (`internal/authmgr/`, with providers under `internal/authmgr/providers/`) — OAuth2 / custom-HTTP token provider layer backing `cmd/nats-auth-manager`

### Configuration

Config files live in `config/` (YAML). Loaded via Viper with environment variable overrides (prefix `RR_`) through pflag. A unified `Config` struct includes a `FeaturesConfig` section to enable/disable router, gateway, and scheduler. The canonical file is `config/rule-router.yaml`; legacy per-feature files (`http-gateway.yaml`, `rule-scheduler.yaml`) still work with the appropriate `features` block. `config/auth-manager.yaml` is consumed by `cmd/nats-auth-manager`. Config struct and validation live in `config/config.go`.

### Rule Format

Rules are YAML files in the `rules/` directory (organized by app: `rules/router/`, `rules/http/`, `rules/scheduler/`). Each rule has a trigger (nats subject, http path, or cron schedule), optional conditions, and an action: publish to a NATS subject (`action.nats`), call an HTTP endpoint (`action.http`), or reply to the caller (`action.respond`).

### NATS Delivery Modes

Both NATS triggers and NATS actions accept an optional `mode: jetstream | core`:

- **`trigger.nats.mode`** (default `jetstream`) selects the subscription transport. `core` means a plain core NATS subscription (at-most-once, no stream/consumer required, excluded from stream validation) served by `broker.Responder` — the same component that serves `reply: true` rules (`reply` implies core; `reply` + `mode: jetstream` is a load-time error). `queue` is valid on any core-transport trigger. `NATSTrigger.IsCore()` is the single classification helper; RouterApp (`jetStreamSubjects`), RuleKVManager (`collectNATSTriggerSubjects`/`hasCoreRules`/`CoreRules`), StreamResolver, and GatewayApp (`setupOutboundSubscriptions`) all branch on it. The Responder lives under `features.router` — core-transport triggers do not fire in gateway-only deployments (a startup warning flags this).
- **`action.nats.mode`** (default: inherit global `nats.publish.mode`) selects the publish transport per action. Resolved by `effectivePublishMode` in the shared `broker.actionPublisher` (used by SubscriptionManager and Responder), `NATSBroker.Publish` (scheduler/httpclient), and the gateway's `publishToNATS`. Retry/ack tuning stays global. Every publisher reads `Mode` off the **evaluated** action, so `Processor.processNATSAction` (and its `forEach` variant) must copy it onto the result — dropping it silently downgrades `mode: core` to a JetStream publish, which never acks on a subject no stream covers (test-pinned by `TestActionMode_SurvivesEvaluation`).
- **Double-fire guard**: `Processor.ProcessForSubscription` takes a `NATSTriggerFilter` (`JetStreamRuleFilter` / `CoreRuleFilter`) so each transport only evaluates its own rules — required because a subject can be covered by both transports (mixed-mode rules or overlapping wildcards). When adding new subscription paths, always pass the right filter.

### Throttle (leading & trailing)

The YAML block is `throttle` on triggers and actions; `debounce` is a deprecated alias folded onto it by `loader.go::normalizeThrottle` (setting both is an error). `ThrottleConfig.Mode` is `leading` (default) or `trailing`.

- **Leading** is the original behavior: `Processor.evaluateRules` calls `ThrottleManager.Allow` and drops the message inline. Fully contained in `internal/rule`.
- **Trailing** is actions-only (rejected on triggers and with `request: true` in `validateThrottleConfig`/`validateNATSAction`). The Processor stays pure: it never sleeps, times, or publishes. `evaluateRules` routes a trailing rule's actions into `Outcome.Deferred` as one `DeferredBatch`, and `internal/deferred.Coalescer` does the holding — replace-on-submit, fixed window from first submit (not reset-on-each), flush-on-Stop.

Every `Process*` method returns a **`rule.Outcome`**, not `[]*Action`: `Immediate` runs now, `Deferred` goes to a Coalescer. The two are separate fields precisely because a deferred action is indistinguishable from an immediate one by inspection — a single slice would let a caller publish a held action immediately with no error. `Outcome.All()` flattens both for *inspection only* (rule-cli, web tester, tests); never execute its result.

One Coalescer per execution site, each wired to that site's own executor so retry/transport/metrics match the immediate path: `SubscriptionManager.executeAction`, `Responder.executeSideEffect`, `InboundServer.executeDeferred`, `SchedulerApp.executeDeferred`. Each site's shutdown path must call `coalescer.Stop(ctx)` after its workers stop but *before* the NATS connection closes.

An action throttle gates the action as a unit and runs before `forEach` expansion, so the key resolves against the trigger context and a fan-out is one batch. This is deliberate and test-pinned (`TestProcessor_ActionThrottle_ForEach*`) — it is what keeps trailing mode from collapsing N forEach actions into the last element.

### Request/Reply & HTTP Responses

Beyond fire-and-forget routing, a rule can return a correlated response to the caller. Three shapes, all built on `action.respond` (a single terminal response — payload/passthrough/merge/headers like a NATS action, plus an HTTP-only `statusCode`; no `forEach`/`debounce`) and a `request` flag on `action.nats`:

- **HTTP synchronous respond (B1):** an HTTP-triggered rule with `action.respond` writes the evaluated payload back as the HTTP response (`statusCode` defaults to 200). The inbound gateway routes such paths to an inline synchronous handler (`Processor.HasSyncHTTPPath`) instead of the fire-and-forget worker queue.
- **NATS responder (A1):** a NATS trigger with `reply: true` (optional `queue` for load-balancing) makes the router subscribe via **core NATS** (not JetStream — see `internal/broker/responder.go`, wired in `RouterApp` under `features.router`) and answer each request via `msg.Respond` using the `respond` action. Reply subjects are excluded from JetStream consumer creation and stream validation. The Responder also serves `trigger.nats.mode: core` rules, executing their NATS/HTTP actions (see NATS Delivery Modes above).
- **HTTP↔NATS bridge (B2):** an HTTP-triggered rule with `action.nats.request: true` (+ optional `timeout`, default 5s) makes the gateway issue `nc.Request` and return the reply as the HTTP response (`nats.ErrNoResponders` → 503, timeout → 504). `request: true` is only honored on HTTP triggers.

Cross-checks live in `loader.go::validateTriggerActionCompatibility`. When adding/altering these fields, update every schema surface: `internal/cli` + `internal/tester` (rule-cli), `cmd/wasm/main.go` (`actionResult`), and the web rule-builder (`web/src/`).

## Key Dependencies

- `nats-io/nats.go` — NATS client and JetStream
- `nats-io/nkeys` — NKey signature verification
- `spf13/cobra` + `spf13/viper` — CLI and configuration
- `go.uber.org/zap` — logging backend
- `prometheus/client_golang` — metrics
- `goccy/go-json` — JSON parsing (with UseNumber for numeric precision)
- `robfig/cron` + `go-co-op/gocron` — cron scheduling

## WASM Build Architecture

The web UI includes a rule tester that runs the real Go rule engine in the browser via WebAssembly. The WASM binary is built from `cmd/wasm/main.go` and uses the same evaluation path as `rule-cli check`.

Heavy dependencies are excluded from the WASM build via `//go:build` tags to keep the binary small (~7 MB, ~2 MB gzipped):

- `internal/logger/logger.go` has `//go:build !js` — WASM uses `logger_wasm.go` (slog-only, no zap/viper)
- `internal/metrics/metrics.go` and `collector.go` have `//go:build !js` — WASM uses `metrics_wasm.go` (no-op stubs)
- `internal/rule/kv_context.go` has `//go:build !js` — WASM uses `kv_context_wasm.go` (local cache only, no jetstream)
- `internal/rule/signature_verify.go` has `//go:build !js` — WASM uses `signature_verify_wasm.go` (no-op, no nkeys)

When adding new methods to `*metrics.Metrics`, `*logger.Logger`, or `*KVContext`, the corresponding WASM stub file must also be updated. When adding new methods to the `*KVContext` that are called from other rule package files, add them to both `kv_context.go` and `kv_context_wasm.go`. `internal/tester` is imported by `cmd/wasm` — any change that pulls new non-WASM-safe imports into tester must be guarded behind build tags.

## Code Conventions

- JSON decoding uses `UseNumber()` to preserve numeric precision (important for large integers)
- Error wrapping with `fmt.Errorf("...%w", err)` and checking with `errors.Is()`
- Lock-free hot-reload via `atomic.Value` for KV-backed rule updates
- Context cancellation propagated throughout for graceful shutdown
- All logging uses structured key-value pairs (slog-style)
- `//go:build` tags separate WASM-specific stubs from production code (see WASM Build Architecture above)

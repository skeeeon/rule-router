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
4. **Evaluator** resolves template variables and checks condition operators (`eq`, `gt`, `lt`, `gte`, `lte`, `contains`, `not_contains`, `any`, `all`, `none`); `condition_resolver.go` holds shared helpers
5. **ThrottleManager** (`throttle.go`) — per-rule fire-first suppression with a configurable window; state resets naturally when Processor is rebuilt on reload
6. **Signature verification** (`signature.go` + `signature_verify.go`) — nkey-based payload signature checks (stubbed out in the WASM build)

Template syntax:
- Message fields: `{field}`, plus nested paths via `{data.device.id}`
- Subject tokens: `{@subject.0}`, `{@subject.1}`, …
- KV lookups: `{@kv.bucket.key}` (supports nested template substitution inside the key)
- System functions: `{@timestamp()}`, `{@uuid4()}`, `{@uuid7()}`
- Time context (pre-computed per evaluation): `{@time.hour}`, `{@time.minute}`, `{@day.name}`, `{@day.number}`, `{@date.year}`, `{@date.month}`, `{@date.day}`, `{@date.iso}`, `{@timestamp.unix}`, `{@timestamp.iso}`

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
- **SchedulerApp** (`scheduler.go`) — cron jobs via `go-co-op/gocron/v2` that publish to NATS or HTTP; KV-loaded jobs are tagged (`kv-rule`) so they can be swapped on hot-reload without touching file-loaded jobs

### Shared Infrastructure

- **AppBuilder** (`internal/app/builder.go`) — fluent builder pattern that wires Logger, Metrics, Broker, Processor, and KV Rule Manager together as a shared BaseApp
- **CompositeApp** (`internal/app/composite.go`) — runs multiple features concurrently under a single lifecycle, with shared resource cleanup via BaseApp.Close()
- **Lifecycle** (`internal/lifecycle/`) — SIGHUP triggers rule/stream reload; SIGTERM triggers graceful shutdown with WaitGroups
- **Logger** (`internal/logger/`) — slog frontend backed by zap; structured key-value logging
- **Metrics** (`internal/metrics/`) — Prometheus counters/histograms exposed on configurable port
- **Gateway** (`internal/gateway/`) — HTTP handlers for inbound (fire-and-forget by default, or synchronous when a matched rule has a `respond`/`request` action — see Request/Reply below) and outbound (ACK-on-success with retry) routes. The inbound server uses a single catch-all handler that delegates path matching to the Processor; both file-loaded and KV-loaded rules support exact paths and NATS-style wildcard paths (`/webhooks/*/events`, `/api/>`). Exact and wildcard rules both fire when both match. Wildcards are validated by `rule.ValidatePathPattern`.
- **HTTPClient** (`internal/httpclient/`) — shared HTTP client (with retry/backoff) used by GatewayApp and SchedulerApp
- **Tester** (`internal/tester/`) — shared rule-evaluation harness used by both `rule-cli check` and the WASM build
- **CLI helpers** (`internal/cli/`) — prompt, renderer, and validator helpers backing `rule-cli`
- **Auth Manager** (`internal/authmgr/`, with providers under `internal/authmgr/providers/`) — OAuth2 / custom-HTTP token provider layer backing `cmd/nats-auth-manager`

### Configuration

Config files live in `config/` (YAML). Loaded via Viper with environment variable overrides (prefix `RR_`) through pflag. A unified `Config` struct includes a `FeaturesConfig` section to enable/disable router, gateway, and scheduler. The canonical file is `config/rule-router.yaml`; legacy per-feature files (`http-gateway.yaml`, `rule-scheduler.yaml`) still work with the appropriate `features` block. `config/auth-manager.yaml` is consumed by `cmd/nats-auth-manager`. Config struct and validation live in `config/config.go`.

### Rule Format

Rules are YAML files in the `rules/` directory (organized by app: `rules/router/`, `rules/http/`, `rules/scheduler/`). Each rule has a trigger (nats subject, http path, or cron schedule), optional conditions, and an action: publish to a NATS subject (`action.nats`), call an HTTP endpoint (`action.http`), or reply to the caller (`action.respond`).

### Request/Reply & HTTP Responses

Beyond fire-and-forget routing, a rule can return a correlated response to the caller. Three shapes, all built on `action.respond` (a single terminal response — payload/passthrough/merge/headers like a NATS action, plus an HTTP-only `statusCode`; no `forEach`/`debounce`) and a `request` flag on `action.nats`:

- **HTTP synchronous respond (B1):** an HTTP-triggered rule with `action.respond` writes the evaluated payload back as the HTTP response (`statusCode` defaults to 200). The inbound gateway routes such paths to an inline synchronous handler (`Processor.HasSyncHTTPPath`) instead of the fire-and-forget worker queue.
- **NATS responder (A1):** a NATS trigger with `reply: true` (optional `queue` for load-balancing) makes the router subscribe via **core NATS** (not JetStream — see `internal/broker/responder.go`, wired in `RouterApp` under `features.router`) and answer each request via `msg.Respond` using the `respond` action. Reply subjects are excluded from JetStream consumer creation and stream validation.
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

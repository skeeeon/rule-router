# Changelog

## [Unreleased]

## [0.14.0] - 2026-06-07

### Features
- **Inbound webhook HMAC verification** — an HTTP trigger can declare an `hmac` block (`header`, `secret`, `algorithm` `sha256`/`sha1`, `encoding` `hex`/`base64`, optional `prefix`). The gateway verifies the HMAC over the raw request body as a **fail-closed gate** before any rule fires: a bad, missing, or unverifiable signature returns `401` and the rule never evaluates (both fire-and-forget and synchronous routes). Covers GitHub, Shopify, and most generic HMAC webhooks; timestamp-signed schemes (Stripe/Slack) are out of scope. The `secret` accepts a literal, an env ref `${VAR}`, or a KV ref `{@kv.bucket.key}`. New metric `webhook_hmac_verifications_total{result}`. `rule-cli` gains the `http-inbound-hmac` template and an HMAC step in the interactive builder; the web rule builder gains an HMAC section on HTTP triggers.

## [0.13.0] - 2026-06-07

### Features
- **NATS request/reply & HTTP responses** — rules can now answer the caller, not just route onward. Built on a new `respond` action plus a `request` flag on NATS actions; the rule evaluation path is unchanged, only where the result goes:
  - **`respond` action**: returns the evaluated/enriched payload to the caller — written as the HTTP response on an HTTP-triggered rule, or sent via `msg.Respond` on a NATS trigger. Supports an HTTP `statusCode` (default 200), headers, and templated/passthrough/merge payloads.
  - **NATS responder**: a NATS trigger with `reply: true` (optional `queue` for load-balanced responders) subscribes via **core NATS** (not JetStream) and answers each request with a `respond` action. Runs under the `router` feature — no new feature flag.
  - **HTTP↔NATS bridge**: an HTTP-triggered rule with `nats.request: true` (optional `timeout`, default 5s) issues `nc.Request` and returns the reply as the HTTP response (no responder → `503`, timeout → `504`).
  - The gateway serves these synchronously and inline, bypassing the fire-and-forget worker queue; ordinary webhook routes are unaffected.
  - `rule-cli` gains respond support in `new`/`check` and three templates (`http-respond`, `nats-reply`, `http-bridge`); the web rule builder gains the respond action, `reply`/`queue` trigger fields, and `request`/`timeout` NATS-action fields; the WASM tester renders respond output.
- Inbound gateway HTTP paths now support NATS-style wildcards: `*` matches one segment, `>` matches one or more trailing segments (e.g. `/webhooks/*/events`, `/api/>`). Works for both file-loaded and KV-loaded rules. When an exact rule and a wildcard rule both match a request, both fire — same semantics as NATS subjects.

### Improvements
- Unified the inbound gateway HTTP handler: a single catch-all handler now serves both file-loaded and KV-loaded rules, removing the file/KV mode split and the `SetKVMode` wiring.
- `MatchTokens` hot path rewritten to use slice equality instead of `strings.Join` + string compare. Eliminates one allocation per NATS message dispatch on exact-match rules (~5× faster on the microbenchmark).

### Fixes
- `RR_*` environment overrides now apply to keys absent from the config file (e.g. `RR_NATS_USERNAME`, `RR_FEATURES_GATEWAY`). Previously Viper's `AutomaticEnv` only resolved keys already present in the YAML, so credential and feature-flag env vars were silently ignored and file-less, env-only operation didn't work. The documented override keys are now bound explicitly.
- Removed the CLI override flags (`--nats-urls`, `--metrics-enabled`, `--metrics-addr`, `--metrics-path`, `--log-level`): they were bound to a different Viper instance than the config loader used and never took effect. Use the equivalent `RR_*` environment variables instead.
- KV-loaded rules now receive unique throttle/debounce indices. Previously every KV-loaded rule shared index `0`, so two rules in the same phase resolving to the same key collided in the throttle map and one rule's traffic could suppress another's.
- Outbound NATS publishes and HTTP actions are now bound to the consumer's ack-wait window. A slow upstream can no longer outlive redelivery and fire the action twice; if it can't finish in time the message is NAK'd and redelivered cleanly.
- HTTP path / NATS subject wildcards no longer match empty segments: `*` requires a non-empty token, so `/webhooks//events` no longer matches `/webhooks/*/events` (and `a..c` no longer matches `a.*.c`).
- Dropped the templated outbound URL from the `http_outbound_requests_total` and `http_outbound_duration_seconds` metric labels (keeping `status_code`) to prevent unbounded Prometheus cardinality from per-request URLs.
- Removed dead message terminal-error classification that matched standard-library JSON error types the rule engine (which uses `goccy/go-json`) never produces. Unparseable payloads continue to be handled leniently by the engine.
- KV rule watcher no longer spins at 100% CPU when its update channel closes unexpectedly; it now self-heals by re-establishing the watcher with capped exponential backoff and performing a full re-sync.

## [0.12.0] - 2026-05-09

### Features
- Added `publishResponse` to HTTP actions: on a 2xx response, the body (capped at 1 MB) is republished to a NATS subject. Works in the scheduler (cron-poll → NATS) and the gateway/router outbound path (NATS event → HTTP call → NATS result). Subject templates resolve against the trigger context only; publish failures log but do not fail the action.
- Added `schedule-poll` template to `rule-cli` (`rule-cli new --template=schedule-poll`) demonstrating the HTTP-poll-to-NATS pattern.
- Web rule builder: "Publish Response" toggle and subject input added to the HTTP action form.

## [0.11.0] - 2026-04-15

### Features
- **Unified single binary**: consolidated `rule-router`, `http-gateway`, and `rule-scheduler` into a single `rule-router` binary with feature flags (`features.router`, `features.gateway`, `features.scheduler`) and env var overrides (`RR_FEATURES_GATEWAY=true`)
- Removed standalone `cmd/http-gateway` and `cmd/rule-scheduler` binaries

### Improvements
- Simplified broker pipeline and removed redundant subscriptions in gateway
- Rule Builder Web UI: UX improvements including field suggestion input, message inspector, and help modal
- Updated web dependencies
- Fixed and updated benchmarks
- Updated Docker configuration and documentation for unified binary

## [0.10.0] - 2026-03-28

### Features
- Added **Rule Builder Web UI** (`web/`): visual rule creation with live YAML preview, multi-file support, and NATS KV push/pull via WebSocket
- Web UI built with Vue 3 (Vite, composition API SFCs), `@nats-io/nats-core` + `@nats-io/kv` for NATS operations
- Guided form covers full rule format: all trigger types, recursive conditions with nested groups, all action types with forEach/filter/headers/retry/debounce
- Per-file rule organization: assign filenames to group rules, download or push individual files or all at once
- Load existing rules from a NATS KV bucket and edit them in the form
- Dark mode with system theme detection and manual toggle (persisted to localStorage)
- Responsive layout with mobile drawer for YAML preview
- KV rule store support expanded to `rule-scheduler` (cron jobs rebuilt automatically on KV changes)

### Improvements
- `Close()` methods in all apps now use `errors.Join` for proper error unwrapping
- `RuleKVManager.Stop()` reads watcher under mutex for thread safety

## [0.9.0] - 2026-03-22

### Features
- Added **KV Rule Store**: optionally load rules from a NATS KV bucket instead of YAML files, with automatic hot-reload via KV Watch
- Rules pushed to KV are validated, parsed, and hot-swapped into the processor without restart
- JetStream consumers and subscriptions are created/removed dynamically as rule subjects change
- Supported by `rule-router` (NATS trigger rules) and `http-gateway` (both inbound HTTP and outbound NATS-to-HTTP rules)
- Added `rule-cli kv push` command to upload rule files to a NATS KV bucket with validation and dry-run support
- File paths are converted to dotted KV keys (e.g., `sensors/tank.yaml` becomes `sensors.tank`)
- Added `OutboundSubscriber` interface for dynamic outbound subscription management in `http-gateway`
- Added `Refresh()` to `StreamResolver` for picking up newly created JetStream streams at runtime
- Added `ProcessForSubscription()` to `Processor` for O(1) rule lookup by trigger subject (bypasses pattern matching index)
- Configuration: new `kv.rules` section with `enabled`, `bucket`, and `autoProvision` options
- Added HTTP action support to `rule-scheduler` for outbound API calls on cron schedules
- HTTP actions use configurable retry with exponential backoff and jitter (same logic as `http-gateway`)
- Extracted shared HTTP executor into `internal/httpclient` package, used by both `rule-scheduler` and `http-gateway`
- Added `http.client` configuration section to `rule-scheduler.yaml` (timeout, connection pooling, TLS)
- Added `keyFilter` option for KV cache to selectively watch specific keys per bucket, reducing memory and bandwidth
- Logger rewritten to slog frontend backed by zap, replacing direct zap usage across all applications

### Improvements
- JSON decoding now uses `UseNumber()` to preserve numeric precision, preventing silent data corruption on large integers
- Error comparisons use `errors.Is()` instead of bare `==` or string matching, so wrapped errors are handled correctly
- Auth manager startup jitter now respects context cancellation, preventing goroutines from hanging during shutdown
- Gateway NATS publish derives timeout from the worker context instead of `context.Background()`, respecting shutdown signals
- Added string-splitting cache and parsed KV field cache to reduce repeated allocations in hot paths
- Tuned log levels: routine KV cache hits and rule index lookups moved from info/warn to debug
- Structured log fields standardized across broker, gateway, and rule engine

## [0.8.0] - 2026-03-19
- Added `schedule-basic` template to `rule-cli new` for cron-based schedule rules
- Interactive wizard now supports Schedule (Cron) triggers alongside NATS and HTTP
- Interactive wizard supports building multiple rules into a single file with "Add another rule?" loop
- `rule-cli scaffold` creates per-rule `_rule_N/` test subdirectories for multi-rule YAML files
- `rule-cli test` detects and runs `_rule_N/` per-rule test directories independently
- `rule-cli check` adds `--rule-index` / `-n` flag to select a specific rule in multi-rule files
- Single-rule files retain full backward compatibility with existing flat test directory layout
- `forEach` can now source arrays from KV stores (`forEach: "{@kv.bucket.key}"`) for fan-out patterns
- Enables `rule-scheduler` forEach with KV-managed target lists (no message payload required)

## [0.7.0] - 2026-03-19
- Added `rule-scheduler` application for cron-based scheduled NATS publishing
- New `schedule` trigger type with standard 5-field cron expressions and optional IANA timezone support
- Schedule rules use the same conditions, templates, KV lookups, and time variables as NATS/HTTP rules
- Added `Publish()` method to NATS broker for direct publishing without a subscription manager
- Hot reload (`SIGHUP`) support for schedule rules
- Fixed KV `localCache.enabled` default not being applied when `kv.enabled: true` (defaulting logic ran before config was loaded)
- Prometheus metrics on port `:2114` for scheduler action tracking

## [0.6.0] - 2026-03-16
- Added optional per-rule debounce/throttle for triggers and actions
- Fire-first semantics: first message processed immediately, subsequent messages suppressed for the window duration
- Configurable time window (`debounce.window`) and template-based key (`debounce.key`) on NATS/HTTP triggers and actions
- Per-rule isolation via composite throttle keys prevents cross-rule interference
- New `throttle_suppressed_total` Prometheus metric with phase label (trigger/action)

## [0.5.0] - 2026-02-24
- Added `merge: true` action payload mode for NATS and HTTP actions
- Deep-merges a templated overlay onto the original message, preserving all existing fields
- Supports nested object recursion (nested maps merge rather than replace)
- Works with `forEach` (array element is the merge base)
- New `"merge"` action type metric for Prometheus observability

## [0.4.0] - 2025-11-09
- BREAKING CHANGE
- Variable syntax updated to require {} in all invocations
- Variable support added for condition values 

## [0.3.0] - 2025-11-05
- nats-auth-manager application for JWT lifecycle management

## [0.2.2] - 2025-11-03
- Enhanced Environment Variable Substitution 

## [0.2.1] - 2025-10-30
- Resolved primitive/array wrapping and resolution

## [0.2.0] - 2025-10-30
- Added primitive support for KV Lookups
- JSON path is option for KV Lookups

## [0.1.0] - 2025-10-29

### Added
- Initial release
- Rule-based NATS message routing
- Bidirectional HTTP gateway
- Array processing with forEach
- KV store integration with local cache
- Signature verification
- Rule-cli utility

[0.11.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.11.0
[0.10.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.10.0
[0.9.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.9.0
[0.8.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.8.0
[0.7.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.7.0
[0.1.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.1.0
[0.2.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.0
[0.2.1]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.1
[0.2.2]: https://github.com/skeeeon/rule-router/releases/tag/v0.2.2
[0.3.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.3.0
[0.4.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.4.0
[0.5.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.5.0
[0.6.0]: https://github.com/skeeeon/rule-router/releases/tag/v0.6.0

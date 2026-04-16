# Rule Builder Web UI

A visual rule builder for the Rule Router platform. Build rules with a guided form, preview the generated YAML in real time, test rules against sample messages, and optionally push directly to a NATS KV bucket.

## Features

- **Guided Form**: Step-by-step rule creation — Trigger, Conditions, Action — with type-specific fields that appear as needed.
- **Live YAML Preview**: See the generated YAML update in real time as you fill in the form.
- **Rule Testing (WASM)**: Test rules against sample JSON messages directly in the browser using the real Go rule engine compiled to WebAssembly. Full fidelity with production evaluation — no drift, no approximation.
- **Multi-Rule Support**: Build multiple rules, group them into separate files, or combine into one.
- **Per-File Organization**: Assign filenames to rules — rules with the same filename group into one YAML output. Download or push individual files or all at once.
- **KV Push**: Push rules directly to a NATS KV bucket via WebSocket. Authenticate with a `.creds` file. Connection settings are saved in session storage.
- **KV Pull**: Load existing rules from a NATS KV bucket, edit them in the form, and push back.
- **Client-Side Validation**: Validates rules as you type — trigger subjects, cron expressions, HTTP paths, condition operators, and more.
- **Auto-Save**: Rules are automatically saved to localStorage and restored on page load.
- **Dark Mode**: Follows system preference automatically, with a manual toggle (System / Light / Dark) that persists to local storage.
- **Responsive**: Two-panel layout on desktop; bottom drawer for YAML preview on mobile.

## Quick Start

```bash
cd web
npm install
npm run dev
```

Open http://localhost:5173 in your browser.

To enable rule testing, build the WASM binary first (see [WASM Build](#wasm-build) below).

## Build for Production

```bash
# 1. Build the WASM test engine
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/public/tester.wasm ./cmd/wasm/
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/public/

# 2. Build the web UI
cd web
npm run build
```

Static files are output to `dist/`. Serve them with any static file server. The WASM binary and `wasm_exec.js` must be served alongside `index.html`.

## WASM Build

The rule tester compiles the Go rule engine to WebAssembly so rules can be evaluated in the browser with 100% fidelity to the production engine. This is the same code path used by `rule-cli check`.

### Building

```bash
# From the repository root:
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o web/public/tester.wasm ./cmd/wasm/
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/public/
```

This produces two files in `web/public/`:
- **tester.wasm** (~7 MB, ~2 MB gzipped) — the compiled rule engine
- **wasm_exec.js** (~17 KB) — Go's WASM runtime support

Both files are gitignored and must be built locally or in CI.

### How It Works

The WASM binary (`cmd/wasm/main.go`) registers a global `evaluateRule()` function that:
1. Parses rule YAML using the same loader as `rule-cli` and `rule-router`
2. Sets up a mock processor with local KV cache (no NATS connection needed)
3. Evaluates the rule against the provided sample message
4. Returns match status and rendered actions (subject/URL, payload, headers)

The UI lazy-loads the WASM binary only when the user opens the "Test Rule" panel for the first time. Subsequent tests are instant.

### Build Tags

Heavy dependencies (zap, prometheus, NATS client, nkeys) are excluded from the WASM binary via `//go:build` tags to minimize size. WASM-specific stubs in the following packages provide lightweight replacements:

| Package | WASM Stub | What's Excluded |
|---|---|---|
| `internal/logger` | `logger_wasm.go` | zap, viper, config — uses slog only |
| `internal/metrics` | `metrics_wasm.go` | prometheus — no-op stubs |
| `internal/rule` | `kv_context_wasm.go` | nats.go, jetstream — local cache only |
| `internal/rule` | `signature_verify_wasm.go` | nkeys — no-op verification |

These stubs only affect the WASM build target (`GOOS=js GOARCH=wasm`). The normal Go binaries are completely unchanged.

### Limitations

- **KV lookups** use mock data provided in the test panel (same format as `rule-cli check --kv-mock`). Live NATS KV is not available during testing.
- **Signature verification** (`@signature.valid`) is a no-op in the browser — it always returns false.
- **Debounce/throttle** state is not simulated — rules are evaluated as single messages.

## Rule Testing

The **Test Rule** panel appears below the Action section in the rule editor. To test a rule:

1. Expand the "Test Rule" section
2. Paste a sample JSON message
3. The subject/path is auto-populated from your trigger (editable for wildcard subjects)
4. Optionally provide mock headers or KV data
5. Click **Run Test**

The result shows:
- Whether the rule matched or not
- Each generated action with rendered subject/URL, payload, and headers
- For `forEach` rules: one action card per array element

This is equivalent to running `rule-cli check --rule <yaml> --message <json>`.

## Tech Stack

- [Vue 3](https://vuejs.org/) — Composition API with single-file components
- [Vite](https://vitejs.dev/) — Build tool and dev server
- [Go WASM](https://go.dev/wiki/WebAssembly) — Rule engine compiled to WebAssembly for in-browser testing
- [@nats-io/nats-core](https://github.com/nats-io/nats.js) — NATS WebSocket client
- [@nats-io/kv](https://github.com/nats-io/nats.js) — NATS KV operations
- [yaml](https://eemeli.org/yaml/) — YAML parsing for KV pull (rule import)
- Hand-rolled YAML serializer for rule output (no generic library needed)

## Rule Format

The builder supports the full rule YAML format used by all Rule Router applications:

- **Triggers**: NATS subject (with wildcards), HTTP path + method, or Cron schedule + timezone
- **Conditions**: Nested AND/OR groups, 15 operators including array operators (`any`, `all`, `none`), KV lookups, time-based fields
- **Actions**: Publish to NATS subject or make HTTP requests, with payload templates, passthrough, merge, forEach iteration, filters, headers, retry, and debounce

Rules built with this UI are identical to hand-written YAML and can be validated with `rule-cli lint`.

## KV Push / Pull

The KV push feature connects to NATS via WebSocket using the modern `@nats-io/nats-core` client. To use it:

1. Your NATS server must have a WebSocket listener enabled (e.g., port 9222)
2. Click **Push to KV** in the YAML preview panel
3. Enter the WebSocket URL, bucket name, and key name
4. Optionally select a `.creds` file for authentication
5. Click **Push**

Connection settings (URL, bucket, key) are saved to `sessionStorage` for convenience. Credentials are held in memory only — they are not persisted.

To load existing rules from a KV bucket, click **Load from KV** in the header.

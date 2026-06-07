# Rule Builder Web UI

A visual rule builder for the Rule Router platform. Build rules with a guided form, preview the generated YAML in real time, test rules against sample messages, and optionally push directly to a NATS KV bucket.

## Features

- **Guided Form**: Step-by-step rule creation — Trigger, Conditions, Action — with type-specific fields that appear as needed.
- **Live YAML Preview**: See the generated YAML update in real time as you fill in the form.
- **Rule Testing (WASM)**: Test rules against sample JSON messages directly in the browser using the real Go rule engine compiled to WebAssembly. Full fidelity with production evaluation — no drift, no approximation.
- **Sample Message Inspector**: Paste a sample JSON message once; the builder extracts dot-notation field paths and shares them between autocomplete, payload chips, and the rule tester.
- **Field & Context-Variable Autocomplete**: Type `{` in a condition field/value, forEach input, or other variable-aware input to suggest message fields from your sample. Type `{@` to surface the full context-variable catalog (`@time.*`, `@date.*`, `@signature.*`, `@timestamp()`, `@uuid4()`, …) plus trigger-derived tokens like `@subject.0..N` from your NATS subject or `@path.0..N` from your HTTP path. Schedule rules see only the variables that make sense without a message (Time, Date, Func, KV).
- **Cron Builder**: Simple tab with field-by-field controls and presets ("Every 5 min", "Hourly", "Daily 9am", "Weekdays 9am", …); Advanced tab for raw cron expressions. Human-readable description plus next-run preview powered by `cron-parser` / `cronstrue`. Optional IANA timezone.
- **Workspace Layout**: A `Files ▸ Rules` sidebar lists every rule grouped by file, with a `trigger → action` summary, request/reply mode chips (`reply` / `respond` / `bridge` / `forEach`), and per-rule error counts. Search to filter, click to edit in the center pane, and rename a file in place to move all its rules at once. On narrow screens the sidebar and editor become a master-detail push navigation.
- **Per-File Organization**: Rules group into YAML files by filename — add a rule to an existing file, create a new file, or move a rule by changing its filename. Download or push individual files or all at once.
- **KV Push**: Push rules directly to a NATS KV bucket via WebSocket. Authenticate with a `.creds` file. Connection settings are saved in session storage.
- **KV Pull**: Load existing rules from a NATS KV bucket, edit them in the form, and push back.
- **Trigger-Aware Actions**: The action form only offers what the trigger supports — a `Respond` action appears for HTTP triggers and NATS request/reply responders; the HTTP↔NATS bridge appears only on HTTP triggers; schedule triggers hide message-only options (passthrough, merge, forEach) since there is no incoming message. Enabling a NATS responder forces a `respond` action.
- **Client-Side Validation**: Validates rules as you type — trigger subjects, cron expressions, HTTP paths, condition operators, request/reply pairings, and more. Error counts surface in the sidebar per rule and per file.
- **Reference Help (`?`)**: Built-in modal documents every variable, operator, template feature, and trigger type — no need to leave the page.
- **Auto-Save**: Rules are automatically saved to localStorage and restored on page load.
- **Dark Mode**: Follows system preference automatically, with a manual toggle (System / Light / Dark) that persists to local storage.
- **Responsive**: Three-zone layout on wide screens (sidebar · editor · YAML rail); the rail collapses into a bottom drawer below 1024px, and the sidebar/editor become master-detail push navigation on mobile.

## Keyboard Shortcuts

| Shortcut | Action |
|---|---|
| `?` | Open the Reference modal |
| `Cmd/Ctrl + S` | Download the active rule's YAML file |
| `Esc` | Close modals |
| `↑` / `↓` | Navigate autocomplete suggestions |
| `Enter` / `Tab` | Accept the highlighted suggestion |

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

The **Test Rule** panel appears below the Action section in the rule editor. The sample message is shared with the **Sample Message** inspector at the top of the rule editor — paste it once and it powers both autocomplete and testing. To test a rule:

1. Expand the "Test Rule" section
2. Make sure the **Sample Message** inspector has a JSON payload (or paste one into the tester's message field)
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

- **Triggers**: NATS subject (with wildcards, optional request/reply responder + queue), HTTP path + method (paths support NATS-style wildcards too: `/webhooks/*/events`, `/api/>`), or Cron schedule + timezone — NATS and HTTP triggers support per-trigger debounce
- **Conditions**: Nested AND/OR groups, 15 operators including array operators (`any`, `all`, `none`), `in`/`not_in` membership lists, KV lookups, time-based fields. Variables resolve on both sides of a condition (`{temperature}` `gt` `{@kv.thresholds.{device.id}:max}`).
- **Actions**: Publish to a NATS subject, make HTTP requests, or `respond` to the caller, with payload templates, passthrough, merge, forEach iteration, forEach filters, headers, debounce, plus HTTP-only options for retry and publish-response (publish the HTTP response back to a NATS subject).
- **Request/Reply**: `respond` returns a correlated response — the HTTP response on an HTTP trigger, or a NATS reply on a request/reply responder. An HTTP trigger can also set `request: true` on a NATS action to issue a NATS request and return the reply as the HTTP response (the HTTP↔NATS bridge).

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

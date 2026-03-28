# Rule Builder Web UI

A visual rule builder for the Rule Router platform. Build rules with a guided form, preview the generated YAML in real time, and optionally push directly to a NATS KV bucket.

## Features

- **Guided Form**: Step-by-step rule creation — Trigger, Conditions, Action — with type-specific fields that appear as needed.
- **Live YAML Preview**: See the generated YAML update in real time as you fill in the form.
- **Multi-Rule Support**: Build multiple rules, group them into separate files, or combine into one.
- **Per-File Organization**: Assign filenames to rules — rules with the same filename group into one YAML output. Download or push individual files or all at once.
- **KV Push**: Push rules directly to a NATS KV bucket via WebSocket. Authenticate with a `.creds` file. Connection settings are saved in session storage.
- **KV Pull**: Load existing rules from a NATS KV bucket, edit them in the form, and push back.
- **Client-Side Validation**: Validates rules as you type — trigger subjects, cron expressions, HTTP paths, condition operators, and more.
- **Dark Mode**: Follows system preference automatically, with a manual toggle (System / Light / Dark) that persists to local storage.
- **Responsive**: Two-panel layout on desktop; bottom drawer for YAML preview on mobile.

## Quick Start

```bash
cd web
npm install
npm run dev
```

Open http://localhost:5173 in your browser.

## Build for Production

```bash
npm run build
```

Static files are output to `dist/`. Serve them with any static file server.

## Tech Stack

- [Vue 3](https://vuejs.org/) — Composition API with single-file components
- [Vite](https://vitejs.dev/) — Build tool and dev server
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

# Troubleshooting

Common failure modes and how to diagnose them. Most issues show up as "the rule didn't fire" or "the value I expected isn't there"; the trick is knowing which layer dropped it.

## Rule doesn't fire

When a rule that should match doesn't produce its action, work down this list in order.

### 1. Is the trigger subject or path right?

**NATS triggers**: Subscribe to the same subject manually to confirm messages are arriving.

```bash
nats sub "sensors.temperature.>"
```

Common mistakes:
- Wildcard mismatch: `sensors.*` matches `sensors.temperature` but not `sensors.temperature.room1` (use `>` for multi-level).
- Trailing dots, typos, case sensitivity (NATS subjects are case-sensitive).
- The publishing service is using a different subject than you expect.

**HTTP triggers**: Confirm the path matches exactly. The gateway uses exact-path matching by default (not prefix matching). Check the method too — many webhook providers POST, but some PUT.

### 2. Does the stream exist?

Router rules require a JetStream stream that captures the trigger subject. If no stream exists, the rule loads but its subscription is skipped with a warning.

```bash
nats stream ls
nats stream info <stream_name>
```

Look in the application logs at startup for messages like `"no stream found for subject"` — that's the diagnostic.

### 3. Are conditions evaluating as expected?

Use `rule-cli check` to evaluate a rule against a sample message offline:

```bash
echo '{"temperature": 35}' | rule-cli check rules/sensors/temp.yaml --subject sensors.temperature
```

This runs the real rule engine and shows whether each condition passed or failed and what the final action would be. Most subtle bugs (type mismatch, missing field, wrong operator) surface here.

### 4. Is a throttle suppressing it?

If a rule has a `throttle` block (or the deprecated `debounce` spelling), a leading-mode window fires the first message immediately and silently skips the rest. Check the key — without one, all messages share a single window per rule.

The `throttle_suppressed_total` Prometheus metric counts leading-mode suppressions (labeled by `phase` = `trigger` | `action`).

Two specific traps:

- **A trigger throttle on a rule with conditions.** The window is consumed by whichever message arrives first, match or not, and later messages are never evaluated. A reading that would have alerted gets dropped because a boring one got there first. Move the throttle to the action.
- **`mode: trailing` looks like nothing fired.** It did not fire *yet* — the action is held until the window closes. Watch `throttle_deferred_total{outcome="emitted"}` rather than expecting an immediate publish.

## KV lookup returns empty

`{@kv.bucket.key:path}` resolving to an empty string usually means one of:

1. **Bucket isn't listed in config.** Confirm the bucket is under `kv.buckets`. If you're using local caching (`kv.localCache.enabled`), the bucket also needs to be cacheable.
2. **Key doesn't exist** in the bucket. `nats kv get <bucket> <key>` to check.
3. **JSON path syntax is off.** The colon separates the key from the path: `bucket.{var}:json.path`. A bucket name or key with dots is fine — the colon is what divides them.
4. **The KV value isn't JSON** but you used a JSON path. Without a colon, the whole value is returned as a string.
5. **Template substitution inside the key didn't resolve.** If `{customer_id}` isn't in the message, `{@kv.customers.{customer_id}:tier}` becomes `{@kv.customers.:tier}` and looks up an empty key.

For conditions that depend on KV values, missing values evaluate to empty strings — which usually fails the comparison silently. Add an `exists` check first if missing-vs-mismatched matters:

```yaml
- field: "{@kv.customers.{customer_id}:tier}"
  operator: exists
- field: "{@kv.customers.{customer_id}:tier}"
  operator: eq
  value: "premium"
```

## HTTP retry exhausted

When an outbound HTTP action runs out of retries:

- The failure is logged with the final status code or transport error.
- For gateway outbound rules (NATS-triggered), the message is NACKed and JetStream redelivers per the stream's policy. The action will be retried as a new delivery, restarting the in-action retry budget.
- For scheduler rules, exhaustion is terminal for that firing — the next cron tick is a fresh attempt.

Common causes:
- `maxDelay` too low for a slow upstream — the backoff caps before reaching a useful delay.
- Auth token expired — check whether `nats-auth-manager` is keeping the token current (logs from that binary).
- Network egress blocked — the application logs distinguish transport errors from HTTP error responses.

The `http_outbound_requests_total{status_code}` metric counts outbound requests by HTTP status code, and `http_outbound_duration_seconds` tracks their latency.

## Synchronous route returns 404 / 503 / 504

Synchronous gateway routes (an HTTP rule with a `respond` action, or a `nats` action with `request: true`) return real status codes instead of fire-and-forget's `200`:

- **`404 Not Found`** — either no rule matches the path/method at all, **or** the path has a synchronous rule but its conditions did not match this request (so no `respond`/`request` action fired). Use `rule-cli check` to confirm the conditions pass for your sample request. Remember: a path is only handled synchronously when the matched rule actually has a `respond` or `request: true` action — otherwise it stays fire-and-forget and returns `200`.
- **`503 Service Unavailable`** (bridge only) — `nc.Request` found **no responder** on the subject. Confirm a responder is subscribed: for a rule-router responder, the rule needs `reply: true` and runs under `features.router`; check it loaded (`nats req <subject> ''` should answer). Note: `503` is also returned for a full inbound work queue on *non*-synchronous routes — distinguish by whether the route is synchronous.
- **`504 Gateway Timeout`** (bridge only) — a responder exists but didn't reply within `timeout` (default `5s`). Raise the rule's `timeout`, or speed up the responder.

Two related gotchas:
- **Reply cut off / connection reset on the bridge:** the HTTP server's `writeTimeout` must exceed the bridge `timeout`. If `writeTimeout` is shorter, the connection is closed before the reply is written. Size `http.server.writeTimeout` above the largest bridge `timeout`.
- **Reply never arrives on a NATS responder:** a `reply: true` rule must have a `respond` action (enforced at load) and is served over **core NATS**, not JetStream — so it needs no stream, but the requester must use request/reply (`nats req`, `nc.Request`), not a plain publish.

## forEach drops elements

A `forEach` over an array longer than `forEach.maxIterations` (default 100) processes the first N and silently drops the rest.

- Check the `foreach_iterations_total` metric against the array size you expect.
- Raise the limit in config if needed (`forEach.maxIterations: 1000`).
- Hard ceiling is 10000 — set to `0` to disable, but be cautious.

## Throttle state resets after reload

A SIGHUP reload or a KV-driven rule update rebuilds the rule processor, which clears all in-memory **leading-mode** throttle state. If you reload and a rule fires immediately on the next matching message — even though you expected the window to still be active — that's why.

There is no on-disk throttle persistence. If you need durable suppression across reloads, use a KV-backed counter or last-fired timestamp instead.

**Trailing-mode** pending batches are unaffected: they live in the executing layer, not the processor, so a reload does not disturb them and they still fire when their window closes. They are already fully evaluated at that point, so a rule change mid-window does not change what gets emitted.

## A trailing-mode action never fired

Trailing mode is at-most-once by construction. The triggering message is ACKed when its window *opens*, not when the action fires — otherwise the ack would expire mid-window and JetStream would redeliver the message. So:

- A crash or `SIGKILL` inside the window loses the pending batch. Nothing redelivers it.
- A clean shutdown (SIGTERM) flushes pending batches instead of dropping them, bounded by the shutdown grace period. Anything that could not be flushed in time is logged and counted as `throttle_deferred_total{outcome="dropped"}`.

If losing the settled value is unacceptable, use leading mode (which fires before the ack) or persist the value to KV and read it on a schedule.

## Signature verification fails

When `{@signature.valid}` is `false`:

1. **Headers missing.** The publisher must set `Nats-Public-Key` and `Nats-Signature`. Default header names are configurable in `security.verification`.
2. **Wrong payload signed.** The signature must be over the raw NATS message payload, byte-for-byte. JSON re-serialization at the publisher (changing key order or whitespace) will invalidate the signature.
3. **Wrong key encoding.** The public key must be the NKey public key string; the signature must be Base64-encoded.
4. **Replay protection failing separately.** A valid signature on a stale message will still fail a `recent` check on a timestamp condition.

See [07 Security](./07-security.md) for the full signing workflow.

## Where to look in logs

The application uses structured slog output. Key log fields when triaging:

| Field | Meaning |
|-------|---------|
| `rule_file` | Which rule file (or KV key) the message hit |
| `subject` / `path` | The NATS subject or HTTP path of the trigger |
| `condition_result` | Whether conditions passed (debug level) |
| `error` | Underlying cause for failed actions |

Set `logging.level: debug` temporarily when triaging a non-firing rule — it logs condition evaluation results, which is the most useful signal.

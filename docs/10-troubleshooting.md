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
- A `{template}` in the trigger subject or path. Templates are never expanded in triggers, so this is rejected at load with an error that suggests the wildcard rewrite (`events.location.*` with `{@subject.2}`).

**HTTP triggers**: Confirm the path matches. A path without wildcards matches exactly (not as a prefix); use `*` or a trailing `>` for wildcard paths. Check the method too — many webhook providers POST, but some PUT.

### 2. Does the stream exist?

NATS triggers in the default JetStream mode require a stream that captures the trigger subject (`mode: core` and `reply: true` triggers do not). A missing stream does not leave a silently dead rule — it is rejected:

- **File-based rules:** startup fails — `stream validation failed: cannot map N subject(s) to streams` (router), or `no stream found for subject` (a gateway outbound rule) — and the error lists the available stream filters.
- **KV rule store:** the whole key is rejected (`stream validation failed, rejecting all rules for this key`, error `no stream found for trigger subject ...`) and that key's previous rules stay in effect.

```bash
nats stream ls
nats stream info <stream_name>
```

### 3. Are conditions evaluating as expected?

Use `rule-cli check` to evaluate a rule against a sample message offline:

```bash
echo '{"temperature": 35}' > msg.json
rule-cli check --rule rules/sensors/temp.yaml --message msg.json --subject sensors.temperature
```

This runs the real rule engine and shows whether the rule matched and the fully rendered action(s). It does not break the result down per condition, so to find the failing one, remove conditions or change the sample until the outcome flips. Most subtle bugs (type mismatch, missing field, wrong operator) surface here. In a multi-rule file, `-n <index>` checks that rule alone — its siblings are not loaded, so one of them matching cannot pass for the rule you picked.

One trap that looks like a broken condition: **`neq`, `not_in`, and `not_contains` are `false` on a missing field**, not true. "Status is not `active`" does not match a message with no `status` at all. To act on absence, use `not_exists` — it is the only operator that does.

### 4. Is a throttle suppressing it?

If a rule has a `throttle` block (or the deprecated `debounce` spelling), a leading-mode window fires the first message immediately and silently skips the rest. Check the key — without one, all messages share a single window per rule.

The `throttle_suppressed_total` Prometheus metric counts leading-mode suppressions (labeled by `phase` = `trigger` | `action`).

Two specific traps:

- **A trigger throttle on a rule with conditions.** The window is consumed by whichever message arrives first, match or not, and later messages are never evaluated. A reading that would have alerted gets dropped because a boring one got there first. Move the throttle to the action.
- **`mode: trailing` looks like nothing fired.** It did not fire *yet* — the action is held until the window closes. Watch `throttle_deferred_total{outcome="emitted"}` rather than expecting an immediate publish.

## Form-encoded webhook: every field is empty

A form body reaches the rule as fields only when the request carries `Content-Type: application/x-www-form-urlencoded`. Without that header the whole body is treated as one raw string, so `{device_id}` renders empty and any condition on it fails. Confirm what the sender is actually sending — some devices omit the header, and there is no sniffing fallback by design.

Reproduce it offline both ways:

```bash
printf 'device_id=9876&user_id=42' > body.form

# Decoded into fields
rule-cli check --rule idface.yaml --message body.form \
  --header 'Content-Type: application/x-www-form-urlencoded'

# Same bytes, no header — one raw string, conditions fail
rule-cli check --rule idface.yaml --message body.form
```

Two adjacent symptoms:

- **The rendered payload is invalid JSON.** An unresolved variable renders as an empty string, so `"card": {card_value}` becomes `"card": ` when the sender omits that field. Quote every optional scalar: `"card": "{card_value}"`. An optional array or object cannot be quoted — split the rule on `exists` / `not_exists` instead ([09 Patterns — Writing to KV](./09-patterns.md#11-writing-to-kv-event--state)).
- **`failed to decode form payload` / `parsing form body` in the logs.** The body has a malformed percent-escape. This is rejected rather than partially parsed on purpose — a silently dropped field reads as absent and can flip a condition instead of raising an error. On a fire-and-forget route the sender has already received its `200`, so the log line is the only trace; a synchronous route returns `500`.

## Query parameter is always empty

Check three things, in this order:

1. **Case.** Query names are matched verbatim — `{@query.Tenant}` will not find `?tenant=acme`. (Headers are the opposite; those are case-insensitive.)
2. **Namespace.** Query values are *only* reachable via `{@query.name}`. They are deliberately never merged into the message, so `{tenant}` reads the body, not the URL.
3. **You are testing with a query string at all.** `rule-cli check` sends none unless you pass `--query 'tenant=acme'`; the web tester needs its **Query Params** field filled in.

Note that a query string never affects *routing* — if the path matches, the rule fires regardless of the query, and a rule cannot be selected by query parameter.

## KV lookup returns empty

`{@kv.bucket.key:path}` resolving to an empty string usually means one of:

1. **Bucket isn't listed in config.** Confirm the bucket is under `kv.buckets`. If you're using local caching (`kv.localCache.enabled`), the bucket also needs to be cacheable.
2. **Key doesn't exist** in the bucket. `nats kv get <bucket> <key>` to check.
3. **JSON path syntax is off.** The colon separates the key from the path: `bucket.{var}:json.path`. The bucket name ends at the first dot, so a key with dots is fine — the colon is what divides key from path.
4. **The KV value isn't JSON** but you used a JSON path. Without a colon, the whole value is returned as a string.
5. **Template substitution inside the key didn't resolve.** If `{customer_id}` isn't in the message, `{@kv.customers.{customer_id}:tier}` becomes `{@kv.customers.:tier}` and looks up an empty key.

In a payload template a missing KV value renders as an empty string. In a condition it counts as missing, so every comparison on it is `false` — silently. Add an `exists` check first if missing-vs-mismatched matters, or a `not_exists` rule to handle the miss explicitly:

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
- The retry policy outlasts its deadline — `nats.consumers.ackWaitTimeout` (default `30s`) for a JetStream-triggered rule, 30 seconds for a core-mode trigger, 10 seconds per action for a schedule rule. Remaining attempts are abandoned with `context cancelled during retry`.
- `maxDelay` too low for a slow upstream — the backoff caps before reaching a useful delay.
- Auth token expired — check whether `nats-auth-manager` is keeping the token current (logs from that binary).
- Network egress blocked — the application logs distinguish transport errors from HTTP error responses.

The `http_outbound_requests_total{status_code}` metric counts outbound requests by HTTP status code, and `http_outbound_duration_seconds` tracks their latency.

## Synchronous route returns 404 / 503 / 504

Synchronous gateway routes (an HTTP rule with a `respond` action, or a `nats` action with `request: true`) return real status codes instead of fire-and-forget's `200`:

- **`404 Not Found`** — either no rule matches the path/method at all, **or** the path has a synchronous rule but its conditions did not match this request (so no `respond`/`request` action fired). Use `rule-cli check` to confirm the conditions pass for your sample request. Remember: a path is only handled synchronously when the matched rule actually has a `respond` or `request: true` action — otherwise it stays fire-and-forget and returns `200`.
- **`503 Service Unavailable`** (bridge only) — `nc.Request` found **no responder** on the subject. Confirm a responder is subscribed: for a rule-router responder, the rule needs `reply: true` and runs under `features.router`; check it loaded (`nats req <subject> ''` should answer). Note: `503` is also returned for a full inbound work queue on *non*-synchronous routes — distinguish by whether the route is synchronous.
- **`504 Gateway Timeout`** (bridge only) — a responder exists but didn't reply within `timeout` (default `5s`). Raise the rule's `timeout`, or speed up the responder.
- **`502 Bad Gateway`** (bridge only) — the NATS request failed for another reason (for example the connection was closed). The log line `NATS bridge request failed` carries the error.
- **`500 Internal Server Error`** — the request could not be evaluated at all, typically a malformed form body (see above).

Two related gotchas:
- **Reply cut off / connection reset on the bridge:** the HTTP server's `writeTimeout` must exceed the bridge `timeout`. If `writeTimeout` is shorter, the connection is closed before the reply is written. Size `http.server.writeTimeout` above the largest bridge `timeout`.
- **Reply never arrives on a NATS responder:** a `reply: true` rule must have a `respond` action (enforced at load) and is served over **core NATS**, not JetStream — so it needs no stream, but the requester must use request/reply (`nats req`, `nc.Request`), not a plain publish.

## forEach action fails on large arrays

A `forEach` over an array longer than `forEach.maxIterations` (default 100) does not process the first N — the whole action fails for that message and generates nothing. Other rules on the same message are unaffected.

- Look for `failed to process action` with `forEach array exceeds limit: N elements > M max iterations` in the logs; `template_operations_total{status="error"}` also increments. The `foreach_*` metrics do not count it, because iteration never starts.
- Raise the limit in config if needed (`forEach.maxIterations: 1000`).
- Hard ceiling is 10000 — set to `0` to disable, but be cautious.

## Throttle state resets after reload

A SIGHUP reload rebuilds the rule processor, which clears all in-memory **leading-mode** throttle state. If you reload and a rule fires immediately on the next matching message — even though you expected the window to still be active — that's why. With the KV rule store, writing a key clears the throttle state of that key's rules, including a re-push of unchanged content; rules under other keys are not affected. When the KV watcher reconnects after an outage it re-reads every key, which clears it for all of them.

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

## `NATS asynchronous error` with `kind=permissions_violation`

The server refused a publish or subscribe for this process's credential. The log line's `error` names the subject; `nats_async_errors_total{kind="permissions_violation"}` counts them. A denied publish never acks, so this line is the only signal.

If the subject is under `$SRV.` and the lines appear at startup and after every reconnect, the credential lacks the permissions for [service discovery](./12-observability.md#service-discovery): subscribe on `$SRV.>` and publish replies to the requester's inbox. Routing is unaffected — the process just is not listed by `nats micro ls`. Grant the permissions or expect the lines.

## Where to look in logs

The application uses structured slog output. Key log fields when triaging:

| Field | Meaning |
|-------|---------|
| `subject` / `path` | The NATS subject or HTTP path of the trigger |
| `ruleIndex` | Which rule was evaluated (`evaluating rule`, debug level) — its position among all loaded rules, not within one file. With the KV rule store it is a number assigned when the rule loads, kept until its key is written again. |
| `field` / `operator` | A condition whose field could not be resolved (`failed to resolve condition field`, warn level) |
| `variable` | A template variable that rendered empty (`template variable did not resolve; rendering empty`, debug level) |
| `key` | The KV rule key (KV rule store messages) or the throttle key (`... throttle suppressed rule`, debug level) |
| `error` | Underlying cause for failed actions |

Set `logging.level: debug` temporarily when triaging a non-firing rule — it shows which rules were evaluated, throttle suppressions, and template variables that did not resolve. It does not log each condition's pass/fail result; use `rule-cli check` to test conditions against a sample message.

# Security: Message & Webhook Verification

The platform supports two payload-authentication mechanisms, each suited to a different source:

- **NKey signature verification** — for **NATS publishers you control**. The publisher signs the payload with its Ed25519 NKey; rules read the outcome via `{@signature.*}` condition variables. Covered first, below.
- **Inbound webhook HMAC** — for **third-party HTTP webhooks** (GitHub, Shopify, …) that sign the raw body with a shared-secret HMAC. The gateway verifies it as a fail-closed gate *before* the rule fires. See [Inbound Webhook HMAC Verification](#inbound-webhook-hmac-verification).

Both ensure a payload came from a trusted source and was not tampered with in transit. The difference is the trust model: NKeys are public-key signatures from a sender that holds a private seed; HMAC is a pre-shared secret you exchange with the provider out of band.

## NKey Signature Verification

The platform supports cryptographic message verification using NATS NKeys, enabling secure workflows like privileged command execution and non-repudiable event logging.

This feature ensures that a message was sent by a trusted publisher and that its payload has not been tampered with in transit.

### How It Works

1.  **Publisher Side**:
    *   The publisher signs the raw message payload using its private NKey (seed).
    *   It attaches two headers to the NATS message:
        *   `Nats-Public-Key`: The publisher's public NKey.
        *   `Nats-Signature`: The Base64-encoded signature.

2.  **Rule Engine Side**:
    *   The rule-router (router or gateway feature) receives the message.
    *   When a rule condition references a `{@signature.*}` variable, the engine automatically performs verification.
    *   It uses the public key from the header to verify the signature against the raw message payload.
    *   The results (`true`/`false` and the public key) are made available to the rule.

### Configuration

To enable this feature, you must set `security.verification.enabled: true` in your `config.yaml` file.

```yaml
security:
  verification:
    # Enable cryptographic signature verification
    enabled: true
    
    # Header containing signer's NKey public key
    publicKeyHeader: "Nats-Public-Key"
    
    # Header containing Base64-encoded Ed25519 signature
    signatureHeader: "Nats-Signature"
```

### Rule Variables

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@signature.valid}` | Whether signature verification passed | `true` / `false` |
| `{@signature.pubkey}` | Signer's public key | `UDXU4RCRBVXEZ...` |

### Example: Secure Command Execution

This rule demonstrates a complete secure workflow: signature verification, replay protection, and KV-based authorization.

**Scenario**: A command to unlock a door, which must be authenticated and authorized.

**1. KV Store Setup (Authorization)**
First, we store the public keys of authorized users in a NATS KV store.

```bash
# Create the access control bucket
nats kv add access_control

# Add an authorized user (Alice)
nats kv put access_control UDXU4RCRBVXEZ... \
  '{"active": true, "doors": ["front", "back"], "name": "alice"}'
```

**2. The Rule**
The rule combines multiple checks for a robust security posture.

```yaml
- trigger:
    nats:
      subject: "cmds.door.unlock"
  conditions:
    operator: and
    items:
      # 1. Verify the cryptographic signature is valid
      - field: "{@signature.valid}"
        operator: eq
        value: true
      
      # 2. Prevent replay attacks (message must be recent)
      - field: "{timestamp}" # Unix seconds from payload
        operator: recent
        value: "5s"
      
      # 3. Check if the user's key is active in the KV store
      - field: "{@kv.access_control.{@signature.pubkey}:active}"
        operator: eq
        value: true
      
      # 4. Verify the user has access to the requested door
      - field: "{@kv.access_control.{@signature.pubkey}:doors}"
        operator: contains
        value: "{door}"
  
  action:
    nats:
      subject: "hardware.door.{door}.trigger"
      payload: |
        {
          "door": "{door}",
          "action": "unlock",
          "authorized_by": "{@kv.access_control.{@signature.pubkey}:name}",
          "timestamp": "{@timestamp()}",
          "request_id": "{@uuid7()}"
        }
```

### Breakdown of Conditions

1.  `@signature.valid`: This is the core check. If the signature is invalid, the rule fails immediately.
2.  `recent`: This operator checks that the `timestamp` field in the message payload is within the last 5 seconds. This prevents an attacker from capturing a valid message and replaying it later.
3.  `@kv...:active`: This check looks up the signer's public key in the `access_control` KV bucket and ensures their account is active.
4.  `@kv...:doors`: This check verifies that the signer is authorized to operate the specific door requested in the message payload.

This layered approach provides defense-in-depth for critical operations.

## Inbound Webhook HMAC Verification

Third-party webhook providers (GitHub, Shopify, …) authenticate requests with an **HMAC of the raw request body** under a shared secret, carried in a header. Unlike NKey verification — which is a *rule condition* a NATS publisher opts into — webhook HMAC is a **fail-closed gate** on the gateway: declare an `hmac` block on an HTTP trigger and the gateway verifies the signature *before any rule fires*. A bad, missing, or unverifiable signature is rejected with `401 Unauthorized` and the rule never evaluates.

### How It Works

1.  **Provider side**: signs the raw body with `HMAC(secret, body)` and sends the result (hex or base64, sometimes with a prefix like `sha256=`) in a header.
2.  **Gateway side**: recomputes the HMAC over the exact received bytes with the configured secret and compares in constant time. The check runs in the inbound handler, ahead of JSON parsing and rule evaluation, for both fire-and-forget and synchronous routes.

This is transport authentication, not a rule condition — there is no `{@hmac.valid}` variable. Declaring the block enforces it.

### Configuration

HMAC is configured **per rule** (not globally) on the HTTP trigger:

```yaml
- trigger:
    http:
      path: "/webhooks/github/push"
      method: "POST"
      hmac:
        header: "X-Hub-Signature-256"     # required: header carrying the signature
        secret: "${GITHUB_WEBHOOK_SECRET}" # required: literal | ${ENV} | {@kv.bucket.key}
        algorithm: "sha256"               # optional: sha256 (default) | sha1
        encoding: "hex"                   # optional: hex (default) | base64
        prefix: "sha256="                 # optional: stripped from the header value
  action:
    nats:
      subject: "github.push.{repository.name}"
      passthrough: true
```

The `secret` accepts three forms, all using existing syntax: a **literal**, an **env reference** `${VAR}` (expanded at load time), or a **KV reference** `{@kv.bucket.key}` (resolved per request). Prefer `${ENV}` or KV over literals so secrets are not committed in rule files. An empty/unset secret **fails closed** (every request → `401`).

### Provider Coverage

The generic scheme covers the common case:

| Provider | `header` | `algorithm` | `encoding` | `prefix` |
|----------|----------|-------------|------------|----------|
| GitHub | `X-Hub-Signature-256` | `sha256` | `hex` | `sha256=` |
| Shopify | `X-Shopify-Hmac-Sha256` | `sha256` | `base64` | — |
| Generic | (your header) | `sha256`/`sha1` | `hex`/`base64` | (optional) |

**Not covered:** timestamp-signed schemes that sign `timestamp.body` and enforce a replay window — Stripe (`Stripe-Signature: t=…,v1=…`) and Slack (`v0:ts:body`). Verify those in a downstream service.

### Observability

Outcomes are exported as `webhook_hmac_verifications_total{result}` with `result` ∈ `valid` / `invalid` / `missing` / `error` (`error` = misconfiguration such as an empty secret or unknown algorithm). Rejections also surface as `http_inbound_requests_total{status="401"}`.

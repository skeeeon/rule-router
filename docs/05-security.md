# Security: Cryptographic Signature Verification

The platform supports cryptographic message verification using NATS NKeys, enabling secure workflows like privileged command execution and non-repudiable event logging.

This feature ensures that a message was sent by a trusted publisher and that its payload has not been tampered with in transit.

## How It Works

1.  **Publisher Side**:
    *   The publisher signs the raw message payload using its private NKey (seed).
    *   It attaches two headers to the NATS message:
        *   `Nats-Public-Key`: The publisher's public NKey.
        *   `Nats-Signature`: The Base64-encoded signature.

2.  **Rule Engine Side**:
    *   The `rule-router` or `http-gateway` receives the message.
    *   When a rule condition references a `{@signature.*}` variable, the engine automatically performs verification.
    *   It uses the public key from the header to verify the signature against the raw message payload.
    *   The results (`true`/`false` and the public key) are made available to the rule.

## Configuration

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

## Rule Variables

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `{@signature.valid}` | Whether signature verification passed | `true` / `false` |
| `{@signature.pubkey}` | Signer's public key | `UDXU4RCRBVXEZ...` |

## Example: Secure Command Execution

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
      - field: "@signature.valid"
        operator: eq
        value: true
      
      # 2. Prevent replay attacks (message must be recent)
      - field: "timestamp" # Unix seconds from payload
        operator: recent
        value: "5s"
      
      # 3. Check if the user's key is active in the KV store
      - field: "@kv.access_control.{@signature.pubkey}:active"
        operator: eq
        value: true
      
      # 4. Verify the user has access to the requested door
      - field: "@kv.access_control.{@signature.pubkey}:doors"
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

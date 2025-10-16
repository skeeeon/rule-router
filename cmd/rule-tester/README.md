# Rule Tester (`rule-tester`)

The `rule-tester` is a command-line utility for writing, testing, and validating your rules in a fast, offline environment. It allows you to verify the logic of your rules—including conditions, templates, and dependencies like KV and time—without needing a running NATS server or live HTTP endpoints.

This tool is essential for maintaining a high-quality, reliable ruleset and integrating rule validation into a modern CI/CD workflow for both the `rule-router` and `http-gateway` applications.

-----

## Features

*   **Unified Testing**: Test rules for both NATS and HTTP triggers/actions with a single tool.
*   **Trigger-Aware Scaffolding**: Automatically generates test configurations tailored to your rule's specific trigger type (NATS or HTTP).
*   **Linting**: Quickly validate the YAML syntax and structure of all rule files.
*   **Batch Testing**: Run a full suite of tests for your entire ruleset, based on a simple, convention-over-configuration directory structure.
*   **Dependency Mocking**: Isolate your tests by providing mock data for NATS KV stores, mock timestamps for time-based rules, and mock signature verification results.
*   **Output Validation**: Guarantee the correctness of your templates by comparing the rendered action (NATS subject/payload or HTTP URL/method/payload) against an expected output.
*   **Parallel Execution**: Run tests in parallel for faster feedback.
*   **CI/CD Friendly**: Multiple output formats (human-readable or JSON) for easy integration.

-----

## Testing Workflow

### 1. Scaffolding Your Tests

Start by using the `--scaffold` command on your new rule file. The tool will smartly inspect your rule's trigger and generate a pre-populated configuration to accelerate your test setup.

**Command:**
```bash
rule-tester --scaffold ./rules/my-new-rule.yaml
```

**Result:**
The tool creates a `_test` directory alongside your rule and populates it with a configuration file and placeholders for matching and non-matching test cases.

```
rules/
├── my-new-rule.yaml
└── my-new-rule_test/
    ├── _test_config.json  # Pre-populated with the correct mock trigger
    ├── match_1.json       # Placeholder for a message that SHOULD match
    └── not_match_1.json   # Placeholder for a message that SHOULD NOT match
```

**Example Generated `_test_config.json` (NATS Trigger):**
```json
{
  "mockTrigger": {
    "nats": {
      "subject": "sensors.temperature"
    }
  },
  "mockTime": "2025-10-16T05:30:00Z",
  "headers": {}
}
```

**Example Generated `_test_config.json` (HTTP Trigger):**
```json
{
  "mockTrigger": {
    "http": {
      "path": "/webhooks/github",
      "method": "POST"
    }
  },
  "mockTime": "2025-10-16T05:30:00Z",
  "headers": {}
}
```

### 2. Writing a Basic Test

Edit the generated JSON files with your sample message data. The filename itself declares the expected outcome.

*   `match_*.json`: The tester will assert that the rule **must** match.
*   `not_match_*.json`: The tester will assert that the rule **must not** match.

### 3. Running the Tests

Run the batch tester from the root of your project. It will automatically discover and run all tests for both NATS and HTTP rules.

**Command:**
```bash
rule-tester --test --rules ./rules
```

**Output:**
```
▶ RUNNING TESTS in ./rules/

=== RULE: rules/my-nats-rule.yaml ===
  ✔ match_1.json (5ms)
  ✔ not_match_1.json (3ms)

=== RULE: rules/my-http-rule.yaml ===
  ✔ match_webhook.json (8ms)

--- SUMMARY ---
Total Tests: 3, Passed: 3, Failed: 0
Duration: 21ms
```

-----

## Advanced Testing

### Output Validation

To ensure your templates are rendered correctly, provide an "expected output" file. For a test message named `match_case.json`, create `match_case_output.json`.

**Example for a NATS Action:**
*   **`match_high_temp_output.json`**:
    ```json
    {
      "subject": "alerts.us-west",
      "payload": {
        "temp": 40
      }
    }
    ```

**Example for an HTTP Action:**
*   **`match_critical_alert_output.json`**:
    ```json
    {
      "url": "https://api.pagerduty.com/incidents",
      "method": "POST",
      "payload": {
        "incident": {
          "title": "High temperature alert for device-123"
        }
      },
      "headers": {
        "Authorization": "Token secret-token-value"
      }
    }
    ```

### Mocking Dependencies

The `_test_config.json` file controls the entire simulated environment for a test suite.

*   **`mockTrigger`**: (Required) Defines the event that triggers the rule. Must contain either a `nats` or `http` block.
*   **`mockTime`**: (Optional) An RFC3339 timestamp. All time-based conditions (`@time.hour`, `{@timestamp()}`) will be evaluated against this fixed time.
*   **`headers`**: (Optional) A map of key-value strings to simulate incoming NATS or HTTP headers.
*   **`mockSignature`**: (Optional) A block to mock the result of `@signature.valid` and `@signature.pubkey` for testing security rules.
*   **`mock_kv_data.json`**: (Optional) Create this file in your test directory to provide mock data for KV lookups. The top-level keys are bucket names.

**Example `_test_config.json` with Signature Mocking:**
```json
{
  "mockTrigger": {
    "nats": { "subject": "cmds.door.unlock" }
  },
  "mockSignature": {
    "valid": true,
    "publicKey": "UDXU4RCRBVXEZ..."
  }
}
```

-----

## Full CLI Reference

| Flag | Description |
|---|---|
| `--lint` | Validate the syntax of all `*.yaml` rule files. |
| `--scaffold <path>` | Generate a test directory for a given rule file. |
| `--test` | Run all test suites found in a directory. |
| `--rule <path>` | Path to a single rule file for Quick Check mode. |
| `--message <path>` | Path to a single message file for Quick Check mode. |
| `--subject <subj>` | **(NATS Only)** Manually specify a subject for Quick Check mode. |
| `--kv-mock <path>` | Path to mock KV data file for Quick Check mode. |
| `--output <format>` | Output format: `pretty` (default) or `json`. |
| `--verbose` | Show detailed output for failures. |
| `--parallel <N>` | Number of parallel test workers (default: 4, 0 = sequential). |
| `--no-overwrite` | Prevent scaffold from overwriting an existing test directory. |

-----

## Examples

### Example 1: NATS → NATS Rule (Updated Format)

**Rule (`rules/temperature_alert.yaml`):**
```yaml
- trigger:
    nats:
      subject: "sensors.temperature"
  conditions:
    - field: "value"
      operator: gt
      value: 30
  action:
    nats:
      subject: "alerts.high-temp"
      payload: '{"temp": {value}}'
```

**Test Config (`.../_test_config.json`):**
```json
{
  "mockTrigger": {
    "nats": {
      "subject": "sensors.temperature"
    }
  }
}
```

### Example 2: HTTP → NATS Rule (Inbound Webhook)

**Rule (`rules/github_webhook.yaml`):**
```yaml
- trigger:
    http:
      path: "/webhooks/github"
      method: "POST"
  conditions:
    - field: "@header.X-GitHub-Event"
      operator: eq
      value: "pull_request"
  action:
    nats:
      subject: "github.events.pr"
      payload: '{"repo":"{repository.name}","pr":{number}}'
```

**Test Config (`.../_test_config.json`):**
```json
{
  "mockTrigger": {
    "http": {
      "path": "/webhooks/github",
      "method": "POST"
    }
  },
  "headers": {
    "X-GitHub-Event": "pull_request"
  }
}
```

**Test Case (`.../match_pr.json`):**
```json
{
  "repository": { "name": "rule-router" },
  "number": 42
}
```

**Expected Output (`.../match_pr_output.json`):**
```json
{
  "subject": "github.events.pr",
  "payload": {
    "repo": "rule-router",
    "pr": 42
  }
}
```

### Example 3: NATS → HTTP Rule (Outbound Webhook)

**Rule (`rules/pagerduty_alert.yaml`):**
```yaml
- trigger:
    nats:
      subject: "alerts.critical.>"
  action:
    http:
      url: "https://api.pagerduty.com/incidents"
      method: "POST"
      payload: '{"incident":{"title":"{message}"}}'
```

**Test Config (`.../_test_config.json`):**
```json
{
  "mockTrigger": {
    "nats": {
      "subject": "alerts.critical.database"
    }
  }
}
```

**Test Case (`.../match_db_alert.json`):**
```json
{
  "message": "Database connection lost"
}
```

**Expected Output (`.../match_db_alert_output.json`):**
```json
{
  "url": "https://api.pagerduty.com/incidents",
  "method": "POST",
  "payload": {
    "incident": {
      "title": "Database connection lost"
    }
  }
}
```

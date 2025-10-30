# Rule CLI (`rule-cli`)

The `rule-cli` is a powerful command-line utility for creating, testing, and validating your rules in a fast, offline environment. It is the primary tool for managing the lifecycle of rules for both the `rule-router` and `http-gateway` applications.

This tool streamlines the developer workflow by providing both rapid, template-based scaffolding for common patterns and a flexible, interactive builder for custom rules. It is essential for maintaining a high-quality, reliable ruleset and integrating rule validation into a modern CI/CD workflow.

-----

## Features

*   **Interactive Rule Builder**: A step-by-step wizard that guides you through creating complex rules, including nested conditions and `forEach` actions.
*   **Template-Based Scaffolding**: Quickly generate rules for common use cases like NATS-to-NATS routing, batch processing, KV enrichment, and HTTP webhooks.
*   **Unified Testing**: Test rules for both NATS and HTTP triggers/actions with a single tool.
*   **Full `forEach` Support**: Intelligently scaffolds, tests, and validates rules that use `forEach` for batch processing, including multi-action output validation.
*   **Primitive Message Support**: Test rules with strings, numbers, and arrays at the root.
*   **Linting**: Quickly validate the YAML syntax and structure of all rule files.
*   **Batch Testing**: Run a full suite of tests for your entire ruleset.
*   **Dependency Mocking**: Isolate your tests by providing mock data for NATS KV stores, mock timestamps for time-based rules, and mock signature verification results.
*   **CI/CD Friendly**: Multiple output formats (human-readable or JSON) for easy integration.
*   **Portable & Self-Contained**: A single, dependency-free binary that works on any platform.

-----

## Quick Start

### 1. Create a New Rule

You can create a rule either interactively or from a template.

**Option A: Interactive Builder (Recommended for custom rules)**

Run the `new` command without any flags to start the interactive wizard. It will guide you through creating a trigger, conditions, and an action.

```bash
./rule-cli new
```

**Option B: From a Template (Fastest for common patterns)**

First, list the available templates:

```bash
./rule-cli new --list
```
```
Available templates:
  - array-operators
  - http-forEach
  - http-inbound
  - http-outbound
  - nats-basic
  - nats-forEach
  - nats-kv-enrichment
  - signature-verification
```

Then, create a rule from a template. The CLI will automatically place it in the `rules/` directory if it exists.

```bash
# Create a basic NATS rule
./rule-cli new --template=nats-basic --output=rules/my-first-rule.yaml

# Create a rule for batch processing
./rule-cli new --template=nats-forEach --output=rules/my-batch-rule.yaml
```

### 2. Scaffold Tests

After creating a rule, use the `scaffold` command to generate a test directory. The tool will smartly inspect your rule and **automatically detect `forEach` operations** to generate appropriate array-based test examples.

```bash
./rule-cli scaffold rules/my-batch-rule.yaml
```

This creates a `rules/my-batch-rule_test/` directory with pre-populated JSON files for you to edit.

### 3. Write Test Cases

Edit the generated `*.json` files with your sample message data. The filename itself declares the expected outcome.

*   `match_*.json`: The tester will assert that the rule **must** match and generate actions.
*   `not_match_*.json`: The tester will assert that the rule **must not** match.
*   `match_*_output.json`: (Optional) Provide this file to validate the exact content of the generated action(s). For `forEach` rules, this file should contain a JSON array of expected actions.

### 4. Run Tests

Run the `test` command from the root of your project. It will automatically discover and run all test suites.

```bash
./rule-cli test --rules ./rules
```
```
▶ RUNNING TESTS in ./rules/

=== RULE: rules/my-first-rule.yaml ===
  ✓ match_1.json (5ms)
  ✓ not_match_1.json (3ms)

=== RULE: rules/my-batch-rule.yaml ===
  ✓ match_1.json (12ms)
  ✓ not_match_1.json (4ms)
  ✓ not_match_2_empty_array.json (2ms)

--- SUMMARY ---
Total Tests: 5, Passed: 5, Failed: 0
Duration: 24ms
```

-----

## Full CLI Reference

```
A CLI for creating, testing, and managing rules for the rule-router and http-gateway.

Usage:
  rule-cli [command]

Available Commands:
  check       Run a quick check of a single rule against a single message
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  lint        Validate the syntax and structure of all rule files in a directory
  new         Create a new rule from a template or interactively
  scaffold    Generate a test directory for a given rule file
  test        Run all test suites for rules in a directory

Flags:
  -h, --help   help for rule-cli
```

### `new` Command

Create new rule files.

```bash
# Start the interactive builder
rule-cli new

# List available templates
rule-cli new --list

# Show the content of a template
rule-cli new --show=nats-basic

# Create from a template
rule-cli new --template=http-inbound --output=rules/http/stripe-webhooks.yaml

# Create and automatically scaffold tests
rule-cli new --template=nats-forEach --with-tests
```

### `test` Command

Run a batch of test suites.

```bash
# Run all tests in the 'rules' directory
rule-cli test --rules ./rules

# Run with verbose output on failures
rule-cli test --rules ./rules -v

# Run tests sequentially (disable parallelism)
rule-cli test --rules ./rules --parallel=0

# Output results as JSON for CI/CD integration
rule-cli test --rules ./rules --output=json
```

### `lint` Command

Validate the syntax of all rule files.

```bash
# Lint all rules in the 'rules' directory
rule-cli lint --rules ./rules
```

### `scaffold` Command

Generate a test directory for an existing rule file.

```bash
# Scaffold tests for a rule (auto-detects forEach)
rule-cli scaffold rules/my-rule.yaml
```

### `check` Command

Perform a quick, interactive check of a single rule against a single message.

```bash
# Quick check a rule with a message file
rule-cli check --rule rules/my-rule.yaml --message test-data/message.json

# Override the NATS subject for the test
rule-cli check --rule rules/my-rule.yaml --message msg.json --subject "new.test.subject"

# Provide mock KV data for the check
rule-cli check --rule rules/kv-rule.yaml --message msg.json --kv-mock test-data/kv.json
```

-----

## Testing `forEach` and Array Rules

The `rule-cli` has first-class support for testing array operations.

*   **Scaffolding**: When you run `rule-cli scaffold` on a rule with a `forEach` action, it automatically generates:
    *   An input `match_1.json` file containing an array.
    *   An output `match_1_output.json` file containing a **JSON array** of expected actions.
    *   Edge case tests like `not_match_2_empty_array.json`.

*   **Validation**: The `test` command automatically detects whether the `_output.json` file is a single object (for standard rules) or an array (for `forEach` rules) and validates accordingly. It checks:
    *   The exact number of actions generated.
    *   The content of each action (subject, payload, headers, etc.).
    *   Correct resolution of `{@msg...}` variables from the root message.

**Example `match_1_output.json` for a `forEach` rule:**
```json
[
  {
    "subject": "alerts.critical.A1",
    "payload": {
      "alertId": "A1",
      "message": "High temp",
      "deviceId": "device-123"
    }
  },
  {
    "subject": "alerts.critical.A3",
    "payload": {
      "alertId": "A3",
      "message": "Motion detected",
      "deviceId": "device-123"
    }
  }
]
```

-----

## CI/CD Integration

The `rule-cli` is designed for automation. Use the `lint` and `test` commands in your CI/CD pipeline to guarantee the quality and correctness of your rules before deployment.

**GitHub Actions Example:**
```yaml
name: Rule Tests
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      
      - name: Build rule-cli
        run: go build -o rule-cli ./cmd/rule-cli
      
      - name: Lint Rules
        run: ./rule-cli lint --rules ./rules
      
      - name: Test Rules
        run: ./rule-cli test --rules ./rules --output json > results.json
      
      - name: Upload Test Results
        uses: actions/upload-artifact@v4
        with:
          name: test-results
          path: results.json
```

-----

## Troubleshooting

*   **"Template not found"**: Ensure the template name you provided to `--template` or `--show` matches one of the names listed by `rule-cli new --list`.
*   **"Tests failed"**: Run `rule-cli test --rules ./rules -v` to get verbose output, which will show the exact mismatch between the generated action and the expected output.
*   **Interactive mode not working**: The interactive builder uses standard input/output. Ensure you are running it in an interactive terminal session.


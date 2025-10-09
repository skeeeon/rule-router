# Rule Tester (`rule-tester`)

The `rule-tester` is a command-line utility designed to help you write, test, and validate your `rule-router` rules in a fast, offline environment. It allows you to verify the logic of your rules, including conditions, templates, and dependencies like KV and time, without needing a running NATS server.

This tool is essential for maintaining a high-quality, reliable ruleset and integrating rule validation into a modern CI/CD workflow.

-----

## Features

  * **Linting**: Quickly validate the YAML syntax and structure of all rule files.
  * **Smart Scaffolding**: Automatically generate test directories and pre-populated configuration files to accelerate test writing.
  * **Quick Check**: Interactively test a single rule against a single message for immediate feedback during development.
  * **Batch Testing**: Run a full suite of tests for your entire ruleset, based on a simple, convention-over-configuration directory structure.
  * **Dependency Mocking**: Isolate your tests by providing mock data for NATS KV stores and mock timestamps for time-based rules.
  * **Output Validation**: Guarantee the correctness of your templates by comparing the rendered action subject and payload against an expected output.
  * **Parallel Execution**: Run tests in parallel for faster feedback (configurable workers).
  * **Multiple Output Formats**: Human-readable output for development, JSON output for CI/CD integration.
  * **Progress Indication**: Real-time progress updates during test execution.
  * **Overwrite Protection**: Prevents accidental data loss when scaffolding test directories.

-----

## Testing Workflow

The `rule-tester` is designed around a simple, convention-based workflow that is both efficient and powerful.

### 1\. Scaffolding Your Tests

The easiest way to start is by using the `--scaffold` command on your new rule file. The tool will smartly inspect your rule and generate a pre-populated configuration to accelerate your test setup.

**Command:**

```bash
rule-tester --scaffold ./rules/my-new-rule.yaml
```

**Result:**
The tool creates a `_test` directory alongside your rule and populates it with a configuration file and placeholders, guiding you to create both a matching and non-matching test case.

```
rules/
├── my-new-rule.yaml
└── my-new-rule_test/
    ├── _test_config.json  # Pre-populated with subject and mock time
    ├── match_1.json       # Placeholder for a message that SHOULD match
    └── not_match_1.json   # Placeholder for a message that SHOULD NOT match
```

If your rule has a concrete subject (e.g., `sensors.data`), it will be automatically added to `_test_config.json`. If it uses a wildcard (e.g., `sensors.*`), a placeholder will be added.

**Overwrite Protection:**
If a test directory already exists, the scaffolder will prompt you before overwriting to prevent accidental data loss. You can use the `--no-overwrite` flag to disable this behavior.

```bash
# If test directory exists, you'll be prompted
$ rule-tester --scaffold ./rules/my-rule.yaml
⚠️  Test directory already exists: rules/my-rule_test
   Overwrite? (y/N): y

# Skip the prompt and fail if the directory exists
$ rule-tester --scaffold ./rules/my-rule.yaml --no-overwrite
Error: test directory already exists: rules/my-rule_test (use without --no-overwrite to proceed)
```

### 2\. Writing a Basic Test

Edit the generated JSON files with your sample message data. The filename itself declares the expected outcome.

  * `match_*.json`: The tester will assert that the rule **must** match. The test fails if it doesn't.
  * `not_match_*.json`: The tester will assert that the rule **must not** match. The test fails if it does.

**Test Naming Conventions:**
```
✅ Good names (descriptive):
   match_high_temp.json
   match_business_hours.json
   match_premium_customer.json
   not_match_weekend.json
   not_match_low_value.json

⚠️  Acceptable (but less clear):
   match_1.json
   match_2.json

❌ Invalid (wrong prefix):
   MATCH_test.json  (case-sensitive)
   test_match.json  (wrong order)
```

### 3\. Running the Tests

Run the batch tester from the root of your project. It will automatically discover and run all tests.

**Command:**

```bash
rule-tester --test --rules ./rules
```

**Output:**

```
▶ RUNNING TESTS in ./rules/

=== RULE: rules/my-new-rule.yaml ===
  ✔ match_1.json (5ms)
  ✔ not_match_1.json (3ms)

--- SUMMARY ---
Total Tests: 2, Passed: 2, Failed: 0
Duration: 12ms
```

**Parallel Execution:**
```bash
# Run with 8 parallel workers (default: 4)
rule-tester --test --rules ./rules --parallel 8

# Run sequentially (useful for debugging)
rule-tester --test --rules ./rules --parallel 0
```

**Progress Indication:**
For larger test suites, a progress indicator will be displayed during parallel execution.
```
[45/100] Tests completed... (Failed: 1)
```

-----

## Advanced Testing

For complex rules with dependencies or strict output requirements, you can add optional files to your test directory.

### Output Validation

To ensure your templates are rendered correctly, you can provide an "expected output" file. For a test message named `match_high_temp.json`, create `match_high_temp_output.json`.

The output file should contain a JSON object with the expected **subject** and **payload**.

  * **`match_high_temp.json`** (Input):
    ```json
    {"temperature": 40, "region": "us-west"}
    ```
  * **`match_high_temp_output.json`** (Expected Output):
    ```json
    {
      "subject": "alerts.us-west",
      "payload": {
        "temp": 40
      }
    }
    ```

The tester will now perform a deep comparison. The test will only pass if the rule matches AND both the rendered subject and payload are identical to the expected output.

### Mocking Dependencies

You can fully isolate your tests by mocking KV and time dependencies.

#### **Mocking Time and Subject**

The `_test_config.json` file, created during scaffolding, controls the simulated environment.

  * **`_test_config.json`**:
    ```json
    {
      "subject": "sensors.temp.room-101",
      "mockTime": "2025-10-08T17:51:23-05:00"
    }
    ```
  * `subject`: The NATS subject to simulate for the test run, which is crucial for rules using `@subject` tokens.
  * `mockTime`: An RFC3339 timestamp. All time-based conditions (`@time.hour`, etc.) will be evaluated against this fixed time.

#### **Mocking KV Data**

Create a `mock_kv_data.json` file in your test directory. The top-level keys are the bucket names. The tester will load this data into an in-memory cache for your test run.

  * **`mock_kv_data.json`**:
    ```json
    {
      "device_status": {
        "sensor-123": { "status": "active" }
      },
      "device_config": {
        "sensor-123": { 
          "threshold": 30,
          "settings": {
            "enabled": true,
            "alerts": ["email", "sms"]
          }
        }
      }
    }
    ```

**Important Notes:**
- Top-level keys are bucket names (must match buckets in your rules)
- Second-level keys are KV keys
- Values can be any valid JSON (objects, arrays, primitives, nested structures)
- Supports full JSON path traversal just like production

-----

## Full CLI Reference

### Common Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--output` | Output format: `pretty` or `json` | `pretty` |
| `--verbose` | Show detailed output for failures | `false` |
| `--parallel` | Number of parallel workers (0 = sequential) | `4` |

#### `lint`

Validates the syntax of all `*.yaml` rule files in a directory.

```bash
rule-tester --lint --rules ./rules
```

**Output:**
```
▶ LINTING rules in ./rules

✔ PASS: rules/temperature.yaml
✔ PASS: rules/pressure.yaml
✖ FAIL: rules/bad-rule.yaml
  Error: invalid operator: 'invalid_op'

Linting failed
```

#### `scaffold`

Creates a test directory and pre-populated configuration files for a given rule. Prompts before overwriting existing directories.

```bash
rule-tester --scaffold <path/to/rule.yaml>

# With overwrite protection (will fail if directory exists)
rule-tester --scaffold <path/to/rule.yaml> --no-overwrite
```

#### `test`

Runs all test suites found in a directory.

```bash
# Basic usage
rule-tester --test --rules ./rules

# With verbose output for detailed failure reports
rule-tester --test --rules ./rules --verbose

# JSON output for CI/CD
rule-tester --test --rules ./rules --output json

# Parallel execution
rule-tester --test --rules ./rules --parallel 8

# Sequential execution (useful for debugging)
rule-tester --test --rules ./rules --parallel 0
```

**JSON Output Format:**
```json
{
  "total": 10,
  "passed": 8,
  "failed": 2,
  "duration_ms": 145,
  "results": [
    {
      "file": "match_high_temp.json",
      "passed": true,
      "duration_ms": 5
    },
    {
      "file": "match_invalid.json",
      "passed": false,
      "error": "expected match=true, got match=false",
      "details": "...",
      "duration_ms": 8
    }
  ]
}
```

#### Quick Check

Runs a single rule against a single message for interactive development. It will smartly infer the subject from the rule file.

```bash
# Basic usage
rule-tester --rule <path/to/rule.yaml> --message <path/to/message.json>

# With KV mocking
rule-tester --rule <path/to/rule.yaml> \
            --message <path/to/message.json> \
            --kv-mock <path/to/mock_kv_data.json>

# With subject override (for wildcard rules)
rule-tester --rule <path/to/wildcard-rule.yaml> \
            --message <path/to/message.json> \
            --subject "sensors.specific.subject"

# With verbose output
rule-tester --rule <path/to/rule.yaml> \
            --message <path/to/message.json> \
            --verbose
```

**Example Output:**
```
✓ Loaded KV mock data: 2 bucket(s)
▶ Running Quick Check on subject: sensors.temp.room1

Rule Matched: True
Processing Time: 1.234ms

--- Rendered Action 1 ---
Subject: alerts.room1.high-temp
Payload: {"alert":"High temperature","temp":35,"room":"room1"}
-----------------------
```

-----

## CI/CD Integration

### GitHub Actions Example

```yaml
# .github/workflows/test-rules.yml
name: Test Rules

on:
  push:
    paths:
      - 'rules/**'
  pull_request:
    paths:
      - 'rules/**'

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - uses: actions/setup-go@v4
        with:
          go-version: '1.23'
      
      - name: Install rule-tester
        run: go install ./cmd/rule-tester
      
      - name: Lint rules
        run: rule-tester --lint --rules ./rules
      
      - name: Run tests
        run: rule-tester --test --rules ./rules --output json
```

### GitLab CI Example

```yaml
# .gitlab-ci.yml
test-rules:
  image: golang:1.23
  stage: test
  script:
    - go install ./cmd/rule-tester
    - rule-tester --lint --rules ./rules
    - rule-tester --test --rules ./rules --output json
  only:
    changes:
      - rules/**
```

-----

## Troubleshooting

### Common Issues

#### Test Passes Locally, Fails in CI

**Symptom:** Test works on your machine but fails in CI/CD pipeline.

**Common Causes:**
1. **Time zones**: Mock times are interpreted differently. Use UTC (`Z` suffix) or include a timezone offset to avoid ambiguity.
   ```json
   // Good:
   {"mockTime": "2025-10-08T17:51:23Z"}
   {"mockTime": "2025-10-08T12:51:23-05:00"}
   ```

2. **File paths**: Different working directories. Ensure commands are run from the project root.

3. **Missing test files**: Ensure test directories (`*_test/`) are committed to git.

#### KV Lookup Fails in Test

**Symptom:** Test fails with "KV field not found" or empty values.

**Solution:** Ensure `mock_kv_data.json` is properly formatted:
```json
{
  "bucket_name": {
    "key_name": {"your": "data"}
  }
}
```

**Debug with verbose mode:**
```bash
rule-tester --rule ./rules/my-rule.yaml \
            --message ./test.json \
            --kv-mock ./mock_kv.json \
            --verbose
```

#### Wildcard Subject Not Matching

**Symptom:** Rule with wildcard (e.g., `sensors.*`) doesn't match in quick check.

**Solution:** Provide a concrete subject using the `--subject` flag:
```bash
rule-tester --rule ./rules/wildcard-rule.yaml \
            --message ./test.json \
            --subject "sensors.room1.temp"
```

#### Output Validation Fails on Identical JSON

**Symptom:** Output validation fails even though JSON looks the same.

**Cause:** Usually extra whitespace or key ordering (though the tester canonicalizes JSON to handle ordering).

**Debug:** Use `--verbose` to see the actual vs. expected output diff:
```bash
rule-tester --test --rules ./rules --verbose
```

#### Tests Run Slowly

**Solution:** The test runner is highly optimized, but for extremely large test suites, you can increase parallel workers:
```bash
# Default is 4, try 8 or more
rule-tester --test --rules ./rules --parallel 8
```

#### Scaffold Overwrites Test Files

**Solution:** The tool now prompts before overwriting. To prevent this and fail instead, use the `--no-overwrite` flag:
```bash
rule-tester --scaffold ./rules/my-rule.yaml --no-overwrite```

-----

## Performance Tips

1. **Leverage Optimizations**: The test runner is already optimized to parse each rule file only once, making it significantly faster.
2. **Parallel Execution**: Use the `--parallel` flag for large test suites to take advantage of multiple CPU cores.
   ```bash
   rule-tester --test --rules ./rules --parallel 8
   ```
3. **CI Optimization**: Run the fast `lint` command before the `test` command to fail quickly on syntax errors.
   ```bash
   rule-tester --lint --rules ./rules || exit 1
   rule-tester --test --rules ./rules
   ```

-----

## Best Practices

### Test Organization

```
rules/
├── temperature_rule.yaml
├── temperature_rule_test/
│   ├── _test_config.json
│   ├── mock_kv_data.json
│   ├── match_high_temp.json
│   ├── match_high_temp_output.json
│   ├── match_critical.json
│   ├── not_match_normal.json
│   └── not_match_offline.json
└── pressure_rule.yaml
```

### Naming Conventions

- Use descriptive test names: `match_high_temp.json` not `match_1.json`
- Group related tests: `match_business_hours_weekday.json`, `not_match_business_hours_weekend.json`
- Use consistent prefixes: Always `match_` or `not_match_`

### Coverage

Ensure you test:
- **Happy path**: Expected conditions match
- **Edge cases**: Boundary values (e.g., threshold exactly 30)
- **Negative cases**: Conditions that should NOT match
- **Template validation**: Use output files for critical rules
- **KV dependencies**: Mock all required KV data
- **Time dependencies**: Test different times of day/week

-----

## Examples

### Example 1: Basic Rule Test

**Rule** (`rules/temperature.yaml`):
```yaml
- subject: sensors.temperature
  conditions:
    operator: and
    items:
      - field: value
        operator: gt
        value: 30
  action:
    subject: alerts.high-temp
    payload: '{"temp": {value}}'
```

**Test** (`rules/temperature_test/match_high.json`):
```json
{"value": 35}
```

**Test** (`rules/temperature_test/not_match_low.json`):
```json
{"value": 25}
```

### Example 2: KV-Dependent Rule Test

**Rule** (`rules/customer_alert.yaml`):
```yaml
- subject: orders.created
  conditions:
    operator: and
    items:
      - field: "@kv.customer_data.{customer_id}:tier"
        operator: eq
        value: "premium"
  action:
    subject: alerts.premium-order
    payload: '{"customer": "{customer_id}"}'
```

**Mock KV** (`rules/customer_alert_test/mock_kv_data.json`):
```json
{
  "customer_data": {
    "cust-123": {"tier": "premium"},
    "cust-456": {"tier": "standard"}
  }
}
```

**Test** (`rules/customer_alert_test/match_premium.json`):
```json
{"customer_id": "cust-123"}
```

**Test** (`rules/customer_alert_test/not_match_standard.json`):
```json
{"customer_id": "cust-456"}
```

### Example 3: Time-Based Rule Test

**Rule** (`rules/business_hours.yaml`):
```yaml
- subject: support.ticket
  conditions:
    operator: and
    items:
      - field: "@time.hour"
        operator: gte
        value: 9
      - field: "@time.hour"
        operator: lt
        value: 17
  action:
    subject: support.during-hours
    payload: '{}'
```

**Test Config** (`rules/business_hours_test/_test_config.json`):
```json
{
  "subject": "support.ticket",
  "mockTime": "2025-10-08T10:00:00Z"
}
```

**Test** (`rules/business_hours_test/match_business_hours.json`):
```json
{"ticket_id": "123"}
```

To test after-hours, create another test directory with a different `mockTime`.

-----

## Summary

The `rule-tester` provides a complete testing solution for rule-router:

- ✅ Fast feedback loop with Quick Check
- ✅ Comprehensive and **fast** test suites with Batch Test
- ✅ Dependency isolation with mocking
- ✅ CI/CD integration with JSON output
- ✅ Production-grade features (parallel execution, progress indication)
- ✅ Developer-friendly (scaffolding, overwrite protection)

Start with scaffolding, write basic tests, then progressively add KV mocks and output validation as your rules become more complex.

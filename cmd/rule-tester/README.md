# Rule Tester (`rule-tester`)

The `rule-tester` is a command-line utility designed to help you write, test, and validate your `rule-router` rules in a fast, offline environment. It allows you to verify the logic of your rules, including conditions, templates, and dependencies like KV and time, without needing a running NATS server.

This tool is essential for maintaining a high-quality, reliable ruleset and integrating rule validation into a modern CI/CD workflow.

-----

## Features

  * **Linting**: Quickly validate the YAML syntax and structure of all rule files.
  * **Scaffolding**: Automatically generate the directory structure and placeholder files for new tests.
  * **Quick Check**: Interactively test a single rule against a single message for immediate feedback during development.
  * **Batch Testing**: Run a full suite of tests for your entire ruleset, based on a simple, convention-over-configuration directory structure.
  * **Dependency Mocking**: Isolate your tests by providing mock data for NATS KV stores and mock timestamps for time-based rules.
  * **Output Validation**: Guarantee the correctness of your templates by comparing the rendered action subject and payload against an expected output.

-----

## Testing Workflow

The `rule-tester` is designed around a simple, convention-based workflow that is both efficient and powerful.

### 1\. Scaffolding Your Tests

The easiest way to start is by using the `--scaffold` command on your new rule file. This creates the necessary test directory and starter files.

**Command:**

```bash
rule-tester --scaffold ./rules/my-new-rule.yaml
```

**Result:**
The tool creates a `_test` directory alongside your rule and populates it with placeholders, guiding you to create both a matching and non-matching test case.

```
rules/
├── my-new-rule.yaml
└── my-new-rule_test/
    ├── match_1.json      # Placeholder for a message that SHOULD match
    └── not_match_1.json  # Placeholder for a message that SHOULD NOT match
```

The tool will also print a helpful tip to the console to remind you of the more advanced testing features.

### 2\. Writing a Basic Test

Edit the generated JSON files with your sample message data. The filename itself declares the expected outcome.

  * `match_*.json`: The tester will assert that the rule **must** match. The test fails if it doesn't.
  * `not_match_*.json`: The tester will assert that the rule **must not** match. The test fails if it does.

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
  ✔ match_1.json
  ✔ not_match_1.json

--- SUMMARY ---
Total Tests: 2, Passed: 2, Failed: 0
```

-----

## Advanced Testing

For complex rules with dependencies or strict output requirements, you can add optional files to your test directory.

### Output Validation

To ensure your templates are rendered correctly, you can provide an "expected output" file. For a test message named `match_high_temp.json`, create `match_high_temp_output.json`.

The output file should contain a JSON object with the expected **subject** and **payload**.

**Example:**

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

#### **Mocking KV Data**

Create a `mock_kv_data.json` file in your test directory. The top-level keys are the bucket names. The tester will load this data into an in-memory cache for your test run.

  * **`mock_kv_data.json`**:
    ```json
    {
      "device_status": {
        "sensor-123": { "status": "active" }
      },
      "device_config": {
        "sensor-123": { "threshold": 30 }
      }
    }
    ```

#### **Mocking Time and Subject**

Create a `_test_config.json` file to control the simulated environment.

  * **`_test_config.json`**:
    ```json
    {
      "subject": "sensors.temp.room-101",
      "mockTime": "2025-10-08T15:00:00-05:00"
    }
    ```
  * `subject`: The NATS subject to simulate for the test run, which is crucial for rules using `@subject` tokens.
  * `mockTime`: An RFC3339 timestamp. All time-based conditions (`@time.hour`, etc.) will be evaluated against this fixed time.

-----

## Full CLI Reference

#### `lint`

Validates the syntax of all `*.yaml` rule files in a directory.

```bash
rule-tester --lint --rules ./rules
```

#### `scaffold`

Creates a test directory and placeholder files for a given rule.

```bash
rule-tester --scaffold <path/to/rule.yaml>
```

#### `test`

Runs all test suites found in a directory.

```bash
rule-tester --test --rules ./rules
```

#### Quick Check

Runs a single rule against a single message for interactive development.

```bash
rule-tester --rule <path/to/rule.yaml> --message <path/to/message.json>
```

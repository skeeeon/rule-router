<script setup>
import { ref } from 'vue'

const emit = defineEmits(['close'])
const activeSection = ref('variables')
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal modal-wide help-modal">
      <div class="modal-header">
        <h2>Reference</h2>
        <button class="remove-btn" @click="emit('close')">&times;</button>
      </div>

      <div class="help-tabs">
        <button :class="{ active: activeSection === 'variables' }" @click="activeSection = 'variables'">Variables</button>
        <button :class="{ active: activeSection === 'operators' }" @click="activeSection = 'operators'">Operators</button>
        <button :class="{ active: activeSection === 'templates' }" @click="activeSection = 'templates'">Templates</button>
        <button :class="{ active: activeSection === 'triggers' }" @click="activeSection = 'triggers'">Triggers</button>
      </div>

      <div class="help-content">

        <!-- Variables -->
        <div v-if="activeSection === 'variables'">
          <h3>Message Fields</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{field}</td><td>Top-level field from message payload</td></tr>
            <tr><td class="mono">{nested.field.path}</td><td>Dot-notation for nested JSON</td></tr>
            <tr><td class="mono">{items.0.name}</td><td>Array index access</td></tr>
          </tbody></table>

          <h3>NATS Context</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@subject}</td><td>Full NATS subject</td></tr>
            <tr><td class="mono">{@subject.0}</td><td>Subject token by index</td></tr>
            <tr><td class="mono">{@subject.count}</td><td>Number of subject tokens</td></tr>
          </tbody></table>

          <h3>HTTP Context</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@path}</td><td>Full HTTP path</td></tr>
            <tr><td class="mono">{@path.0}</td><td>Path segment by index</td></tr>
            <tr><td class="mono">{@method}</td><td>HTTP method</td></tr>
            <tr><td class="mono">{@header.X-Name}</td><td>Request header value</td></tr>
          </tbody></table>

          <h3>Time &amp; Date</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@time.hour}</td><td>Hour (0-23)</td></tr>
            <tr><td class="mono">{@time.minute}</td><td>Minute (0-59)</td></tr>
            <tr><td class="mono">{@day.name}</td><td>Day name (monday-sunday)</td></tr>
            <tr><td class="mono">{@day.number}</td><td>Day number (1-7, Mon=1)</td></tr>
            <tr><td class="mono">{@date.year}</td><td>Full year</td></tr>
            <tr><td class="mono">{@date.month}</td><td>Month (1-12)</td></tr>
            <tr><td class="mono">{@date.day}</td><td>Day of month (1-31)</td></tr>
            <tr><td class="mono">{@date.iso}</td><td>ISO date (2006-01-02)</td></tr>
            <tr><td class="mono">{@timestamp.unix}</td><td>Unix seconds</td></tr>
            <tr><td class="mono">{@timestamp.iso}</td><td>RFC3339 timestamp</td></tr>
          </tbody></table>

          <h3>KV Store</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@kv.bucket.key}</td><td>Fetch full value from KV</td></tr>
            <tr><td class="mono">{@kv.bucket.key:path}</td><td>Fetch and extract JSON path</td></tr>
            <tr><td class="mono">{@kv.config.{id}:max}</td><td>Dynamic key from message field</td></tr>
          </tbody></table>

          <h3>Functions</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@timestamp()}</td><td>Current RFC3339 timestamp</td></tr>
            <tr><td class="mono">{@uuid4()}</td><td>Random UUID v4</td></tr>
            <tr><td class="mono">{@uuid7()}</td><td>Time-based UUID v7</td></tr>
          </tbody></table>

          <h3>forEach Context</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{field}</td><td>Field from current array element</td></tr>
            <tr><td class="mono">{@msg.field}</td><td>Field from original root message</td></tr>
          </tbody></table>

          <h3>Other</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{@signature.valid}</td><td>NKey signature is valid (bool)</td></tr>
            <tr><td class="mono">{@signature.pubkey}</td><td>NKey public key string</td></tr>
            <tr><td class="mono">{@value}</td><td>Root value (primitive messages)</td></tr>
            <tr><td class="mono">{@items}</td><td>Root array (array messages)</td></tr>
            <tr><td class="mono">${ENV_VAR}</td><td>Environment variable (expanded at load time)</td></tr>
          </tbody></table>

          <div class="help-note">
            Schedule rules have no incoming message — only Time, KV, functions, and date variables are available.
          </div>
        </div>

        <!-- Operators -->
        <div v-if="activeSection === 'operators'">
          <h3>Comparison</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">eq</td><td>Equal</td></tr>
            <tr><td class="mono">neq</td><td>Not equal</td></tr>
            <tr><td class="mono">gt</td><td>Greater than (numeric)</td></tr>
            <tr><td class="mono">lt</td><td>Less than (numeric)</td></tr>
            <tr><td class="mono">gte</td><td>Greater than or equal</td></tr>
            <tr><td class="mono">lte</td><td>Less than or equal</td></tr>
          </tbody></table>

          <h3>String</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">contains</td><td>Field contains substring</td></tr>
            <tr><td class="mono">not_contains</td><td>Field does not contain substring</td></tr>
          </tbody></table>

          <h3>Membership</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">in</td><td>Value is in list (comma-separated)</td></tr>
            <tr><td class="mono">not_in</td><td>Value is not in list</td></tr>
          </tbody></table>

          <h3>Special</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">exists</td><td>Field exists (no value needed)</td></tr>
            <tr><td class="mono">recent</td><td>Timestamp within N seconds of now</td></tr>
          </tbody></table>

          <h3>Array</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">any</td><td>At least one array element matches nested conditions</td></tr>
            <tr><td class="mono">all</td><td>Every array element matches nested conditions</td></tr>
            <tr><td class="mono">none</td><td>No array element matches nested conditions</td></tr>
          </tbody></table>

          <div class="help-note">
            Conditions use AND/OR grouping. Array operators (any, all, none) require nested conditions that evaluate against each element.
          </div>
        </div>

        <!-- Templates -->
        <div v-if="activeSection === 'templates'">
          <h3>Payload Templates</h3>
          <p class="help-text">Action payloads use <code>{variable}</code> syntax. Values are substituted at runtime.</p>

          <h3>Numeric vs String</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">{temperature}</td><td>Unquoted — preserves number type</td></tr>
            <tr><td class="mono">"{temperature}"</td><td>Quoted — always a string</td></tr>
          </tbody></table>

          <h3>Payload Modes</h3>
          <table class="help-table"><tbody>
            <tr><td class="mono">payload</td><td>Template string rendered as the action body</td></tr>
            <tr><td class="mono">passthrough</td><td>Forward original message unchanged</td></tr>
            <tr><td class="mono">merge</td><td>Deep-merge template overlay onto original message</td></tr>
          </tbody></table>
          <p class="help-text">Passthrough and payload are mutually exclusive. Merge requires a payload.</p>

          <h3>forEach</h3>
          <p class="help-text">Iterate over an array field, generating one action per element:</p>
          <table class="help-table"><tbody>
            <tr><td class="mono">forEach: "{events}"</td><td>Iterate message array field</td></tr>
            <tr><td class="mono">forEach: "{@kv.b.k}"</td><td>Iterate KV-stored array</td></tr>
          </tbody></table>
          <p class="help-text">Inside forEach, <code>{field}</code> refers to the current element. Use <code>{@msg.field}</code> to access the original root message.</p>

          <h3>Debounce</h3>
          <p class="help-text">Fire-first throttle — allows the first match, suppresses duplicates for the window duration.</p>
          <table class="help-table"><tbody>
            <tr><td class="mono">window</td><td>Suppression duration (e.g., "5s", "1m")</td></tr>
            <tr><td class="mono">key</td><td>Optional grouping key template (e.g., "{@subject.2}")</td></tr>
          </tbody></table>
        </div>

        <!-- Triggers -->
        <div v-if="activeSection === 'triggers'">
          <h3>NATS Trigger</h3>
          <p class="help-text">Subscribe to NATS subjects. Supports wildcards:</p>
          <table class="help-table"><tbody>
            <tr><td class="mono">sensors.temperature</td><td>Exact subject match</td></tr>
            <tr><td class="mono">sensors.*.room1</td><td>Single-level wildcard</td></tr>
            <tr><td class="mono">sensors.></td><td>Multi-level wildcard (must be last)</td></tr>
          </tbody></table>

          <h3>HTTP Trigger</h3>
          <p class="help-text">Receive HTTP webhooks at a path:</p>
          <table class="help-table"><tbody>
            <tr><td class="mono">/webhooks/github</td><td>Path must start with /</td></tr>
            <tr><td class="mono">method</td><td>Optional — defaults to all methods</td></tr>
          </tbody></table>

          <h3>Schedule Trigger</h3>
          <p class="help-text">Standard 5-field cron expression:</p>
          <pre class="help-cron">minute  hour  day-of-month  month  day-of-week</pre>
          <table class="help-table"><tbody>
            <tr><td class="mono">*/5 * * * *</td><td>Every 5 minutes</td></tr>
            <tr><td class="mono">0 8 * * 1-5</td><td>Weekdays at 8:00 AM</td></tr>
            <tr><td class="mono">0 0 * * 0</td><td>Midnight every Sunday</td></tr>
          </tbody></table>
          <p class="help-text">Optional <code>timezone</code> field accepts IANA names (e.g., America/New_York).</p>

          <h3>Testing Rules</h3>
          <p class="help-text">Download your rules and use the <code>rule-cli</code> for local testing:</p>
          <pre class="help-cron">rule-cli lint --rules ./rules
rule-cli check --rule rule.yaml --message msg.json
rule-cli test --rules ./rules</pre>
        </div>

      </div>
    </div>
  </div>
</template>

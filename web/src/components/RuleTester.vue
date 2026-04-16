<script setup>
import { ref, computed, watch, inject } from 'vue'
import { callEvaluateRule, getWasmState, loadWasm } from '../utils/wasm.js'

const props = defineProps({
  rule: Object,
  yaml: String,
})

const inspectedFields = inject('inspectedFields', ref([]))

const expanded = ref(false)
const messageInput = ref('')
const subjectInput = ref('')
const pathInput = ref('')
const methodInput = ref('POST')
const showHeaders = ref(false)
const headersInput = ref('')
const showKvMock = ref(false)
const kvMockInput = ref('')

const loading = ref(false)
const wasmLoading = ref(false)
const result = ref(null)
const error = ref(null)

// Auto-populate trigger fields when rule changes
watch(() => props.rule?.trigger, (trigger) => {
  if (!trigger) return
  if (trigger.type === 'nats' && trigger.nats.subject) {
    subjectInput.value = trigger.nats.subject
  } else if (trigger.type === 'http') {
    pathInput.value = trigger.http.path || '/'
    methodInput.value = trigger.http.method || 'POST'
  }
}, { immediate: true, deep: true })

const triggerType = computed(() => props.rule?.trigger?.type || 'nats')

// Start loading WASM when panel is opened
watch(expanded, (val) => {
  if (val) {
    const state = getWasmState()
    if (!state.ready && !state.loading) {
      wasmLoading.value = true
      loadWasm()
        .then(() => { wasmLoading.value = false })
        .catch(() => { wasmLoading.value = false })
    }
  }
})

function formatPayload(payload) {
  if (!payload) return ''
  try {
    return JSON.stringify(JSON.parse(payload), null, 2)
  } catch {
    return payload
  }
}

async function runTest() {
  if (!messageInput.value.trim()) {
    error.value = 'Sample message is required'
    result.value = null
    return
  }

  // Validate JSON
  try {
    JSON.parse(messageInput.value)
  } catch (e) {
    error.value = `Invalid JSON message: ${e.message}`
    result.value = null
    return
  }

  // Parse KV mock if provided
  let kvMock = {}
  if (kvMockInput.value.trim()) {
    try {
      kvMock = JSON.parse(kvMockInput.value)
    } catch (e) {
      error.value = `Invalid KV mock JSON: ${e.message}`
      result.value = null
      return
    }
  }

  // Parse headers if provided
  let headers = {}
  if (headersInput.value.trim()) {
    try {
      headers = JSON.parse(headersInput.value)
    } catch (e) {
      error.value = `Invalid headers JSON: ${e.message}`
      result.value = null
      return
    }
  }

  error.value = null
  loading.value = true

  try {
    const options = {
      triggerType: triggerType.value,
      subject: subjectInput.value,
      path: pathInput.value,
      method: methodInput.value,
      headers,
      kvMock,
    }

    result.value = await callEvaluateRule(props.yaml, messageInput.value, options)

    if (result.value.error) {
      error.value = result.value.error
    } else if (result.value.validationError) {
      error.value = `Rule validation: ${result.value.validationError}`
    }
  } catch (e) {
    error.value = `WASM error: ${e.message}`
    result.value = null
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="rule-tester" :class="{ expanded }">
    <button class="tester-toggle" @click="expanded = !expanded">
      <span class="tester-toggle-icon">{{ expanded ? '&#9662;' : '&#9656;' }}</span>
      Test Rule
      <span v-if="result && !error" class="tester-result-badge" :class="result.matched ? 'matched' : 'not-matched'">
        {{ result.matched ? 'MATCHED' : 'NO MATCH' }}
      </span>
    </button>

    <div v-if="expanded" class="tester-body">
      <!-- WASM loading state -->
      <div v-if="wasmLoading" class="tester-loading">
        Loading test engine...
      </div>

      <!-- Inputs -->
      <div class="field">
        <label>Sample Message</label>
        <textarea
          v-model="messageInput"
          class="tester-message"
          rows="6"
          placeholder='{"temperature": 105, "sensor_id": "room1"}'
          spellcheck="false"
        ></textarea>
      </div>

      <!-- Trigger context -->
      <div v-if="triggerType === 'nats'" class="field">
        <label>Subject</label>
        <input v-model="subjectInput" placeholder="sensors.temperature.room1">
      </div>
      <div v-else-if="triggerType === 'http'" class="field-row">
        <div class="field">
          <label>Path</label>
          <input v-model="pathInput" placeholder="/webhooks/github">
        </div>
        <div class="field">
          <label>Method</label>
          <select v-model="methodInput">
            <option v-for="m in ['GET','POST','PUT','PATCH','DELETE']" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>
      </div>

      <!-- Optional sections -->
      <div class="tester-options">
        <label class="checkbox">
          <input type="checkbox" v-model="showHeaders"> Headers
        </label>
        <label class="checkbox">
          <input type="checkbox" v-model="showKvMock"> Mock KV Data
        </label>
      </div>

      <div v-if="showHeaders" class="field">
        <label>Headers <span class="optional">(JSON object)</span></label>
        <textarea
          v-model="headersInput"
          class="tester-kv"
          rows="3"
          placeholder='{"X-Api-Key": "abc123"}'
          spellcheck="false"
        ></textarea>
      </div>

      <div v-if="showKvMock" class="field">
        <label>Mock KV Data <span class="optional">(JSON: bucket &rarr; key &rarr; value)</span></label>
        <textarea
          v-model="kvMockInput"
          class="tester-kv"
          rows="4"
          placeholder='{"config": {"thresholds": {"max_temp": 100}}}'
          spellcheck="false"
        ></textarea>
      </div>

      <!-- Run button -->
      <button class="primary-btn tester-run-btn" @click="runTest" :disabled="loading || wasmLoading">
        {{ loading ? 'Testing...' : 'Run Test' }}
      </button>

      <!-- Error -->
      <div v-if="error" class="status error">{{ error }}</div>

      <!-- Results -->
      <div v-if="result && !error" class="tester-results">
        <div class="tester-match-status" :class="result.matched ? 'matched' : 'not-matched'">
          {{ result.matched ? 'Rule Matched' : 'Rule Not Matched' }}
        </div>

        <template v-if="result.matched && result.actions.length > 0">
          <div class="tester-actions-header">
            Actions Generated: {{ result.actions.length }}
          </div>

          <div v-for="(action, i) in result.actions" :key="i" class="tester-action-card">
            <div class="tester-action-header">
              <span class="tester-action-label">Action {{ result.actions.length > 1 ? `${i + 1} of ${result.actions.length}` : '' }}</span>
              <span class="tester-action-type">{{ action.type.toUpperCase() }}</span>
            </div>

            <div class="tester-action-fields">
              <div v-if="action.type === 'nats'" class="tester-action-field">
                <span class="tester-field-label">Subject</span>
                <code>{{ action.subject }}</code>
              </div>
              <div v-if="action.type === 'http'" class="tester-action-field">
                <span class="tester-field-label">URL</span>
                <code>{{ action.method }} {{ action.url }}</code>
              </div>
              <div class="tester-action-field">
                <span class="tester-field-label">Payload</span>
                <pre class="tester-payload">{{ action.passthrough ? '(passthrough)' : formatPayload(action.payload) }}</pre>
              </div>
              <div v-if="action.headers && Object.keys(action.headers).length > 0" class="tester-action-field">
                <span class="tester-field-label">Headers</span>
                <code v-for="(v, k) in action.headers" :key="k" class="tester-header-item">{{ k }}: {{ v }}</code>
              </div>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

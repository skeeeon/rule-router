<script setup>
import { ref, computed, watch, inject, onMounted, onUnmounted } from 'vue'
import { callEvaluateRule, getWasmState, loadWasm } from '../utils/wasm.js'

const props = defineProps({
  rule: Object,
  yaml: String,
  // Index of the active rule within the file's YAML — the engine evaluates
  // exactly one rule, defaulting to 0, so multi-rule files need this.
  ruleIndex: { type: Number, default: 0 },
})

const inspectedFields = inject('inspectedFields', ref([]))
const sampleMessage = inject('sampleMessage', ref(''))

const messageModel = computed({
  get: () => sampleMessage.value,
  set: (v) => { sampleMessage.value = v },
})

const expanded = ref(true)
const subjectInput = ref('')
const pathInput = ref('')
const methodInput = ref('POST')
const showHeaders = ref(false)
const headersInput = ref('')
const showKvMock = ref(false)
const kvMockInput = ref('')
const showMockTime = ref(false)
const mockTimeInput = ref('')

const loading = ref(false)
const wasmLoading = ref(false)
const result = ref(null)
const error = ref(null)

// Track previous trigger values so we only auto-populate when the user hasn't customized.
let prevSubject = ''
let prevPath = ''
let prevMethod = 'POST'

watch(() => props.rule?.trigger, (trigger) => {
  if (!trigger) return
  if (trigger.type === 'nats') {
    const next = trigger.nats.subject || ''
    if (subjectInput.value === '' || subjectInput.value === prevSubject) {
      subjectInput.value = next
    }
    prevSubject = next
  } else if (trigger.type === 'http') {
    const nextPath = trigger.http.path || '/'
    const nextMethod = trigger.http.method || 'POST'
    if (pathInput.value === '' || pathInput.value === prevPath) {
      pathInput.value = nextPath
    }
    if (methodInput.value === prevMethod) {
      methodInput.value = nextMethod
    }
    prevPath = nextPath
    prevMethod = nextMethod
  }
}, { immediate: true, deep: true })

const triggerType = computed(() => props.rule?.trigger?.type || 'nats')

function kickoffWasm() {
  const state = getWasmState()
  if (state.ready || state.loading) return
  wasmLoading.value = true
  loadWasm()
    .then(() => { wasmLoading.value = false })
    .catch(() => {
      wasmLoading.value = false
      error.value = `Failed to load test engine: ${getWasmState().error || 'unknown error'}`
    })
}

// Load WASM on mount (default-expanded) and when re-expanded after collapse.
onMounted(() => {
  if (expanded.value) kickoffWasm()
  document.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', onKeydown)
})

watch(expanded, (val) => {
  if (val) kickoffWasm()
})

function onKeydown(e) {
  if (!expanded.value) return
  if (!(e.metaKey || e.ctrlKey) || e.key !== 'Enter') return
  if (loading.value || wasmLoading.value) return
  e.preventDefault()
  runTest()
}

function formatPayload(payload) {
  if (!payload) return ''
  try {
    return JSON.stringify(JSON.parse(payload), null, 2)
  } catch {
    return payload
  }
}

async function runTest() {
  if (!sampleMessage.value.trim()) {
    error.value = 'Sample message is required'
    result.value = null
    return
  }

  // Validate JSON
  try {
    JSON.parse(sampleMessage.value)
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

  // Validate mock time if provided — the engine silently ignores values it
  // can't parse, so catch the mistake here instead.
  const mockTime = mockTimeInput.value.trim()
  if (mockTime && (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(mockTime) || isNaN(Date.parse(mockTime)))) {
    error.value = 'Invalid mock time — use RFC3339, e.g. 2026-06-10T14:30:00Z'
    result.value = null
    return
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
      mockTime,
      ruleIndex: props.ruleIndex,
    }

    result.value = await callEvaluateRule(props.yaml, sampleMessage.value, options)

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
        <span class="loading-spinner"></span> Loading test engine...
      </div>

      <!-- Inputs -->
      <div class="field">
        <label>Sample Message <span class="optional">(shared with inspector)</span></label>
        <textarea
          v-model="messageModel"
          class="tester-message"
          rows="6"
          placeholder='{"temperature": 105, "sensor_id": "room1"}'
          spellcheck="false"
        ></textarea>
      </div>

      <!-- Trigger context -->
      <div v-if="triggerType === 'nats'" class="field">
        <label>Subject</label>
        <input
          v-model="subjectInput"
          placeholder="sensors.temperature.room1"
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
        >
      </div>
      <div v-else-if="triggerType === 'http'" class="field-row">
        <div class="field">
          <label>Path</label>
          <input
            v-model="pathInput"
            placeholder="/webhooks/github"
            autocapitalize="off"
            autocorrect="off"
            autocomplete="off"
            spellcheck="false"
            inputmode="url"
          >
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
        <label class="checkbox">
          <input type="checkbox" v-model="showMockTime"> Mock Time
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

      <div v-if="showMockTime" class="field">
        <label>Mock Time <span class="optional">(RFC3339 — for {@time...} / {@day...} conditions)</span></label>
        <input
          v-model="mockTimeInput"
          placeholder="2026-06-10T14:30:00Z"
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
        >
      </div>

      <!-- Run button -->
      <button class="primary-btn tester-run-btn" @click="runTest" :disabled="loading || wasmLoading" title="Run Test (Ctrl/Cmd+Enter)">
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
              <div v-if="action.type === 'respond'" class="tester-action-field">
                <span class="tester-field-label">Status Code</span>
                <code>{{ action.statusCode }}</code>
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

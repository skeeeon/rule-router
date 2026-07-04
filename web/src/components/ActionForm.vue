<script setup>
import { ref, computed, watch, inject, nextTick, onMounted, onUnmounted } from 'vue'
import { createDebounce, createRetry, createConditions, createPublishResponse } from '../utils/state.js'
import HeadersEditor from './HeadersEditor.vue'
import DebounceEditor from './DebounceEditor.vue'
import ConditionsBuilder from './ConditionsBuilder.vue'
import FieldSuggestInput from './FieldSuggestInput.vue'
import JsonTextarea from './JsonTextarea.vue'

const props = defineProps({
  action: Object,
  trigger: Object,
  errorFor: Function,
})

// What the trigger permits drives which action options are valid.
// Mirrors loader.validateTriggerActionCompatibility + ProcessSchedule (nil payload).
const isHttp = computed(() => props.trigger.type === 'http')
const isSchedule = computed(() => props.trigger.type === 'schedule')
const natsReply = computed(() => props.trigger.type === 'nats' && props.trigger.nats.reply)
// A respond action is valid on an HTTP trigger or a NATS request/reply responder.
const canRespond = computed(() => isHttp.value || natsReply.value)
// A NATS responder *requires* a respond action — the action type is forced.
const replyForced = computed(() => natsReply.value)

// Keep the action in a valid shape for the current trigger. Runs on mount and
// whenever the trigger type / reply flag changes (e.g. user toggles them, or a
// different rule is selected in the workspace).
watch(
  [() => props.trigger.type, natsReply],
  () => {
    const a = props.action
    if (replyForced.value) {
      a.type = 'respond'
    } else if (a.type === 'respond' && !canRespond.value) {
      a.type = 'nats'
    }
    // Bridge (request/reply) is only honored on HTTP triggers.
    if (!isHttp.value) {
      a.nats.request = false
      a.nats.timeout = ''
    }
    // Schedule has no incoming message — passthrough/merge/forEach are meaningless
    // (passthrough would publish an empty payload), so clear them.
    if (isSchedule.value) {
      for (const act of [a.nats, a.http]) {
        act.passthrough = false
        act.merge = false
        act.forEach = ''
        act.filter = null
      }
    }
  },
  { immediate: true }
)

const inspectedFields = inject('inspectedFields', ref([]))
const activePayloadInsert = inject('activePayloadInsert', ref(null))

const natsPayloadEl = ref(null)
const httpPayloadEl = ref(null)

// Track last known cursor position so chip clicks insert at the right spot.
const cursorPositions = { nats: null, http: null }
// Last payload textarea the user touched — used when an external chip (Inspector) fires.
let lastFocused = null  // 'nats' | 'http' | null

function saveCursor(e, which) {
  cursorPositions[which] = { start: e.target.selectionStart, end: e.target.selectionEnd }
  lastFocused = which
}

function insertField(path, which, target, key) {
  const tag = `{${path}}`
  const val = target[key] || ''
  const pos = cursorPositions[which]
  const start = pos ? pos.start : val.length
  const end = pos ? pos.end : val.length
  target[key] = val.slice(0, start) + tag + val.slice(end)
  const newPos = start + tag.length
  cursorPositions[which] = { start: newPos, end: newPos }
  const el = (which === 'nats' ? natsPayloadEl : httpPayloadEl).value
  if (el) {
    nextTick(() => {
      el.focus()
      el.setSelectionRange(newPos, newPos)
    })
  }
}

// Expose an insert function so the Inspector chip can insert into the active payload.
onMounted(() => {
  activePayloadInsert.value = (path) => {
    const which = lastFocused || props.action.type
    if (which === 'nats' && props.action.type === 'nats' && !props.action.nats.passthrough) {
      insertField(path, 'nats', props.action.nats, 'payload')
    } else if (which === 'http' && props.action.type === 'http' && !props.action.http.passthrough) {
      insertField(path, 'http', props.action.http, 'payload')
    }
  }
})

onUnmounted(() => {
  activePayloadInsert.value = null
})

function typeLabel(type) {
  const labels = { string: 'str', number: 'num', boolean: 'bool', array: 'arr', null: 'null' }
  return labels[type] || type
}

function toggleOption(target, key, factory) {
  if (target[key]) {
    target[key] = null
  } else {
    target[key] = factory()
  }
}
</script>

<template>
  <div class="action-form">
    <!-- A NATS request/reply responder must answer with a respond action. -->
    <div v-if="replyForced" class="reply-banner">
      <strong>Replies to caller</strong>
      <span>This NATS responder answers each request with the respond action below.</span>
    </div>
    <div v-else class="radio-group">
      <label><input type="radio" v-model="action.type" value="nats"> NATS</label>
      <label><input type="radio" v-model="action.type" value="http"> HTTP</label>
      <label v-if="canRespond"><input type="radio" v-model="action.type" value="respond"> Respond</label>
    </div>

    <!-- NATS action -->
    <div v-if="action.type === 'nats'" class="fields">
      <div class="field">
        <label>Subject</label>
        <input
          v-model="action.nats.subject"
          placeholder="alerts.high_temp.{@subject.2}"
          :class="{ error: errorFor('action.nats.subject') }"
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
        >
        <span class="field-error" v-if="errorFor('action.nats.subject')">
          {{ errorFor('action.nats.subject').message }}
        </span>
      </div>

      <div class="field">
        <label>Publish Mode <span class="optional">(optional)</span></label>
        <select v-model="action.nats.mode" :class="{ error: errorFor('action.nats.mode') }">
          <option value="">Inherit global config</option>
          <option value="jetstream">JetStream</option>
          <option value="core">Core NATS</option>
        </select>
        <span class="field-error" v-if="errorFor('action.nats.mode')">
          {{ errorFor('action.nats.mode').message }}
        </span>
        <span class="field-hint">JetStream: acked, needs a stream on the subject. Core: fire-and-forget.</span>
      </div>

      <div v-if="!isSchedule" class="field">
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.nats.passthrough" :disabled="action.nats.merge"> Passthrough
        </label>
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.nats.merge" :disabled="action.nats.passthrough"> Merge
        </label>
      </div>

      <div v-if="!action.nats.passthrough" class="field">
        <label>Payload</label>
        <div v-if="inspectedFields.length" class="field-chips">
          <button
            v-for="f in inspectedFields"
            :key="f.path"
            class="field-chip"
            @click="insertField(f.path, 'nats', action.nats, 'payload')"
            :title="f.sample"
          ><code>{<span>{{ f.path }}</span>}</code><span class="chip-type">{{ typeLabel(f.type) }}</span></button>
        </div>
        <JsonTextarea
          ref="natsPayloadEl"
          v-model="action.nats.payload"
          :rows="6"
          placeholder='{"alert": "High temperature!", "temp": {temperature}}'
          :error="!!errorFor('action.nats.payload')"
          @keyup="saveCursor($event, 'nats')"
          @mouseup="saveCursor($event, 'nats')"
        />
        <span class="field-error" v-if="errorFor('action.nats.payload')">
          {{ errorFor('action.nats.payload').message }}
        </span>
      </div>

      <!-- Optional features -->
      <div class="option-toggles">
        <label v-if="!isSchedule" class="checkbox">
          <input type="checkbox" :checked="!!action.nats.forEach" @change="action.nats.forEach = action.nats.forEach ? '' : '{items}'">
          forEach
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="Object.keys(action.nats.headers).length > 0" @change="action.nats.headers = Object.keys(action.nats.headers).length > 0 ? {} : { '': '' }">
          Headers
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.nats.debounce" @change="toggleOption(action.nats, 'debounce', createDebounce)">
          Debounce
        </label>
      </div>

      <div v-if="action.nats.forEach" class="field">
        <label>forEach Array Field</label>
        <FieldSuggestInput
          v-model="action.nats.forEach"
          placeholder="{events}"
          :error="!!errorFor('action.nats.forEach')"
        />
        <span class="field-error" v-if="errorFor('action.nats.forEach')">
          {{ errorFor('action.nats.forEach').message }}
        </span>
      </div>

      <div v-if="action.nats.forEach">
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.nats.filter" @change="toggleOption(action.nats, 'filter', createConditions)">
          forEach Filter
        </label>
        <ConditionsBuilder
          v-if="action.nats.filter"
          v-model="action.nats.filter"
          :error-for="errorFor"
          prefix="action.nats.filter"
        />
      </div>

      <HeadersEditor v-if="Object.keys(action.nats.headers).length > 0" v-model="action.nats.headers" />
      <DebounceEditor v-if="action.nats.debounce" :debounce="action.nats.debounce" :error-for="errorFor" prefix="action.nats.debounce" />

      <template v-if="isHttp">
      <label class="checkbox">
        <input type="checkbox" v-model="action.nats.request">
        Request/Reply (HTTP↔NATS bridge)
      </label>
      <span class="field-hint">Send a NATS request and return the reply as the HTTP response.</span>
      <div v-if="action.nats.request" class="field">
        <label>Timeout <span class="optional">(optional)</span></label>
        <input
          v-model="action.nats.timeout"
          placeholder="5s"
          :class="{ error: errorFor('action.nats.timeout') }"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
        >
        <span class="field-error" v-if="errorFor('action.nats.timeout')">
          {{ errorFor('action.nats.timeout').message }}
        </span>
        <span class="field-hint">Duration string (e.g. 3s, 500ms). Defaults to 5s.</span>
      </div>
      </template>
    </div>

    <!-- HTTP action -->
    <div v-if="action.type === 'http'" class="fields">
      <div class="field">
        <label>URL</label>
        <input
          v-model="action.http.url"
          placeholder="https://api.example.com/webhook"
          :class="{ error: errorFor('action.http.url') }"
          type="url"
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
          inputmode="url"
        >
        <span class="field-error" v-if="errorFor('action.http.url')">
          {{ errorFor('action.http.url').message }}
        </span>
      </div>
      <div class="field">
        <label>Method</label>
        <select v-model="action.http.method" :class="{ error: errorFor('action.http.method') }">
          <option v-for="m in ['GET','POST','PUT','PATCH','DELETE']" :key="m" :value="m">{{ m }}</option>
        </select>
        <span class="field-error" v-if="errorFor('action.http.method')">
          {{ errorFor('action.http.method').message }}
        </span>
      </div>

      <div v-if="!isSchedule" class="field">
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.http.passthrough" :disabled="action.http.merge"> Passthrough
        </label>
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.http.merge" :disabled="action.http.passthrough"> Merge
        </label>
      </div>

      <div v-if="!action.http.passthrough" class="field">
        <label>Payload</label>
        <div v-if="inspectedFields.length" class="field-chips">
          <button
            v-for="f in inspectedFields"
            :key="f.path"
            class="field-chip"
            @click="insertField(f.path, 'http', action.http, 'payload')"
            :title="f.sample"
          ><code>{<span>{{ f.path }}</span>}</code><span class="chip-type">{{ typeLabel(f.type) }}</span></button>
        </div>
        <JsonTextarea
          ref="httpPayloadEl"
          v-model="action.http.payload"
          :rows="6"
          placeholder='{"alert": "{message}", "source": "{@subject}"}'
          :error="!!errorFor('action.http.payload')"
          @keyup="saveCursor($event, 'http')"
          @mouseup="saveCursor($event, 'http')"
        />
      </div>

      <!-- Optional features -->
      <div class="option-toggles">
        <label v-if="!isSchedule" class="checkbox">
          <input type="checkbox" :checked="!!action.http.forEach" @change="action.http.forEach = action.http.forEach ? '' : '{items}'">
          forEach
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="Object.keys(action.http.headers).length > 0" @change="action.http.headers = Object.keys(action.http.headers).length > 0 ? {} : { '': '' }">
          Headers
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.http.retry" @change="toggleOption(action.http, 'retry', createRetry)">
          Retry
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.http.publishResponse" @change="toggleOption(action.http, 'publishResponse', createPublishResponse)">
          Publish Response
        </label>
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.http.debounce" @change="toggleOption(action.http, 'debounce', createDebounce)">
          Debounce
        </label>
      </div>

      <div v-if="action.http.forEach" class="field">
        <label>forEach Array Field</label>
        <FieldSuggestInput
          v-model="action.http.forEach"
          placeholder="{events}"
          :error="false"
        />
      </div>

      <div v-if="action.http.forEach">
        <label class="checkbox">
          <input type="checkbox" :checked="!!action.http.filter" @change="toggleOption(action.http, 'filter', createConditions)">
          forEach Filter
        </label>
        <ConditionsBuilder
          v-if="action.http.filter"
          v-model="action.http.filter"
          :error-for="errorFor"
          prefix="action.http.filter"
        />
      </div>

      <HeadersEditor v-if="Object.keys(action.http.headers).length > 0" v-model="action.http.headers" />

      <div v-if="action.http.retry" class="retry-fields">
        <h3>Retry</h3>
        <div class="field-row">
          <div class="field">
            <label>Max Attempts</label>
            <input type="number" v-model.number="action.http.retry.maxAttempts" min="1">
          </div>
          <div class="field">
            <label>Initial Delay</label>
            <input v-model="action.http.retry.initialDelay" placeholder="1s">
          </div>
          <div class="field">
            <label>Max Delay</label>
            <input v-model="action.http.retry.maxDelay" placeholder="30s">
          </div>
        </div>
      </div>

      <div v-if="action.http.publishResponse" class="field">
        <label>Publish Response Subject</label>
        <input
          v-model="action.http.publishResponse.subject"
          placeholder="poll.devices.{device_id}.status"
          :class="{ error: errorFor('action.http.publishResponse.subject') }"
        >
        <span class="field-hint">
          On 2xx, the response body (capped at 1 MB) is published to this NATS subject.
          Templates resolve against the trigger context only.
        </span>
        <span class="field-error" v-if="errorFor('action.http.publishResponse.subject')">
          {{ errorFor('action.http.publishResponse.subject').message }}
        </span>
      </div>

      <DebounceEditor v-if="action.http.debounce" :debounce="action.http.debounce" :error-for="errorFor" prefix="action.http.debounce" />
    </div>

    <!-- Respond action -->
    <div v-if="action.type === 'respond'" class="fields">
      <span class="field-hint">
        Sends the evaluated payload back to the caller: the HTTP response (HTTP trigger)
        or a NATS reply (NATS trigger with Request/Reply enabled).
      </span>

      <div v-if="isHttp" class="field">
        <label>Status Code <span class="optional">(optional)</span></label>
        <input
          type="number"
          v-model.number="action.respond.statusCode"
          min="100"
          max="599"
          :class="{ error: errorFor('action.respond.statusCode') }"
        >
        <span class="field-error" v-if="errorFor('action.respond.statusCode')">
          {{ errorFor('action.respond.statusCode').message }}
        </span>
        <span class="field-hint">HTTP response status. Defaults to 200.</span>
      </div>

      <div class="field">
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.respond.passthrough" :disabled="action.respond.merge"> Passthrough
        </label>
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.respond.merge" :disabled="action.respond.passthrough"> Merge
        </label>
      </div>

      <div v-if="!action.respond.passthrough" class="field">
        <label>Payload</label>
        <JsonTextarea
          v-model="action.respond.payload"
          :rows="6"
          placeholder='{"status": "ok", "id": "{@uuid7()}"}'
          :error="!!errorFor('action.respond.payload')"
        />
        <span class="field-error" v-if="errorFor('action.respond.payload')">
          {{ errorFor('action.respond.payload').message }}
        </span>
      </div>

      <div class="option-toggles">
        <label class="checkbox">
          <input type="checkbox" :checked="Object.keys(action.respond.headers).length > 0" @change="action.respond.headers = Object.keys(action.respond.headers).length > 0 ? {} : { '': '' }">
          Headers
        </label>
      </div>

      <HeadersEditor v-if="Object.keys(action.respond.headers).length > 0" v-model="action.respond.headers" />
    </div>
  </div>
</template>

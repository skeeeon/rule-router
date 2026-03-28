<script setup>
import { createDebounce, createRetry, createConditions } from '../utils/state.js'
import HeadersEditor from './HeadersEditor.vue'
import DebounceEditor from './DebounceEditor.vue'
import ConditionsBuilder from './ConditionsBuilder.vue'

const props = defineProps({
  action: Object,
  errorFor: Function,
})

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
    <div class="radio-group">
      <label><input type="radio" v-model="action.type" value="nats"> NATS</label>
      <label><input type="radio" v-model="action.type" value="http"> HTTP</label>
    </div>

    <!-- NATS action -->
    <div v-if="action.type === 'nats'" class="fields">
      <div class="field">
        <label>Subject</label>
        <input
          v-model="action.nats.subject"
          placeholder="alerts.high_temp.{@subject.2}"
          :class="{ error: errorFor('action.nats.subject') }"
        >
        <span class="field-error" v-if="errorFor('action.nats.subject')">
          {{ errorFor('action.nats.subject').message }}
        </span>
      </div>

      <div class="field">
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.nats.passthrough"> Passthrough
        </label>
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.nats.merge"> Merge
        </label>
      </div>

      <div v-if="!action.nats.passthrough" class="field">
        <label>Payload</label>
        <textarea
          v-model="action.nats.payload"
          rows="6"
          placeholder='{"alert": "High temperature!", "temp": {temperature}}'
          :class="{ error: errorFor('action.nats.payload') }"
        ></textarea>
        <span class="field-error" v-if="errorFor('action.nats.payload')">
          {{ errorFor('action.nats.payload').message }}
        </span>
      </div>

      <!-- Optional features -->
      <div class="option-toggles">
        <label class="checkbox">
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
        <input
          v-model="action.nats.forEach"
          placeholder="{events}"
          :class="{ error: errorFor('action.nats.forEach') }"
        >
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
    </div>

    <!-- HTTP action -->
    <div v-if="action.type === 'http'" class="fields">
      <div class="field">
        <label>URL</label>
        <input
          v-model="action.http.url"
          placeholder="https://api.example.com/webhook"
          :class="{ error: errorFor('action.http.url') }"
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

      <div class="field">
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.http.passthrough"> Passthrough
        </label>
        <label class="checkbox inline">
          <input type="checkbox" v-model="action.http.merge"> Merge
        </label>
      </div>

      <div v-if="!action.http.passthrough" class="field">
        <label>Payload</label>
        <textarea
          v-model="action.http.payload"
          rows="6"
          placeholder='{"alert": "{message}", "source": "{@subject}"}'
          :class="{ error: errorFor('action.http.payload') }"
        ></textarea>
      </div>

      <!-- Optional features -->
      <div class="option-toggles">
        <label class="checkbox">
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
          <input type="checkbox" :checked="!!action.http.debounce" @change="toggleOption(action.http, 'debounce', createDebounce)">
          Debounce
        </label>
      </div>

      <div v-if="action.http.forEach" class="field">
        <label>forEach Array Field</label>
        <input v-model="action.http.forEach" placeholder="{events}">
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

      <DebounceEditor v-if="action.http.debounce" :debounce="action.http.debounce" :error-for="errorFor" prefix="action.http.debounce" />
    </div>
  </div>
</template>

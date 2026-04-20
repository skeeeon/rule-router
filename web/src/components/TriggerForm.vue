<script setup>
import { createDebounce } from '../utils/state.js'
import DebounceEditor from './DebounceEditor.vue'
import CronBuilder from './CronBuilder.vue'

const props = defineProps({
  trigger: Object,
  errorFor: Function,
})

function toggleDebounce(target) {
  if (target.debounce) {
    target.debounce = null
  } else {
    target.debounce = createDebounce()
  }
}
</script>

<template>
  <div class="trigger-form">
    <div class="radio-group">
      <label><input type="radio" v-model="trigger.type" value="nats"> NATS</label>
      <label><input type="radio" v-model="trigger.type" value="http"> HTTP</label>
      <label><input type="radio" v-model="trigger.type" value="schedule"> Schedule</label>
    </div>

    <!-- NATS trigger -->
    <div v-if="trigger.type === 'nats'" class="fields">
      <div class="field">
        <label>Subject</label>
        <input
          v-model="trigger.nats.subject"
          placeholder="sensors.temperature.>"
          :class="{ error: errorFor('trigger.nats.subject') }"
        >
        <span class="field-error" v-if="errorFor('trigger.nats.subject')">
          {{ errorFor('trigger.nats.subject').message }}
        </span>
        <span class="field-hint">Supports * and > wildcards</span>
      </div>
      <label class="checkbox">
        <input type="checkbox" :checked="!!trigger.nats.debounce" @change="toggleDebounce(trigger.nats)">
        Debounce
      </label>
      <DebounceEditor v-if="trigger.nats.debounce" :debounce="trigger.nats.debounce" :error-for="errorFor" prefix="trigger.nats.debounce" />
    </div>

    <!-- HTTP trigger -->
    <div v-if="trigger.type === 'http'" class="fields">
      <div class="field">
        <label>Path</label>
        <input
          v-model="trigger.http.path"
          placeholder="/webhooks/github"
          :class="{ error: errorFor('trigger.http.path') }"
        >
        <span class="field-error" v-if="errorFor('trigger.http.path')">
          {{ errorFor('trigger.http.path').message }}
        </span>
      </div>
      <div class="field">
        <label>Method <span class="optional">(optional)</span></label>
        <select v-model="trigger.http.method">
          <option value="">Any</option>
          <option v-for="m in ['GET','POST','PUT','PATCH','DELETE']" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
      <label class="checkbox">
        <input type="checkbox" :checked="!!trigger.http.debounce" @change="toggleDebounce(trigger.http)">
        Debounce
      </label>
      <DebounceEditor v-if="trigger.http.debounce" :debounce="trigger.http.debounce" :error-for="errorFor" prefix="trigger.http.debounce" />
    </div>

    <!-- Schedule trigger -->
    <div v-if="trigger.type === 'schedule'" class="fields">
      <CronBuilder :schedule="trigger.schedule" :error-for="errorFor" />
    </div>
  </div>
</template>

<script setup>
import { createDebounce, createHMAC } from '../utils/state.js'
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

function toggleHMAC(target) {
  target.hmac = target.hmac ? null : createHMAC()
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
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
        >
        <span class="field-error" v-if="errorFor('trigger.nats.subject')">
          {{ errorFor('trigger.nats.subject').message }}
        </span>
        <span class="field-hint">Supports * and > wildcards</span>
      </div>
      <label class="checkbox">
        <input type="checkbox" v-model="trigger.nats.reply">
        Request/Reply responder
      </label>
      <span class="field-hint">Subscribe via core NATS and answer each request with a respond action (msg.Respond). The action is forced to Respond.</span>
      <div v-if="trigger.nats.reply" class="field">
        <label>Queue <span class="optional">(optional)</span></label>
        <input
          v-model="trigger.nats.queue"
          placeholder="geocoders"
          :class="{ error: errorFor('trigger.nats.queue') }"
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
        >
        <span class="field-hint">Load-balance requests across responder instances</span>
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
          autocapitalize="off"
          autocorrect="off"
          autocomplete="off"
          spellcheck="false"
          inputmode="url"
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
        <input type="checkbox" :checked="!!trigger.http.hmac" @change="toggleHMAC(trigger.http)">
        HMAC verification
      </label>
      <span class="field-hint">Verify a shared-secret HMAC over the raw body before the rule fires. Invalid or missing signature → 401.</span>
      <div v-if="trigger.http.hmac" class="fields hmac-fields">
        <div class="field">
          <label>Signature header</label>
          <input
            v-model="trigger.http.hmac.header"
            placeholder="X-Hub-Signature-256"
            :class="{ error: errorFor('trigger.http.hmac.header') }"
            autocapitalize="off"
            autocorrect="off"
            autocomplete="off"
            spellcheck="false"
          >
          <span class="field-error" v-if="errorFor('trigger.http.hmac.header')">
            {{ errorFor('trigger.http.hmac.header').message }}
          </span>
        </div>
        <div class="field">
          <label>Secret</label>
          <input
            v-model="trigger.http.hmac.secret"
            placeholder="${GITHUB_WEBHOOK_SECRET}"
            :class="{ error: errorFor('trigger.http.hmac.secret') }"
            autocapitalize="off"
            autocorrect="off"
            autocomplete="off"
            spellcheck="false"
          >
          <span class="field-hint">Literal, env <code>${VAR}</code>, or KV <code>{@kv.bucket.key}</code></span>
        </div>
        <div class="field">
          <label>Algorithm</label>
          <select v-model="trigger.http.hmac.algorithm">
            <option value="sha256">sha256</option>
            <option value="sha1">sha1</option>
          </select>
        </div>
        <div class="field">
          <label>Encoding</label>
          <select v-model="trigger.http.hmac.encoding">
            <option value="hex">hex</option>
            <option value="base64">base64</option>
          </select>
        </div>
        <div class="field">
          <label>Prefix <span class="optional">(optional)</span></label>
          <input
            v-model="trigger.http.hmac.prefix"
            placeholder="sha256="
            autocapitalize="off"
            autocorrect="off"
            autocomplete="off"
            spellcheck="false"
          >
          <span class="field-hint">Stripped from the header value before decoding</span>
        </div>
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

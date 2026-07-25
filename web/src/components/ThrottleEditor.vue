<script setup>
// Editor for a `throttle` block. Trigger throttles are leading-only (a trigger
// throttle exists to skip evaluation, which holding a message cannot do), so
// the mode selector is hidden unless allowTrailing is set.
const props = defineProps({
  throttle: Object,
  errorFor: Function,
  prefix: String,
  allowTrailing: { type: Boolean, default: false },
})
</script>

<template>
  <div class="throttle-editor">
    <div class="field-row">
      <div class="field">
        <label>Window</label>
        <input
          v-model="throttle.window"
          placeholder="5s"
          :class="{ error: errorFor(`${prefix}.window`) }"
        >
        <span class="field-error" v-if="errorFor(`${prefix}.window`)">
          {{ errorFor(`${prefix}.window`).message }}
        </span>
      </div>
      <div class="field" v-if="allowTrailing">
        <label>Mode</label>
        <select v-model="throttle.mode">
          <option value="leading">leading — fire first, drop the rest</option>
          <option value="trailing">trailing — hold, emit the last value</option>
        </select>
      </div>
      <div class="field">
        <label>Key <span class="optional">(optional)</span></label>
        <input v-model="throttle.key" placeholder="{@subject.2}">
      </div>
    </div>
    <p class="throttle-hint" v-if="throttle.mode === 'trailing'">
      Holds the action for the window and emits whichever value arrived last.
      Adds up to one window of latency and is at-most-once — not for alerting.
      The window is fixed from the first message, not reset on each one.
    </p>
  </div>
</template>

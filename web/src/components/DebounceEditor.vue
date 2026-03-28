<script setup>
const props = defineProps({
  debounce: Object,
  errorFor: Function,
  prefix: String,
})
</script>

<template>
  <div class="debounce-editor">
    <div class="field-row">
      <div class="field">
        <label>Window</label>
        <input
          v-model="debounce.window"
          placeholder="5s"
          :class="{ error: errorFor(`${prefix}.window`) }"
        >
        <span class="field-error" v-if="errorFor(`${prefix}.window`)">
          {{ errorFor(`${prefix}.window`).message }}
        </span>
      </div>
      <div class="field">
        <label>Key <span class="optional">(optional)</span></label>
        <input v-model="debounce.key" placeholder="{@subject.2}">
      </div>
    </div>
  </div>
</template>

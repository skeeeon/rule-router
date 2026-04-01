<script setup>
import { ref, watch } from 'vue'
import { parseAndExtract } from '../utils/json-fields.js'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
})

const emit = defineEmits(['update:modelValue'])

const jsonInput = ref('')
const error = ref(null)
const expanded = ref(false)
const copiedPath = ref(null)

watch(jsonInput, (val) => {
  const result = parseAndExtract(val)
  error.value = result.error
  emit('update:modelValue', result.fields)
})

function typeLabel(type) {
  const labels = { string: 'str', number: 'num', boolean: 'bool', array: 'arr', object: 'obj', null: 'null' }
  return labels[type] || type
}

function copyField(path) {
  const text = `{${path}}`
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text)
  } else {
    // Fallback for non-secure contexts (http://localhost)
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  copiedPath.value = path
  setTimeout(() => { copiedPath.value = null }, 1200)
}
</script>

<template>
  <div class="message-inspector" :class="{ expanded }">
    <button class="inspector-toggle" @click="expanded = !expanded">
      <span class="inspector-toggle-icon">{{ expanded ? '&#9662;' : '&#9656;' }}</span>
      Message Inspector
      <span v-if="modelValue.length" class="inspector-badge">{{ modelValue.length }} fields</span>
    </button>

    <div v-if="expanded" class="inspector-body">
      <textarea
        v-model="jsonInput"
        class="inspector-input"
        rows="6"
        placeholder='Paste a sample JSON message to extract field paths for autocomplete...'
        spellcheck="false"
      ></textarea>

      <div v-if="error" class="inspector-error">{{ error }}</div>

      <div v-if="modelValue.length" class="inspector-fields">
        <div
          v-for="f in modelValue"
          :key="f.path"
          class="inspector-field"
          :class="{ copied: copiedPath === f.path }"
          @click="copyField(f.path)"
          :title="`Click to copy {${f.path}} — ${f.sample}`"
        >
          <code class="field-path">{<span>{{ f.path }}</span>}</code>
          <span class="field-type">{{ copiedPath === f.path ? 'copied' : typeLabel(f.type) }}</span>
        </div>
      </div>
      <p v-else-if="jsonInput.trim() && !error" class="hint">No fields extracted.</p>
    </div>
  </div>
</template>

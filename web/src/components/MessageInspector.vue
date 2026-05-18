<script setup>
import { ref, computed, inject, watch } from 'vue'
import { parseAndExtract } from '../utils/json-fields.js'

const sampleMessage = inject('sampleMessage', ref(''))
const inspectedFields = inject('inspectedFields', ref([]))
const activePayloadInsert = inject('activePayloadInsert', ref(null))

const jsonModel = computed({
  get: () => sampleMessage.value,
  set: (v) => { sampleMessage.value = v },
})

const error = ref(null)
const expanded = ref(false)
const copiedPath = ref(null)

watch(sampleMessage, (val) => {
  const result = parseAndExtract(val)
  error.value = result.error
  inspectedFields.value = result.fields
}, { immediate: true })

function typeLabel(type) {
  const labels = { string: 'str', number: 'num', boolean: 'bool', array: 'arr', object: 'obj', null: 'null' }
  return labels[type] || type
}

// Click = insert into the active payload textarea. Falls back to clipboard.
function onFieldClick(path) {
  if (typeof activePayloadInsert.value === 'function') {
    activePayloadInsert.value(path)
    flashCopied(path)
    return
  }
  copyToClipboard(`{${path}}`)
  flashCopied(path)
}

function flashCopied(path) {
  copiedPath.value = path
  setTimeout(() => { copiedPath.value = null }, 1200)
}

function copyToClipboard(text) {
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text)
    return
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  document.execCommand('copy')
  document.body.removeChild(ta)
}
</script>

<template>
  <div class="message-inspector" :class="{ expanded }">
    <button class="inspector-toggle" @click="expanded = !expanded">
      <span class="inspector-toggle-icon">{{ expanded ? '&#9662;' : '&#9656;' }}</span>
      Sample Message
      <span v-if="inspectedFields.length" class="inspector-badge">{{ inspectedFields.length }} fields</span>
    </button>

    <div v-if="expanded" class="inspector-body">
      <textarea
        v-model="jsonModel"
        class="inspector-input"
        rows="6"
        placeholder='Paste a sample JSON message. Used for field autocomplete and rule testing.'
        spellcheck="false"
      ></textarea>

      <div v-if="error" class="inspector-error">{{ error }}</div>

      <div v-if="inspectedFields.length" class="inspector-fields">
        <div
          v-for="f in inspectedFields"
          :key="f.path"
          class="inspector-field"
          :class="{ copied: copiedPath === f.path }"
          @click="onFieldClick(f.path)"
          :title="`Click to insert {${f.path}} into payload (or copy) — ${f.sample}`"
        >
          <code class="field-path">{<span>{{ f.path }}</span>}</code>
          <span class="field-type">{{ copiedPath === f.path ? 'inserted' : typeLabel(f.type) }}</span>
        </div>
      </div>
      <p v-else-if="jsonModel.trim() && !error" class="hint">No fields extracted.</p>
    </div>
  </div>
</template>

<script setup>
import { reactive, watch } from 'vue'
import { uid } from '../utils/state.js'

const props = defineProps({ modelValue: Object })
const emit = defineEmits(['update:modelValue'])

// Convert object to array of {id, key, value} pairs for editing
const entries = reactive(
  Object.entries(props.modelValue).map(([k, v]) => ({ id: uid(), key: k, value: v }))
)

// If entries is empty, start with one blank row
if (entries.length === 0) {
  entries.push({ id: uid(), key: '', value: '' })
}

watch(entries, () => {
  const obj = {}
  for (const e of entries) {
    if (e.key) obj[e.key] = e.value
  }
  emit('update:modelValue', obj)
}, { deep: true })

function addHeader() {
  entries.push({ id: uid(), key: '', value: '' })
}

function removeHeader(index) {
  entries.splice(index, 1)
}
</script>

<template>
  <div class="headers-editor">
    <h3>Headers</h3>
    <div v-for="(entry, i) in entries" :key="entry.id" class="header-row">
      <input v-model="entry.key" placeholder="Header-Name" class="header-key">
      <input v-model="entry.value" placeholder="value" class="header-value">
      <button class="remove-btn" @click="removeHeader(i)">&times;</button>
    </div>
    <button class="small-btn" @click="addHeader">+ Header</button>
  </div>
</template>

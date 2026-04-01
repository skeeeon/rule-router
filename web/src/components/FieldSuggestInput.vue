<script setup>
import { ref, computed, inject, nextTick } from 'vue'

const props = defineProps({
  modelValue: String,
  placeholder: { type: String, default: '' },
  error: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue'])

const inspectedFields = inject('inspectedFields', ref([]))

const inputEl = ref(null)
const showSuggestions = ref(false)
const selectedIndex = ref(0)

// Filter fields based on what's typed after the last `{`
const query = computed(() => {
  const val = props.modelValue || ''
  const lastOpen = val.lastIndexOf('{')
  if (lastOpen === -1) return null
  const afterOpen = val.slice(lastOpen + 1)
  // Don't suggest if there's a closing brace after the opening
  if (afterOpen.includes('}')) return null
  return afterOpen.toLowerCase()
})

const suggestions = computed(() => {
  if (query.value === null || inspectedFields.value.length === 0) return []
  const q = query.value
  // Filter to leaf fields (not object/array containers, except root arrays)
  return inspectedFields.value
    .filter(f => f.type !== 'object')
    .filter(f => !q || f.path.toLowerCase().includes(q))
    .slice(0, 12)
})

const visible = computed(() => showSuggestions.value && suggestions.value.length > 0)

// Show a soft warning when the field has a completed {path} that doesn't match any inspected field
const fieldWarning = computed(() => {
  const val = props.modelValue || ''
  if (!val || inspectedFields.value.length === 0) return false
  // Extract all completed {path} references (not system @ vars)
  const refs = [...val.matchAll(/\{([^}@][^}]*)\}/g)]
  if (refs.length === 0) return false
  const paths = new Set(inspectedFields.value.map(f => f.path))
  return refs.some(m => !paths.has(m[1]))
})

function onInput(e) {
  emit('update:modelValue', e.target.value)
  showSuggestions.value = true
  selectedIndex.value = 0
}

function onFocus() {
  if (query.value !== null && suggestions.value.length > 0) {
    showSuggestions.value = true
  }
}

function onBlur() {
  // Delay to allow click on suggestion
  setTimeout(() => { showSuggestions.value = false }, 150)
}

function onKeydown(e) {
  if (!visible.value) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    selectedIndex.value = (selectedIndex.value + 1) % suggestions.value.length
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    selectedIndex.value = (selectedIndex.value - 1 + suggestions.value.length) % suggestions.value.length
  } else if (e.key === 'Enter' || e.key === 'Tab') {
    if (visible.value) {
      e.preventDefault()
      pick(suggestions.value[selectedIndex.value])
    }
  } else if (e.key === 'Escape') {
    showSuggestions.value = false
  }
}

function pick(field) {
  const val = props.modelValue || ''
  const lastOpen = val.lastIndexOf('{')
  // Replace from the `{` to end with the completed field
  const before = val.slice(0, lastOpen)
  const completed = `{${field.path}}`
  emit('update:modelValue', before + completed)
  showSuggestions.value = false
  nextTick(() => inputEl.value?.focus())
}

function typeLabel(type) {
  const labels = { string: 'str', number: 'num', boolean: 'bool', array: 'arr', null: 'null' }
  return labels[type] || type
}
</script>

<template>
  <div class="field-suggest-wrap">
    <input
      ref="inputEl"
      :value="modelValue"
      :placeholder="placeholder"
      :class="{ error, warn: fieldWarning }"
      :title="fieldWarning ? 'Field not found in sample message' : ''"
      @input="onInput"
      @focus="onFocus"
      @blur="onBlur"
      @keydown="onKeydown"
      autocomplete="off"
    >
    <span v-if="fieldWarning" class="field-warn-hint">not in sample</span>
    <div v-if="visible" class="suggest-dropdown">
      <div
        v-for="(s, i) in suggestions"
        :key="s.path"
        class="suggest-item"
        :class="{ active: i === selectedIndex }"
        @mousedown.prevent="pick(s)"
      >
        <code>{<span>{{ s.path }}</span>}</code>
        <span class="suggest-type">{{ typeLabel(s.type) }}</span>
      </div>
    </div>
  </div>
</template>

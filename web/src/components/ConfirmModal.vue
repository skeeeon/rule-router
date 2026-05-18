<script setup>
import { onMounted, onUnmounted } from 'vue'

const props = defineProps({
  title: { type: String, required: true },
  message: { type: String, required: true },
  confirmLabel: { type: String, default: 'Confirm' },
  cancelLabel: { type: String, default: 'Cancel' },
  danger: { type: Boolean, default: false },
})

const emit = defineEmits(['confirm', 'close'])

function onEscape(e) { if (e.key === 'Escape') emit('close') }
onMounted(() => document.addEventListener('keydown', onEscape))
onUnmounted(() => document.removeEventListener('keydown', onEscape))
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal modal-confirm">
      <div class="modal-header">
        <h2>{{ title }}</h2>
        <button class="remove-btn" @click="emit('close')">&times;</button>
      </div>

      <div class="modal-body">
        <p class="confirm-message">{{ message }}</p>
      </div>

      <div class="modal-footer">
        <button @click="emit('close')">{{ cancelLabel }}</button>
        <button
          class="primary-btn"
          :class="{ 'danger-btn': danger }"
          @click="emit('confirm')"
          autofocus
        >{{ confirmLabel }}</button>
      </div>
    </div>
  </div>
</template>

<script setup>
const props = defineProps({
  rule: Object,
  index: Number,
  errorCount: { type: Number, default: 0 },
  canRemove: { type: Boolean, default: true },
})

const emit = defineEmits(['edit', 'remove', 'duplicate'])

function summary(rule) {
  const t = rule.trigger
  const a = rule.action
  let triggerLabel = ''
  let actionLabel = ''

  if (t.type === 'nats') triggerLabel = `NATS ${t.nats.subject || '...'}`
  else if (t.type === 'http') triggerLabel = `HTTP ${t.http.method || '*'} ${t.http.path || '...'}`
  else if (t.type === 'schedule') triggerLabel = `Cron ${t.schedule.cron || '...'}`

  if (a.type === 'nats') actionLabel = `NATS ${a.nats.subject || '...'}`
  else if (a.type === 'http') actionLabel = `HTTP ${a.http.method || ''} ${a.http.url || '...'}`

  return `${triggerLabel} → ${actionLabel}`
}
</script>

<template>
  <div class="rule-card" :class="{ 'has-errors': errorCount > 0 }" @click="emit('edit')">
    <span class="rule-index">Rule {{ index + 1 }}</span>
    <span
      v-if="errorCount > 0"
      class="rule-error-badge"
      :title="`${errorCount} validation ${errorCount === 1 ? 'error' : 'errors'} — click to edit`"
    >&#9888; {{ errorCount }}</span>
    <span v-if="rule.file" class="rule-file-badge">{{ rule.file }}</span>
    <span class="rule-summary">{{ summary(rule) }}</span>
    <button class="rule-action-btn" @click.stop="emit('duplicate')" title="Duplicate rule" aria-label="Duplicate rule">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
        <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
      </svg>
    </button>
    <button v-if="canRemove" class="rule-action-btn remove" @click.stop="emit('remove')" title="Remove rule">&times;</button>
  </div>
</template>

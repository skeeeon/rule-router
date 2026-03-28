<script setup>
const props = defineProps({
  rule: Object,
  index: Number,
})

const emit = defineEmits(['edit', 'remove'])

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
  <div class="rule-card" @click="emit('edit')">
    <span class="rule-index">Rule {{ index + 1 }}</span>
    <span v-if="rule.file" class="rule-file-badge">{{ rule.file }}</span>
    <span class="rule-summary">{{ summary(rule) }}</span>
    <button class="remove-btn" @click.stop="emit('remove')" title="Remove rule">&times;</button>
  </div>
</template>

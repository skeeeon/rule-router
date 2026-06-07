<script setup>
import { ref, computed } from 'vue'
import { ruleModes, ruleSummary } from '../utils/state.js'

const props = defineProps({
  groups: Array,          // [{ file, rules: [...] }]
  activeRuleId: [Number, String, null],
  errorById: Object,      // { [ruleId]: errorCount }
})

const emit = defineEmits(['select', 'add', 'duplicate', 'remove', 'rename-file', 'new-file'])

const search = ref('')
const collapsed = ref(new Set())

function toggleCollapse(file) {
  const next = new Set(collapsed.value)
  if (next.has(file)) next.delete(file)
  else next.add(file)
  collapsed.value = next
}

// Filter rules within each group by the search term (matches the summary text
// and the filename). Groups with no surviving rules are dropped while searching.
const filteredGroups = computed(() => {
  const q = search.value.trim().toLowerCase()
  return props.groups
    .map(g => {
      if (!q) return g
      const rules = g.rules.filter(r => {
        const { trigger, action } = ruleSummary(r)
        return (
          g.file.toLowerCase().includes(q) ||
          trigger.toLowerCase().includes(q) ||
          action.toLowerCase().includes(q)
        )
      })
      return { ...g, rules }
    })
    .filter(g => g.rules.length > 0)
})

function fileErrorCount(group) {
  return group.rules.reduce((sum, r) => sum + (props.errorById[r.id] || 0), 0)
}

function onRenameFile(group, e) {
  const to = e.target.value.trim()
  if (to && to !== group.file) {
    emit('rename-file', { from: group.file, to })
  } else {
    e.target.value = group.file // revert empty/unchanged edits
  }
}

function summaryOf(rule) {
  return ruleSummary(rule)
}
function modesOf(rule) {
  return ruleModes(rule)
}
</script>

<template>
  <aside class="rule-sidebar">
    <div class="sidebar-header">
      <input
        v-model="search"
        class="sidebar-search"
        type="search"
        placeholder="Search rules…"
        autocapitalize="off"
        autocorrect="off"
        autocomplete="off"
        spellcheck="false"
      >
      <button class="sidebar-newfile" @click="emit('new-file')" title="Create a rule in a new file">+ File</button>
    </div>

    <div class="sidebar-tree">
      <div v-for="group in filteredGroups" :key="group.file" class="sidebar-file">
        <div class="sidebar-file-header">
          <button
            class="file-collapse"
            @click="toggleCollapse(group.file)"
            :aria-expanded="!collapsed.has(group.file)"
            :title="collapsed.has(group.file) ? 'Expand' : 'Collapse'"
          >{{ collapsed.has(group.file) ? '▸' : '▾' }}</button>
          <input
            class="file-name"
            :value="group.file"
            @change="onRenameFile(group, $event)"
            @keydown.enter="$event.target.blur()"
            title="Rename file (applies to all its rules)"
            autocapitalize="off"
            autocorrect="off"
            autocomplete="off"
            spellcheck="false"
          >
          <span
            v-if="fileErrorCount(group) > 0"
            class="file-error-dot"
            :title="`${fileErrorCount(group)} validation error(s) in this file`"
          >●</span>
          <span class="file-count">{{ group.rules.length }}</span>
          <button class="file-add" @click="emit('add', group.file)" title="Add a rule to this file">+</button>
        </div>

        <div v-show="!collapsed.has(group.file)" class="sidebar-rules">
          <div
            v-for="rule in group.rules"
            :key="rule.id"
            class="sidebar-rule"
            :class="{ active: rule.id === activeRuleId, 'has-errors': errorById[rule.id] > 0 }"
            @click="emit('select', rule.id)"
          >
            <div class="sidebar-rule-main">
              <div class="sidebar-rule-summary">
                <span class="sr-trigger">{{ summaryOf(rule).trigger }}</span>
                <span class="sr-arrow">→</span>
                <span class="sr-action">{{ summaryOf(rule).action || '…' }}</span>
              </div>
              <div v-if="modesOf(rule).length" class="sidebar-rule-chips">
                <span v-for="m in modesOf(rule)" :key="m" class="mode-chip" :class="`mode-${m}`">{{ m }}</span>
              </div>
            </div>
            <div class="sidebar-rule-meta">
              <span v-if="errorById[rule.id] > 0" class="sr-error" :title="`${errorById[rule.id]} error(s)`">⚠ {{ errorById[rule.id] }}</span>
              <button class="sr-action-btn" @click.stop="emit('duplicate', rule.id)" title="Duplicate rule" aria-label="Duplicate rule">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
              <button class="sr-action-btn remove" @click.stop="emit('remove', rule.id)" title="Remove rule" aria-label="Remove rule">×</button>
            </div>
          </div>
        </div>
      </div>

      <p v-if="filteredGroups.length === 0" class="sidebar-empty">No rules match “{{ search }}”.</p>
    </div>

    <button class="sidebar-addrule" @click="emit('add', null)">+ Add Rule</button>
  </aside>
</template>

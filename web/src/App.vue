<script setup>
import { reactive, computed, ref, provide, onMounted, onUnmounted, watch } from 'vue'
import { createRule, createConditions, groupRulesByFile, uniqueFileName, DEFAULT_FILENAME, uid } from './utils/state.js'
import { rulesToYaml } from './utils/yaml.js'
import { validateRule } from './utils/validate.js'
import { parseYamlToRules } from './utils/parse.js'
import { buildContextVars } from './utils/context-vars.js'
import TriggerForm from './components/TriggerForm.vue'
import ConditionsBuilder from './components/ConditionsBuilder.vue'
import ActionForm from './components/ActionForm.vue'
import YamlPreview from './components/YamlPreview.vue'
import RuleSidebar from './components/RuleSidebar.vue'
import KvPushModal from './components/KvPushModal.vue'
import KvPullModal from './components/KvPullModal.vue'
import HelpModal from './components/HelpModal.vue'
import MessageInspector from './components/MessageInspector.vue'
import RuleTester from './components/RuleTester.vue'
import SectionPanel from './components/SectionPanel.vue'
import ConfirmModal from './components/ConfirmModal.vue'

const inspectedFields = ref([])
provide('inspectedFields', inspectedFields)

// Shared sample JSON between Inspector and Tester
const sampleMessage = ref('')
provide('sampleMessage', sampleMessage)

// Set by ActionForm while a payload textarea is mounted; called by Inspector chips.
const activePayloadInsert = ref(null)
provide('activePayloadInsert', activePayloadInsert)

const showKvModal = ref(false)
const showKvPull = ref(false)
const showHelp = ref(false)
const showDrawer = ref(false)
const showResetConfirm = ref(false)
const kvPushTarget = ref(null)

// Theme: 'system' | 'light' | 'dark'
const theme = ref(localStorage.getItem('theme') || 'system')

function cycleTheme() {
  const order = ['system', 'light', 'dark']
  const next = order[(order.indexOf(theme.value) + 1) % order.length]
  theme.value = next
  applyTheme(next)
  localStorage.setItem('theme', next)
}

function applyTheme(t) {
  const root = document.documentElement
  if (t === 'dark') {
    root.classList.add('dark')
  } else if (t === 'light') {
    root.classList.remove('dark')
  } else {
    // System preference
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
    root.classList.toggle('dark', prefersDark)
  }
}

const themeIcon = computed(() => {
  if (theme.value === 'light') return '\u2600\uFE0F'   // sun
  if (theme.value === 'dark') return '\uD83C\uDF19'     // crescent moon
  return '\uD83D\uDCBB'                                  // computer (system)
})

const themeTitle = computed(() => {
  if (theme.value === 'light') return 'Theme: Light (click to switch)'
  if (theme.value === 'dark') return 'Theme: Dark (click to switch)'
  return 'Theme: System (click to switch)'
})

// React to OS dark mode changes when theme is 'system'
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  if (theme.value === 'system') applyTheme('system')
})

const state = reactive({
  rules: [createRule()],
  activeRuleId: null,      // stable id of the selected rule (survives reorder/filter)
  activeFile: DEFAULT_FILENAME,
  showConditions: false,
  mobileView: 'list',      // 'list' | 'editor' — master-detail nav on narrow screens
})

// Select the first rule by default; replaced on mount if localStorage restores.
state.activeRuleId = state.rules[0].id

// Assign IDs to a rule tree loaded from JSON (which has no IDs)
function assignIds(rule) {
  rule.id = uid()
  if (rule.conditions) assignConditionIds(rule.conditions)
  if (rule.action?.nats?.filter) assignConditionIds(rule.action.nats.filter)
  if (rule.action?.http?.filter) assignConditionIds(rule.action.http.filter)
  return rule
}

function assignConditionIds(cond) {
  cond.id = uid()
  if (cond.items) cond.items.forEach(item => {
    item.id = uid()
    if (item.conditions) assignConditionIds(item.conditions)
  })
  if (cond.groups) cond.groups.forEach(g => assignConditionIds(g))
}

// Restore from localStorage on mount
onMounted(() => {
  applyTheme(theme.value)
  try {
    const saved = localStorage.getItem('ruleBuilder.rules')
    if (saved) {
      const rules = JSON.parse(saved).map(assignIds)
      if (rules.length > 0) {
        state.rules = rules
        setActive(rules[0].id)
      }
    }
  } catch { /* ignore corrupt data */ }
})

// Auto-save rules to localStorage on change
watch(() => state.rules, (rules) => {
  try {
    localStorage.setItem('ruleBuilder.rules', JSON.stringify(rules))
  } catch { /* quota exceeded — ignore */ }
}, { deep: true })

const activeRule = computed(() => state.rules.find(r => r.id === state.activeRuleId) || null)
const activeIndex = computed(() => state.rules.findIndex(r => r.id === state.activeRuleId))

// Files ▸ Rules tree for the sidebar (real rule refs, grouped by filename).
const groups = computed(() => groupRulesByFile(state.rules))

// Context-variable catalog (@subject.N, @time.*, etc.) derived from the active
// rule's trigger. Provided to FieldSuggestInput for autocomplete.
const contextVars = computed(() => buildContextVars(activeRule.value?.trigger))
provide('contextVars', contextVars)

// Validation errors per rule, indexed alongside state.rules.
const allErrors = computed(() => state.rules.map(r => validateRule(r)))

// Map of ruleId → error count, for sidebar badges.
const errorById = computed(() => {
  const map = {}
  state.rules.forEach((r, i) => { map[r.id] = allErrors.value[i].length })
  return map
})

// Group rules by filename, generate YAML per group, count errors per file.
const files = computed(() => {
  const groups = groupRulesByFile(state.rules)
  return groups.map(g => {
    let errorCount = 0
    for (const r of g.rules) {
      const idx = state.rules.indexOf(r)
      if (idx >= 0) errorCount += allErrors.value[idx].length
    }
    return {
      file: g.file,
      yaml: rulesToYaml(g.rules),
      ruleCount: g.rules.length,
      errorCount,
    }
  })
})

// YAML for the active rule's file group (used by the tester)
const activeFileYaml = computed(() => {
  if (!activeRule.value) return ''
  const file = activeRule.value.file || 'untitled'
  const group = files.value.find(f => f.file === file)
  return group?.yaml || ''
})

function openKvPush(target) {
  kvPushTarget.value = target || null
  showKvModal.value = true
}

function openKvPushAll(allFiles) {
  kvPushTarget.value = allFiles
  showKvModal.value = true
}

const errors = computed(() => (activeIndex.value >= 0 ? allErrors.value[activeIndex.value] : []) || [])

function errorFor(path) {
  return errors.value.find(e => e.path === path)
}

// Update the active selection without forcing a view change (used internally,
// e.g. after deleting a rule).
function setActive(id) {
  state.activeRuleId = id
  const r = state.rules.find(x => x.id === id)
  if (r) {
    state.activeFile = r.file || DEFAULT_FILENAME
    state.showConditions = !!r.conditions
  }
}

// Explicit user selection — also navigates to the editor on narrow screens.
function selectRule(id) {
  setActive(id)
  state.mobileView = 'editor'
}

function backToList() {
  state.mobileView = 'list'
}

// file === null → add to the active rule's file; otherwise add to the named
// file (DEFAULT_FILENAME maps back to the empty-file group).
function addRule(file) {
  const target = file != null ? file : (activeRule.value?.file || '')
  const fileVal = target === DEFAULT_FILENAME ? '' : target
  const rule = createRule(fileVal)
  state.rules.push(rule)
  selectRule(rule.id)
}

function addFile() {
  const rule = createRule(uniqueFileName(state.rules))
  state.rules.push(rule)
  selectRule(rule.id)
}

function duplicateRule(id) {
  const index = state.rules.findIndex(r => r.id === id)
  if (index < 0) return
  const clone = JSON.parse(JSON.stringify(state.rules[index]))
  assignIds(clone)
  state.rules.splice(index + 1, 0, clone)
  selectRule(clone.id)
}

function removeRule(id) {
  if (state.rules.length <= 1) return
  const index = state.rules.findIndex(r => r.id === id)
  if (index < 0) return
  const wasActive = state.activeRuleId === id
  state.rules.splice(index, 1)
  if (wasActive) {
    const next = state.rules[index] || state.rules[index - 1] || state.rules[0]
    if (next) setActive(next.id)
  }
}

// Rename a whole file group — applies to every rule currently in it.
function renameFile({ from, to }) {
  for (const r of state.rules) {
    if ((r.file || DEFAULT_FILENAME) === from) r.file = to
  }
  if (state.activeFile === from) state.activeFile = to
}

function toggleConditions() {
  if (!activeRule.value) return
  state.showConditions = !state.showConditions
  if (state.showConditions && !activeRule.value.conditions) {
    activeRule.value.conditions = createConditions()
  } else if (!state.showConditions) {
    activeRule.value.conditions = null
  }
}

function resetRules() {
  const hasContent = state.rules.length > 1
    || !!state.rules[0]?.trigger?.nats?.subject
    || !!state.rules[0]?.trigger?.http?.path
    || !!state.rules[0]?.trigger?.schedule?.cron
    || !!state.rules[0]?.action?.nats?.subject
    || !!state.rules[0]?.action?.http?.url
  if (hasContent) {
    showResetConfirm.value = true
    return
  }
  performReset()
}

function performReset() {
  const rule = createRule()
  state.rules = [rule]
  setActive(rule.id)
  state.mobileView = 'list'
  localStorage.removeItem('ruleBuilder.rules')
  showResetConfirm.value = false
}

function downloadActiveYaml() {
  if (!activeRule.value) return
  const file = activeRule.value?.file || 'untitled'
  const group = files.value.find(f => f.file === file)
  if (!group) return
  const name = file.endsWith('.yaml') ? file : file + '.yaml'
  const blob = new Blob([group.yaml], { type: 'text/yaml' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

function isTextInput(el) {
  if (!el) return false
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || el.isContentEditable
}

function onGlobalKeydown(e) {
  const mod = e.metaKey || e.ctrlKey
  if (mod && (e.key === 's' || e.key === 'S')) {
    e.preventDefault()
    downloadActiveYaml()
    return
  }
  if (e.key === '?' && !isTextInput(e.target) && !showHelp.value) {
    e.preventDefault()
    showHelp.value = true
  }
}

onMounted(() => document.addEventListener('keydown', onGlobalKeydown))
onUnmounted(() => document.removeEventListener('keydown', onGlobalKeydown))

// Load rules pulled from KV into the builder, replacing current state.
function loadFromKV(entries) {
  const rules = []
  for (const entry of entries) {
    const parsed = parseYamlToRules(entry.yaml, entry.key)
    rules.push(...parsed)
  }
  if (rules.length === 0) return
  state.rules = rules
  setActive(rules[0].id)
  state.mobileView = 'editor'
  showKvPull.value = false
}

</script>

<template>
  <div class="app">
    <header class="app-header">
      <h1>Rule Builder</h1>
      <div class="header-actions">
        <button class="header-btn" @click="resetRules" title="Clear all rules and start fresh">New</button>
        <button class="header-btn" @click="showKvPull = true" title="Load rules from a NATS KV bucket">Load KV</button>
        <button class="help-toggle" @click="showHelp = true" title="Reference">?</button>
        <button class="theme-toggle" @click="cycleTheme" :title="themeTitle">
          {{ themeIcon }}
        </button>
      </div>
    </header>

    <div class="workspace" :class="{ 'show-editor': state.mobileView === 'editor' }">
      <!-- Left: Files ▸ Rules navigation -->
      <RuleSidebar
        :groups="groups"
        :active-rule-id="state.activeRuleId"
        :error-by-id="errorById"
        @select="selectRule"
        @add="addRule"
        @new-file="addFile"
        @duplicate="duplicateRule"
        @remove="removeRule"
        @rename-file="renameFile"
      />

      <!-- Center: editor for the selected rule -->
      <main class="editor-pane">
        <div v-if="activeRule" class="rule-editor">
          <button class="editor-back" @click="backToList">‹ Rules</button>

          <div class="rule-editor-header">
            <span class="rule-editor-label">Rule {{ activeIndex + 1 }}</span>
            <div class="rule-file-input">
              <input
                v-model="activeRule.file"
                placeholder="untitled"
                title="Filename — move this rule by changing its file"
                autocapitalize="off"
                autocorrect="off"
                autocomplete="off"
                spellcheck="false"
              >
            </div>
            <div class="rule-editor-actions">
              <button
                class="rule-action-btn"
                @click="duplicateRule(activeRule.id)"
                title="Duplicate rule"
                aria-label="Duplicate rule"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
              <button
                v-if="state.rules.length > 1"
                class="rule-action-btn remove"
                @click="removeRule(activeRule.id)"
                title="Remove rule"
              >&times;</button>
            </div>
          </div>

          <MessageInspector v-if="activeRule.trigger.type !== 'schedule'" />

          <SectionPanel title="Trigger">
            <TriggerForm :trigger="activeRule.trigger" :error-for="errorFor" />
          </SectionPanel>

          <SectionPanel title="Conditions">
            <template #header-actions>
              <button class="toggle-btn" @click="toggleConditions">
                {{ state.showConditions ? 'Clear' : 'Add Conditions' }}
              </button>
            </template>
            <ConditionsBuilder
              v-if="state.showConditions && activeRule.conditions"
              v-model="activeRule.conditions"
              :error-for="errorFor"
              prefix="conditions"
            />
            <p v-else class="hint">No conditions — action runs on every trigger match.</p>
          </SectionPanel>

          <SectionPanel title="Action">
            <ActionForm :action="activeRule.action" :trigger="activeRule.trigger" :error-for="errorFor" />
          </SectionPanel>

          <RuleTester :rule="activeRule" :yaml="activeFileYaml" />
        </div>

        <div v-else class="editor-empty">
          <p>No rule selected.</p>
          <button class="add-rule-btn" @click="addRule(null)">+ Add Rule</button>
        </div>
      </main>

      <!-- Right: YAML preview rail (wide screens only) -->
      <div class="yaml-rail">
        <YamlPreview :files="files" :active-file="state.activeFile" @push="openKvPush" @push-all="openKvPushAll" />
      </div>
    </div>

    <!-- Narrow screens: floating button + drawer for the YAML preview -->
    <button class="drawer-fab" @click="showDrawer = true">
      YAML
    </button>

    <Transition name="drawer">
      <div v-if="showDrawer" class="drawer-overlay" @click.self="showDrawer = false">
        <div class="drawer">
          <div class="drawer-handle" @click="showDrawer = false">
            <span class="drawer-handle-bar"></span>
          </div>
          <YamlPreview :files="files" @push="openKvPush" @push-all="openKvPushAll" />
        </div>
      </div>
    </Transition>

    <KvPushModal v-if="showKvModal" :target="kvPushTarget" @close="showKvModal = false" />
    <KvPullModal v-if="showKvPull" @close="showKvPull = false" @load="loadFromKV" />
    <HelpModal v-if="showHelp" @close="showHelp = false" />
    <ConfirmModal
      v-if="showResetConfirm"
      title="Clear all rules?"
      message="This will discard the current rules and start fresh. The change cannot be undone."
      confirm-label="Clear rules"
      :danger="true"
      @confirm="performReset"
      @close="showResetConfirm = false"
    />
  </div>
</template>

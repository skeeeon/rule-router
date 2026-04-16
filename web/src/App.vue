<script setup>
import { reactive, computed, ref, provide, onMounted, watch } from 'vue'
import { createRule, createConditions, groupRulesByFile, uid } from './utils/state.js'
import { rulesToYaml } from './utils/yaml.js'
import { validateRule } from './utils/validate.js'
import { parseYamlToRules } from './utils/parse.js'
import TriggerForm from './components/TriggerForm.vue'
import ConditionsBuilder from './components/ConditionsBuilder.vue'
import ActionForm from './components/ActionForm.vue'
import YamlPreview from './components/YamlPreview.vue'
import RuleCard from './components/RuleCard.vue'
import KvPushModal from './components/KvPushModal.vue'
import KvPullModal from './components/KvPullModal.vue'
import HelpModal from './components/HelpModal.vue'
import MessageInspector from './components/MessageInspector.vue'
import RuleTester from './components/RuleTester.vue'

const inspectedFields = ref([])
provide('inspectedFields', inspectedFields)

const showKvModal = ref(false)
const showKvPull = ref(false)
const showHelp = ref(false)
const showDrawer = ref(false)
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
  activeIndex: 0,
  showConditions: false,
})

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
        state.showConditions = !!rules[0].conditions
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

const activeRule = computed(() => state.rules[state.activeIndex])

// Group rules by filename, generate YAML per group
const files = computed(() => {
  const groups = groupRulesByFile(state.rules)
  return groups.map(g => ({
    file: g.file,
    yaml: rulesToYaml(g.rules),
    ruleCount: g.rules.length,
  }))
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

const errors = computed(() => {
  if (!activeRule.value) return []
  return validateRule(activeRule.value)
})

function errorFor(path) {
  return errors.value.find(e => e.path === path)
}

function addRule() {
  const currentFile = activeRule.value?.file || ''
  state.rules.push(createRule(currentFile))
  state.activeIndex = state.rules.length - 1
  state.showConditions = false
}

function duplicateRule(index) {
  const clone = JSON.parse(JSON.stringify(state.rules[index]))
  assignIds(clone)
  state.rules.splice(index + 1, 0, clone)
  state.activeIndex = index + 1
  state.showConditions = !!clone.conditions
}

function removeRule(index) {
  if (state.rules.length <= 1) return
  state.rules.splice(index, 1)
  if (state.activeIndex >= state.rules.length) {
    state.activeIndex = state.rules.length - 1
  }
}

function editRule(index) {
  state.activeIndex = index
  state.showConditions = !!state.rules[index].conditions
}

function collapseRule() {
  state.activeIndex = -1
}

function toggleConditions() {
  state.showConditions = !state.showConditions
  if (state.showConditions && !activeRule.value.conditions) {
    activeRule.value.conditions = createConditions()
  } else if (!state.showConditions) {
    activeRule.value.conditions = null
  }
}

function resetRules() {
  state.rules = [createRule()]
  state.activeIndex = 0
  state.showConditions = false
  localStorage.removeItem('ruleBuilder.rules')
}

// Load rules pulled from KV into the builder, replacing current state.
function loadFromKV(entries) {
  const rules = []
  for (const entry of entries) {
    const parsed = parseYamlToRules(entry.yaml, entry.key)
    rules.push(...parsed)
  }
  if (rules.length === 0) return
  state.rules = rules
  state.activeIndex = 0
  state.showConditions = !!rules[0].conditions
  showKvPull.value = false
}

</script>

<template>
  <div class="app">
    <header class="app-header">
      <h1>Rule Builder</h1>
      <div class="header-actions">
        <button class="header-btn" @click="resetRules" title="Clear all rules and start fresh">New</button>
        <button class="header-btn" @click="showKvPull = true">Load from KV</button>
        <button class="help-toggle" @click="showHelp = true" title="Reference">?</button>
        <button class="theme-toggle" @click="cycleTheme" :title="themeTitle">
          {{ themeIcon }}
        </button>
      </div>
    </header>

    <div class="app-layout">
      <div class="builder-panel">
        <div v-for="(rule, i) in state.rules" :key="rule.id">
          <!-- Collapsed card for non-active rules -->
          <RuleCard
            v-if="i !== state.activeIndex"
            :rule="rule"
            :index="i"
            :can-remove="state.rules.length > 1"
            @edit="editRule(i)"
            @remove="removeRule(i)"
            @duplicate="duplicateRule(i)"
          />

          <!-- Expanded editor for the active rule -->
          <div v-else class="rule-editor">
            <div class="rule-editor-header">
              <span class="rule-editor-label">Rule {{ i + 1 }}</span>
              <div class="rule-file-input">
                <input
                  v-model="rule.file"
                  placeholder="untitled"
                  title="Filename — rules with the same name group into one file"
                >
              </div>
              <div class="rule-editor-actions">
                <button
                  class="rule-action-btn collapse"
                  @click="collapseRule()"
                  title="Collapse rule"
                >&#x25B2;</button>
                <button
                  class="rule-action-btn"
                  @click="duplicateRule(i)"
                  title="Duplicate rule"
                >&#x2398;</button>
                <button
                  v-if="state.rules.length > 1"
                  class="rule-action-btn remove"
                  @click="removeRule(i)"
                  title="Remove rule"
                >&times;</button>
              </div>
            </div>

            <MessageInspector v-model="inspectedFields" />

            <div class="section">
              <h2>Trigger</h2>
              <TriggerForm :trigger="rule.trigger" :error-for="errorFor" />
            </div>

            <div class="section">
              <h2>
                Conditions
                <button class="toggle-btn" @click="toggleConditions">
                  {{ state.showConditions ? 'Clear' : 'Add Conditions' }}
                </button>
              </h2>
              <ConditionsBuilder
                v-if="state.showConditions && rule.conditions"
                v-model="rule.conditions"
                :error-for="errorFor"
                prefix="conditions"
              />
              <p v-else class="hint">No conditions — action runs on every trigger match.</p>
            </div>

            <div class="section">
              <h2>Action</h2>
              <ActionForm :action="rule.action" :error-for="errorFor" />
            </div>

            <RuleTester :rule="rule" :yaml="activeFileYaml" />
          </div>
        </div>

        <button class="add-rule-btn" @click="addRule">+ Add Another Rule</button>
      </div>

      <!-- Desktop: side panel -->
      <div class="preview-panel desktop-only">
        <YamlPreview :files="files" @push="openKvPush" @push-all="openKvPushAll" />
      </div>
    </div>

    <!-- Mobile: floating button + drawer -->
    <button class="drawer-fab mobile-only" @click="showDrawer = true">
      YAML
    </button>

    <Transition name="drawer">
      <div v-if="showDrawer" class="drawer-overlay mobile-only" @click.self="showDrawer = false">
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
  </div>
</template>

<script setup>
import { reactive, computed, ref, provide, onMounted } from 'vue'
import { createRule, createConditions, groupRulesByFile } from './utils/state.js'
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
  document.documentElement.removeAttribute('data-theme')
  if (t === 'light' || t === 'dark') {
    document.documentElement.setAttribute('data-theme', t)
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

onMounted(() => applyTheme(theme.value))

const state = reactive({
  rules: [createRule()],
  activeIndex: 0,
  showConditions: false,
})

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

function toggleConditions() {
  state.showConditions = !state.showConditions
  if (state.showConditions && !activeRule.value.conditions) {
    activeRule.value.conditions = createConditions()
  } else if (!state.showConditions) {
    activeRule.value.conditions = null
  }
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
        <button class="header-btn" @click="showKvPull = true">Load from KV</button>
        <button class="help-toggle" @click="showHelp = true" title="Reference">?</button>
        <button class="theme-toggle" @click="cycleTheme" :title="themeTitle">
          {{ themeIcon }}
        </button>
      </div>
    </header>

    <div class="app-layout">
      <div class="builder-panel">
        <div v-for="(rule, i) in state.rules" :key="i">
          <!-- Collapsed card for non-active rules -->
          <RuleCard
            v-if="i !== state.activeIndex"
            :rule="rule"
            :index="i"
            @edit="editRule(i)"
            @remove="removeRule(i)"
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
              <button
                v-if="state.rules.length > 1"
                class="remove-btn"
                @click="removeRule(i)"
                title="Remove rule"
              >&times;</button>
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
                  {{ state.showConditions ? 'Remove' : 'Add' }}
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

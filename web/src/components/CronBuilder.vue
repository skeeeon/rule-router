<script setup>
import { ref, reactive, computed, watch } from 'vue'
import {
  parseCron, buildCron, emptyState,
  describe, nextRuns, isValid,
  PRESETS, MONTHS, WEEKDAYS,
} from '../utils/cron.js'

const props = defineProps({
  schedule: Object,     // { cron, timezone }
  errorFor: Function,
})

const activeTab = ref('simple')
const state = reactive(emptyState())
const unsupported = ref(false)
let writingFromSimple = false

watch(
  () => props.schedule.cron,
  (val) => {
    if (writingFromSimple) { writingFromSimple = false; return }
    const parsed = parseCron(val)
    if (parsed.ok) {
      unsupported.value = false
      assignState(parsed.state)
    } else {
      unsupported.value = val !== '' && val != null
    }
  },
  { immediate: true },
)

function assignState(next) {
  for (const key of Object.keys(state)) {
    Object.assign(state[key], next[key])
  }
}

function emitFromSimple() {
  if (unsupported.value) return
  writingFromSimple = true
  props.schedule.cron = buildCron(state)
}

watch(state, emitFromSimple, { deep: true })

function applyPreset(cron) {
  props.schedule.cron = cron
  activeTab.value = 'simple'
}

function toggleListValue(field, value) {
  const list = field.list
  const idx = list.indexOf(value)
  if (idx >= 0) list.splice(idx, 1)
  else list.push(value)
}

const cronError = computed(() => props.errorFor?.('trigger.schedule.cron'))
const descText = computed(() => describe(props.schedule.cron))
const runs = computed(() => nextRuns(props.schedule.cron, 3, props.schedule.timezone || ''))
const valid = computed(() => isValid(props.schedule.cron))
const bothDayFieldsSet = computed(
  () => !unsupported.value && state.dom.mode !== 'every' && state.dow.mode !== 'every',
)

function formatRun(d) {
  try {
    const opts = { dateStyle: 'medium', timeStyle: 'short' }
    if (props.schedule.timezone) opts.timeZone = props.schedule.timezone
    return new Intl.DateTimeFormat(undefined, opts).format(d)
  } catch {
    return d.toISOString()
  }
}

function range(n, startAt = 0) {
  return Array.from({ length: n }, (_, i) => i + startAt)
}
</script>

<template>
  <div class="cron-builder">
    <div class="preset-row">
      <button
        v-for="p in PRESETS"
        :key="p.label"
        type="button"
        class="preset-chip"
        :class="{ active: schedule.cron === p.cron }"
        @click="applyPreset(p.cron)"
      >{{ p.label }}</button>
    </div>

    <div class="cron-tabs">
      <button
        type="button"
        class="preview-tab"
        :class="{ active: activeTab === 'simple' }"
        @click="activeTab = 'simple'"
      >Simple</button>
      <button
        type="button"
        class="preview-tab"
        :class="{ active: activeTab === 'advanced' }"
        @click="activeTab = 'advanced'"
      >Advanced</button>
    </div>

    <!-- Simple tab -->
    <div v-if="activeTab === 'simple'" class="simple-panel">
      <div v-if="unsupported" class="unsupported-notice">
        This expression is too complex for the simple builder — edit it in the Advanced tab.
        <div class="expr-preview">{{ schedule.cron }}</div>
      </div>

      <template v-else>
        <!-- Minute -->
        <div class="cron-field">
          <div class="cron-field-label">Minute</div>
          <div class="radio-group compact">
            <label><input type="radio" v-model="state.minute.mode" value="every"> Every</label>
            <label><input type="radio" v-model="state.minute.mode" value="everyN"> Every N</label>
            <label><input type="radio" v-model="state.minute.mode" value="at"> At</label>
            <label><input type="radio" v-model="state.minute.mode" value="range"> Range</label>
            <label><input type="radio" v-model="state.minute.mode" value="list"> Specific</label>
          </div>
          <div class="cron-field-inputs">
            <template v-if="state.minute.mode === 'everyN'">
              <span class="inline-label">Every</span>
              <input type="number" min="1" max="59" v-model.number="state.minute.step" class="num-input">
              <span class="inline-label">minute(s)</span>
            </template>
            <template v-else-if="state.minute.mode === 'at'">
              <span class="inline-label">At minute</span>
              <input type="number" min="0" max="59" v-model.number="state.minute.value" class="num-input">
            </template>
            <template v-else-if="state.minute.mode === 'range'">
              <span class="inline-label">From</span>
              <input type="number" min="0" max="59" v-model.number="state.minute.rangeFrom" class="num-input">
              <span class="inline-label">to</span>
              <input type="number" min="0" max="59" v-model.number="state.minute.rangeTo" class="num-input">
            </template>
            <template v-else-if="state.minute.mode === 'list'">
              <div class="chip-row wrap">
                <button
                  v-for="n in range(60, 0)"
                  :key="n"
                  type="button"
                  class="chip small"
                  :class="{ active: state.minute.list.includes(n) }"
                  @click="toggleListValue(state.minute, n)"
                >{{ n }}</button>
              </div>
            </template>
          </div>
        </div>

        <!-- Hour -->
        <div class="cron-field">
          <div class="cron-field-label">Hour</div>
          <div class="radio-group compact">
            <label><input type="radio" v-model="state.hour.mode" value="every"> Every</label>
            <label><input type="radio" v-model="state.hour.mode" value="everyN"> Every N</label>
            <label><input type="radio" v-model="state.hour.mode" value="at"> At</label>
            <label><input type="radio" v-model="state.hour.mode" value="range"> Range</label>
            <label><input type="radio" v-model="state.hour.mode" value="list"> Specific</label>
          </div>
          <div class="cron-field-inputs">
            <template v-if="state.hour.mode === 'everyN'">
              <span class="inline-label">Every</span>
              <input type="number" min="1" max="23" v-model.number="state.hour.step" class="num-input">
              <span class="inline-label">hour(s)</span>
            </template>
            <template v-else-if="state.hour.mode === 'at'">
              <span class="inline-label">At hour</span>
              <input type="number" min="0" max="23" v-model.number="state.hour.value" class="num-input">
            </template>
            <template v-else-if="state.hour.mode === 'range'">
              <span class="inline-label">From</span>
              <input type="number" min="0" max="23" v-model.number="state.hour.rangeFrom" class="num-input">
              <span class="inline-label">to</span>
              <input type="number" min="0" max="23" v-model.number="state.hour.rangeTo" class="num-input">
            </template>
            <template v-else-if="state.hour.mode === 'list'">
              <div class="chip-row wrap">
                <button
                  v-for="n in range(24, 0)"
                  :key="n"
                  type="button"
                  class="chip small"
                  :class="{ active: state.hour.list.includes(n) }"
                  @click="toggleListValue(state.hour, n)"
                >{{ n }}</button>
              </div>
            </template>
          </div>
        </div>

        <!-- Day of month -->
        <div class="cron-field">
          <div class="cron-field-label">Day of month</div>
          <div class="radio-group compact">
            <label><input type="radio" v-model="state.dom.mode" value="every"> Every</label>
            <label><input type="radio" v-model="state.dom.mode" value="everyN"> Every N</label>
            <label><input type="radio" v-model="state.dom.mode" value="at"> On day</label>
            <label><input type="radio" v-model="state.dom.mode" value="range"> Range</label>
            <label><input type="radio" v-model="state.dom.mode" value="list"> Specific</label>
          </div>
          <div class="cron-field-inputs">
            <template v-if="state.dom.mode === 'everyN'">
              <span class="inline-label">Every</span>
              <input type="number" min="1" max="31" v-model.number="state.dom.step" class="num-input">
              <span class="inline-label">day(s)</span>
            </template>
            <template v-else-if="state.dom.mode === 'at'">
              <span class="inline-label">Day</span>
              <input type="number" min="1" max="31" v-model.number="state.dom.value" class="num-input">
            </template>
            <template v-else-if="state.dom.mode === 'range'">
              <span class="inline-label">From</span>
              <input type="number" min="1" max="31" v-model.number="state.dom.rangeFrom" class="num-input">
              <span class="inline-label">to</span>
              <input type="number" min="1" max="31" v-model.number="state.dom.rangeTo" class="num-input">
            </template>
            <template v-else-if="state.dom.mode === 'list'">
              <div class="chip-row wrap">
                <button
                  v-for="n in 31"
                  :key="n"
                  type="button"
                  class="chip small"
                  :class="{ active: state.dom.list.includes(n) }"
                  @click="toggleListValue(state.dom, n)"
                >{{ n }}</button>
              </div>
            </template>
          </div>
        </div>

        <!-- Month -->
        <div class="cron-field">
          <div class="cron-field-label">Month</div>
          <div class="radio-group compact">
            <label><input type="radio" v-model="state.month.mode" value="every"> Every</label>
            <label><input type="radio" v-model="state.month.mode" value="everyN"> Every N</label>
            <label><input type="radio" v-model="state.month.mode" value="range"> Range</label>
            <label><input type="radio" v-model="state.month.mode" value="list"> Specific</label>
          </div>
          <div class="cron-field-inputs">
            <template v-if="state.month.mode === 'everyN'">
              <span class="inline-label">Every</span>
              <input type="number" min="1" max="12" v-model.number="state.month.step" class="num-input">
              <span class="inline-label">month(s)</span>
            </template>
            <template v-else-if="state.month.mode === 'range'">
              <select v-model.number="state.month.rangeFrom">
                <option v-for="m in MONTHS" :key="m.n" :value="m.n">{{ m.short }}</option>
              </select>
              <span class="inline-label">to</span>
              <select v-model.number="state.month.rangeTo">
                <option v-for="m in MONTHS" :key="m.n" :value="m.n">{{ m.short }}</option>
              </select>
            </template>
            <template v-else-if="state.month.mode === 'list'">
              <div class="chip-row">
                <button
                  v-for="m in MONTHS"
                  :key="m.n"
                  type="button"
                  class="chip"
                  :class="{ active: state.month.list.includes(m.n) }"
                  @click="toggleListValue(state.month, m.n)"
                >{{ m.short }}</button>
              </div>
            </template>
          </div>
        </div>

        <!-- Day of week -->
        <div class="cron-field">
          <div class="cron-field-label">Day of week</div>
          <div class="radio-group compact">
            <label><input type="radio" v-model="state.dow.mode" value="every"> Every</label>
            <label><input type="radio" v-model="state.dow.mode" value="range"> Range</label>
            <label><input type="radio" v-model="state.dow.mode" value="list"> Specific</label>
          </div>
          <div class="cron-field-inputs">
            <template v-if="state.dow.mode === 'range'">
              <select v-model.number="state.dow.rangeFrom">
                <option v-for="d in WEEKDAYS" :key="d.n" :value="d.n">{{ d.short }}</option>
              </select>
              <span class="inline-label">to</span>
              <select v-model.number="state.dow.rangeTo">
                <option v-for="d in WEEKDAYS" :key="d.n" :value="d.n">{{ d.short }}</option>
              </select>
            </template>
            <template v-else-if="state.dow.mode === 'list'">
              <div class="chip-row">
                <button
                  v-for="d in WEEKDAYS"
                  :key="d.n"
                  type="button"
                  class="chip"
                  :class="{ active: state.dow.list.includes(d.n) }"
                  @click="toggleListValue(state.dow, d.n)"
                >{{ d.short }}</button>
              </div>
            </template>
          </div>
        </div>

        <div v-if="bothDayFieldsSet" class="or-warning">
          Cron treats "day of month" and "day of week" as OR — the job runs when either matches.
        </div>
      </template>
    </div>

    <!-- Advanced tab -->
    <div v-else class="advanced-panel fields">
      <div class="field">
        <label>Cron Expression</label>
        <input
          v-model="schedule.cron"
          placeholder="0 8 * * 1-5"
          :class="{ error: !!cronError }"
        >
        <span class="field-error" v-if="cronError">{{ cronError.message }}</span>
        <span class="field-hint">5-field: minute hour day-of-month month day-of-week</span>
      </div>
    </div>

    <!-- Description + next runs -->
    <div class="cron-preview" v-if="schedule.cron">
      <div class="cron-expr">
        <code>{{ schedule.cron }}</code>
        <span v-if="!valid" class="invalid-tag">invalid</span>
      </div>
      <div v-if="descText" class="cron-desc">{{ descText }}</div>
      <div v-if="runs.length" class="cron-runs">
        <span class="runs-label">Next runs:</span>
        <ul>
          <li v-for="(r, i) in runs" :key="i">{{ formatRun(r) }}</li>
        </ul>
      </div>
    </div>

    <div class="field">
      <label>Timezone <span class="optional">(optional)</span></label>
      <input v-model="schedule.timezone" placeholder="America/New_York">
    </div>
  </div>
</template>

<style scoped>
.cron-builder {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.preset-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.preset-chip {
  padding: 5px 10px;
  font-size: 12px;
  font-weight: 500;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--bg-surface);
  color: var(--text-secondary);
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;
}
.preset-chip:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.preset-chip.active {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}

.cron-tabs {
  display: flex;
  border-bottom: 1px solid var(--border);
}

.simple-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 0;
}

.advanced-panel {
  padding: 4px 0;
}

.cron-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px;
  border: 1px solid var(--border-light);
  border-radius: 8px;
  background: var(--bg-surface);
}

.cron-field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  letter-spacing: 0.2px;
}

.radio-group.compact {
  margin-bottom: 0;
  padding: 2px;
  gap: 2px;
}
.radio-group.compact label {
  padding: 5px 8px;
  font-size: 12px;
}

.cron-field-inputs {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  min-height: 4px;
}

.inline-label {
  font-size: 12px;
  color: var(--text-secondary);
}

.num-input {
  width: 64px;
  padding: 6px 8px !important;
  font-size: 13px;
}

.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.chip-row.wrap {
  max-height: 120px;
  overflow-y: auto;
}

.chip {
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-surface);
  color: var(--text-secondary);
  cursor: pointer;
  min-width: 30px;
  transition: background 0.1s ease, color 0.1s ease, border-color 0.1s ease;
}
.chip.small {
  padding: 3px 6px;
  font-size: 11px;
  min-width: 26px;
}
.chip:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.chip.active {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}

.or-warning {
  font-size: 12px;
  color: var(--text-muted);
  padding: 6px 10px;
  background: var(--bg-inset);
  border-radius: 6px;
  border-left: 3px solid var(--accent);
}

.unsupported-notice {
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--bg-inset);
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.4;
}
.expr-preview {
  margin-top: 6px;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', 'Consolas', monospace;
  font-size: 12px;
  color: var(--text-primary);
}

.cron-preview {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  background: var(--bg-inset);
  border-radius: 8px;
  font-size: 13px;
}
.cron-expr {
  display: flex;
  align-items: center;
  gap: 8px;
}
.cron-expr code {
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', 'Consolas', monospace;
  font-size: 13px;
  color: var(--text-primary);
}
.invalid-tag {
  font-size: 11px;
  color: var(--error);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.4px;
}
.cron-desc {
  color: var(--text-secondary);
}
.cron-runs {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  font-size: 12px;
  color: var(--text-muted);
}
.runs-label {
  font-weight: 600;
}
.cron-runs ul {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
</style>

<script setup>
import { ref, nextTick } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '' },
  rows: { type: Number, default: 6 },
  placeholder: String,
  error: Boolean,
  showFormat: { type: Boolean, default: true },
})

const emit = defineEmits(['update:modelValue', 'keyup', 'mouseup'])

const textarea = ref(null)
const formatError = ref('')

defineExpose({
  focus: () => textarea.value?.focus(),
  setSelectionRange: (s, e) => textarea.value?.setSelectionRange(s, e),
  get element() { return textarea.value },
})

const INDENT = '  '
const OPEN_TO_CLOSE = { '{': '}', '[': ']', '"': '"' }

function onInput(e) {
  emit('update:modelValue', e.target.value)
}

function onKeydown(e) {
  const ta = e.target
  const start = ta.selectionStart
  const end = ta.selectionEnd
  const value = ta.value

  if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
    e.preventDefault()
    const lineStart = value.lastIndexOf('\n', start - 1) + 1
    const currentIndent = (value.slice(lineStart, start).match(/^[ \t]*/) || [''])[0]
    const before = value[start - 1]
    const after = value[end]
    const opensBlock = (before === '{' && after === '}') || (before === '[' && after === ']')
    if (opensBlock) {
      const inner = currentIndent + INDENT
      const inserted = '\n' + inner + '\n' + currentIndent
      const newVal = value.slice(0, start) + inserted + value.slice(end)
      const cur = start + 1 + inner.length
      applyEdit(newVal, cur, cur)
    } else {
      const inserted = '\n' + currentIndent
      const newVal = value.slice(0, start) + inserted + value.slice(end)
      const cur = start + inserted.length
      applyEdit(newVal, cur, cur)
    }
    return
  }

  if (e.key === 'Tab') {
    e.preventDefault()
    if (start === end && !e.shiftKey) {
      const newVal = value.slice(0, start) + INDENT + value.slice(end)
      const cur = start + INDENT.length
      applyEdit(newVal, cur, cur)
      return
    }
    // Indent / dedent by line
    const firstLineStart = value.lastIndexOf('\n', start - 1) + 1
    const before = value.slice(0, firstLineStart)
    const after = value.slice(end)
    const region = value.slice(firstLineStart, end)
    const lines = region.split('\n')
    let firstDelta = 0
    let totalDelta = 0
    const newLines = lines.map((ln, i) => {
      if (e.shiftKey) {
        const m = ln.match(/^(  |\t| )/)
        if (m) {
          if (i === 0) firstDelta = -m[0].length
          totalDelta -= m[0].length
          return ln.slice(m[0].length)
        }
        return ln
      }
      if (i === 0) firstDelta = INDENT.length
      totalDelta += INDENT.length
      return INDENT + ln
    })
    const newVal = before + newLines.join('\n') + after
    applyEdit(newVal, start + firstDelta, end + totalDelta)
    return
  }

  if (e.key === 'Escape') {
    ta.blur()
    return
  }

  // Auto-pair opening char
  if (e.key in OPEN_TO_CLOSE && !e.ctrlKey && !e.metaKey && !e.altKey) {
    const close = OPEN_TO_CLOSE[e.key]
    const after = value[end]
    if (start !== end) {
      // Wrap selection
      e.preventDefault()
      const sel = value.slice(start, end)
      const newVal = value.slice(0, start) + e.key + sel + close + value.slice(end)
      applyEdit(newVal, start + 1, end + 1)
      return
    }
    // Skip-over matching quote if next char is the same
    if (e.key === '"' && after === '"') {
      e.preventDefault()
      applyEdit(value, start + 1, start + 1)
      return
    }
    // Only auto-close when not followed by a word char
    if (!/\w/.test(after || '')) {
      e.preventDefault()
      const newVal = value.slice(0, start) + e.key + close + value.slice(end)
      applyEdit(newVal, start + 1, start + 1)
      return
    }
  }

  // Skip-over close char when it's already there
  if ((e.key === '}' || e.key === ']') && start === end && value[start] === e.key) {
    e.preventDefault()
    applyEdit(value, start + 1, start + 1)
    return
  }

  // Backspace at cursor between matching pair deletes both
  if (e.key === 'Backspace' && start === end && start > 0) {
    const b = value[start - 1]
    const a = value[start]
    if (OPEN_TO_CLOSE[b] === a) {
      e.preventDefault()
      const newVal = value.slice(0, start - 1) + value.slice(start + 1)
      applyEdit(newVal, start - 1, start - 1)
      return
    }
  }
}

function applyEdit(newValue, selStart, selEnd) {
  emit('update:modelValue', newValue)
  nextTick(() => {
    const ta = textarea.value
    if (ta) ta.setSelectionRange(selStart, selEnd)
  })
}

// JSON formatting that tolerates template syntax like {field} or {@timestamp()}.
function formatJson() {
  formatError.value = ''
  const raw = props.modelValue
  if (!raw || !raw.trim()) return
  try {
    const { sanitized, templates } = substituteTemplates(raw)
    const parsed = JSON.parse(sanitized)
    const out = restoreTemplates(JSON.stringify(parsed, null, 2), templates)
    emit('update:modelValue', out)
  } catch (e) {
    formatError.value = e?.message || 'Invalid JSON'
    setTimeout(() => { formatError.value = '' }, 4000)
  }
}

// Replace each template with a placeholder. Outside a string, use "__TPLV_N__"
// (so the placeholder is a valid JSON string value). Inside a string, use
// __TPLS_N__ (so the host string stays well-formed). Templates are defined as
// {body} where body has no {, }, ", or newline.
function substituteTemplates(str) {
  const templates = []
  let out = ''
  let i = 0
  let inString = false
  while (i < str.length) {
    const ch = str[i]
    if (inString) {
      if (ch === '\\' && i + 1 < str.length) {
        out += ch + str[i + 1]; i += 2; continue
      }
      if (ch === '"') { inString = false; out += ch; i++; continue }
      if (ch === '{') {
        const end = str.indexOf('}', i + 1)
        if (end > i) {
          const body = str.slice(i + 1, end)
          if (!/[{}"\n]/.test(body)) {
            templates.push(str.slice(i, end + 1))
            out += `__TPLS_${templates.length - 1}__`
            i = end + 1
            continue
          }
        }
      }
      out += ch; i++; continue
    }
    if (ch === '"') { inString = true; out += ch; i++; continue }
    if (ch === '{') {
      const end = str.indexOf('}', i + 1)
      if (end > i) {
        const body = str.slice(i + 1, end)
        if (!/[{}"\n]/.test(body)) {
          templates.push(str.slice(i, end + 1))
          out += `"__TPLV_${templates.length - 1}__"`
          i = end + 1
          continue
        }
      }
    }
    out += ch; i++
  }
  return { sanitized: out, templates }
}

function restoreTemplates(str, templates) {
  return str
    .replace(/"__TPLV_(\d+)__"/g, (_, i) => templates[parseInt(i, 10)])
    .replace(/__TPLS_(\d+)__/g, (_, i) => templates[parseInt(i, 10)])
}
</script>

<template>
  <div class="json-textarea">
    <textarea
      ref="textarea"
      :value="modelValue"
      :rows="rows"
      :placeholder="placeholder"
      :class="{ error }"
      spellcheck="false"
      autocomplete="off"
      autocapitalize="off"
      autocorrect="off"
      @input="onInput"
      @keydown="onKeydown"
      @keyup="$emit('keyup', $event)"
      @mouseup="$emit('mouseup', $event)"
    ></textarea>
    <div class="jt-toolbar" v-if="showFormat">
      <button type="button" class="jt-format" @click="formatJson" title="Format JSON (templates preserved)">Format</button>
      <span v-if="formatError" class="jt-format-error">{{ formatError }}</span>
    </div>
  </div>
</template>

<style scoped>
.json-textarea {
  position: relative;
  display: flex;
  flex-direction: column;
}
.json-textarea textarea {
  padding: 9px 12px;
  padding-right: 70px;
  border: 1px solid var(--border-input);
  border-radius: 6px;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', 'Consolas', monospace;
  font-size: 13px;
  background: var(--bg-surface);
  color: var(--text-primary);
  resize: vertical;
  min-height: 80px;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
  tab-size: 2;
}
.json-textarea textarea:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(74, 144, 217, 0.12);
}
.json-textarea textarea.error {
  border-color: var(--error);
  box-shadow: 0 0 0 3px rgba(220, 53, 69, 0.08);
}
.json-textarea textarea::placeholder {
  color: var(--text-placeholder);
}

.jt-toolbar {
  position: absolute;
  top: 6px;
  right: 6px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.jt-format {
  padding: 3px 8px;
  font-size: 11px;
  font-weight: 500;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-surface);
  color: var(--text-secondary);
  cursor: pointer;
  transition: background 0.1s ease, color 0.1s ease, border-color 0.1s ease;
}
.jt-format:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.jt-format-error {
  font-size: 11px;
  color: var(--error);
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  background: var(--bg-surface);
  padding: 2px 6px;
  border-radius: 4px;
  border: 1px solid var(--error);
}
</style>

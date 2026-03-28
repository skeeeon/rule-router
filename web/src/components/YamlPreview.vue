<script setup>
import { ref, computed } from 'vue'

const props = defineProps({
  files: Array,  // [{ file: string, yaml: string, ruleCount: number }]
})

const emit = defineEmits(['push', 'push-all'])

const activeTab = ref(0)
const copied = ref(false)

const activeFile = computed(() => props.files[activeTab.value] || props.files[0])

function clampTab() {
  if (activeTab.value >= props.files.length) {
    activeTab.value = Math.max(0, props.files.length - 1)
  }
}

async function copyToClipboard() {
  clampTab()
  const text = activeFile.value?.yaml || ''
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  copied.value = true
  setTimeout(() => copied.value = false, 2000)
}

function download() {
  clampTab()
  const f = activeFile.value
  if (!f) return
  downloadFile(f.file, f.yaml)
}

function downloadAll() {
  for (const f of props.files) {
    downloadFile(f.file, f.yaml)
  }
}

function downloadFile(name, content) {
  const blob = new Blob([content], { type: 'text/yaml' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name.endsWith('.yaml') ? name : name + '.yaml'
  a.click()
  URL.revokeObjectURL(url)
}

function pushFile() {
  clampTab()
  const f = activeFile.value
  if (!f) return
  emit('push', { file: f.file, yaml: f.yaml })
}

function pushAll() {
  emit('push-all', props.files)
}
</script>

<template>
  <div class="yaml-preview">
    <div class="preview-header">
      <h2>YAML Preview</h2>
      <div class="preview-actions">
        <button @click="copyToClipboard" :class="{ success: copied }">
          {{ copied ? 'Copied' : 'Copy' }}
        </button>
        <button @click="download">Download</button>
        <template v-if="files.length > 1">
          <button @click="downloadAll">Download All</button>
          <button @click="pushAll">Push All</button>
        </template>
        <button @click="pushFile">Push to KV</button>
      </div>
    </div>

    <!-- File tabs -->
    <div v-if="files.length > 1" class="preview-tabs">
      <button
        v-for="(f, i) in files"
        :key="f.file"
        class="preview-tab"
        :class="{ active: activeTab === i }"
        @click="activeTab = i"
      >
        {{ f.file }}<span class="tab-count">{{ f.ruleCount }}</span>
      </button>
    </div>

    <pre class="yaml-code"><code>{{ activeFile?.yaml || '# Build a rule to see the YAML preview' }}</code></pre>
  </div>
</template>

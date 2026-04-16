<script setup>
import { reactive, ref, computed, onMounted, onUnmounted } from 'vue'
import { connectAndPush, pushMultiple, testConnection } from '../utils/nats.js'

const props = defineProps({
  // Single file: { file: string, yaml: string }
  // Multiple files: [{ file, yaml }, ...]
  target: [Object, Array],
})
const emit = defineEmits(['close'])

const isMulti = computed(() => Array.isArray(props.target))

const form = reactive({
  url: 'ws://localhost:9222',
  bucket: 'rules',
  key: '',
  creds: null,
})

const status = ref('')
const statusType = ref('')
const loading = ref(false)

function onEscape(e) { if (e.key === 'Escape') emit('close') }

onMounted(() => {
  document.addEventListener('keydown', onEscape)
  form.url = sessionStorage.getItem('kv.url') || 'ws://localhost:9222'
  form.bucket = sessionStorage.getItem('kv.bucket') || 'rules'

  // Pre-fill key from the target filename (single file mode)
  if (!isMulti.value && props.target?.file) {
    form.key = props.target.file
  } else {
    form.key = sessionStorage.getItem('kv.key') || ''
  }
})

onUnmounted(() => { document.removeEventListener('keydown', onEscape) })

function saveSettings() {
  sessionStorage.setItem('kv.url', form.url)
  sessionStorage.setItem('kv.bucket', form.bucket)
  if (!isMulti.value) {
    sessionStorage.setItem('kv.key', form.key)
  }
}

function onCredsFile(event) {
  const file = event.target.files[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { form.creds = reader.result }
  reader.readAsText(file)
}

async function test() {
  loading.value = true
  status.value = ''
  try {
    await testConnection({ url: form.url, creds: form.creds })
    status.value = 'Connection successful'
    statusType.value = 'success'
  } catch (err) {
    status.value = `Connection failed: ${err.message}`
    statusType.value = 'error'
  } finally {
    loading.value = false
  }
}

async function push() {
  saveSettings()
  loading.value = true
  status.value = ''

  try {
    if (isMulti.value) {
      await pushMultiple({
        url: form.url,
        creds: form.creds,
        bucket: form.bucket,
        files: props.target,
      })
      status.value = `Pushed ${props.target.length} files to ${form.bucket}/`
      statusType.value = 'success'
    } else {
      // Push single file
      if (!form.key) {
        status.value = 'Key name is required'
        statusType.value = 'error'
        loading.value = false
        return
      }
      await connectAndPush({
        url: form.url,
        creds: form.creds,
        bucket: form.bucket,
        key: form.key,
        yamlString: props.target?.yaml || '',
      })
      status.value = `Pushed to ${form.bucket}/${form.key}`
      statusType.value = 'success'
    }
  } catch (err) {
    status.value = `Push failed: ${err.message}`
    statusType.value = 'error'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal">
      <div class="modal-header">
        <h2>{{ isMulti ? 'Push All Files to NATS KV' : 'Push to NATS KV' }}</h2>
        <button class="remove-btn" @click="emit('close')">&times;</button>
      </div>

      <div class="modal-body">
        <div class="field">
          <label>NATS URL</label>
          <input v-model="form.url" placeholder="ws://localhost:9222">
        </div>
        <div class="field">
          <label>Credentials File <span class="optional">(.creds)</span></label>
          <input type="file" accept=".creds" @change="onCredsFile">
        </div>
        <div class="field">
          <label>Bucket</label>
          <input v-model="form.bucket" placeholder="rules">
        </div>

        <!-- Single file: editable key -->
        <div v-if="!isMulti" class="field">
          <label>Key</label>
          <input v-model="form.key" placeholder="sensors.tank">
          <span class="field-hint">Dot-separated namespace (e.g., sensors.tank)</span>
        </div>

        <!-- Multi-file: show the files that will be pushed -->
        <div v-else class="push-file-list">
          <label>Files to push</label>
          <div v-for="f in target" :key="f.file" class="push-file-item">
            <span class="push-file-key">{{ form.bucket }}/{{ f.file }}</span>
            <span class="push-file-rules">{{ f.ruleCount }} rule{{ f.ruleCount !== 1 ? 's' : '' }}</span>
          </div>
        </div>

        <div v-if="status" class="status" :class="statusType">{{ status }}</div>
      </div>

      <div class="modal-footer">
        <button @click="test" :disabled="loading">Test Connection</button>
        <button @click="push" :disabled="loading" class="primary-btn">
          {{ isMulti ? `Push ${target.length} Files` : 'Push' }}
        </button>
      </div>
    </div>
  </div>
</template>

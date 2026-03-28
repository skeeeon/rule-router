<script setup>
import { reactive, ref, onMounted } from 'vue'
import { pullFromKV, testConnection } from '../utils/nats.js'

const emit = defineEmits(['close', 'load'])

const form = reactive({
  url: 'ws://localhost:9222',
  bucket: 'rules',
  creds: null,
})

const status = ref('')
const statusType = ref('')
const loading = ref(false)
const entries = ref(null) // null = not fetched, [] = empty bucket
const selected = ref(new Set())

onMounted(() => {
  form.url = sessionStorage.getItem('kv.url') || 'ws://localhost:9222'
  form.bucket = sessionStorage.getItem('kv.bucket') || 'rules'
})

function onCredsFile(event) {
  const file = event.target.files[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { form.creds = reader.result }
  reader.readAsText(file)
}

async function fetchKeys() {
  loading.value = true
  status.value = ''
  entries.value = null
  try {
    const result = await pullFromKV({
      url: form.url,
      creds: form.creds,
      bucket: form.bucket,
    })
    entries.value = result
    // Select all by default
    selected.value = new Set(result.map(e => e.key))
    if (result.length === 0) {
      status.value = 'Bucket is empty'
      statusType.value = 'error'
    } else {
      status.value = `Found ${result.length} key${result.length !== 1 ? 's' : ''}`
      statusType.value = 'success'
    }
    // Save connection settings on successful fetch
    sessionStorage.setItem('kv.url', form.url)
    sessionStorage.setItem('kv.bucket', form.bucket)
  } catch (err) {
    status.value = `Failed: ${err.message}`
    statusType.value = 'error'
  } finally {
    loading.value = false
  }
}

function toggleKey(key) {
  if (selected.value.has(key)) {
    selected.value.delete(key)
  } else {
    selected.value.add(key)
  }
}

function toggleAll() {
  if (selected.value.size === entries.value.length) {
    selected.value.clear()
  } else {
    selected.value = new Set(entries.value.map(e => e.key))
  }
}

function loadSelected() {
  const toLoad = entries.value.filter(e => selected.value.has(e.key))
  if (toLoad.length === 0) return
  emit('load', toLoad)
}
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal modal-wide">
      <div class="modal-header">
        <h2>Load Rules from NATS KV</h2>
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

        <button @click="fetchKeys" :disabled="loading" class="primary-btn fetch-btn">
          {{ loading ? 'Fetching...' : 'Fetch Keys' }}
        </button>

        <div v-if="status" class="status" :class="statusType">{{ status }}</div>

        <!-- Key list -->
        <div v-if="entries && entries.length > 0" class="kv-key-list">
          <div class="kv-key-list-header">
            <label class="checkbox">
              <input
                type="checkbox"
                :checked="selected.size === entries.length"
                :indeterminate="selected.size > 0 && selected.size < entries.length"
                @change="toggleAll"
              >
              Select all
            </label>
            <span class="kv-key-count">{{ selected.size }} / {{ entries.length }} selected</span>
          </div>
          <div
            v-for="entry in entries"
            :key="entry.key"
            class="kv-key-item"
            :class="{ selected: selected.has(entry.key) }"
            @click="toggleKey(entry.key)"
          >
            <input type="checkbox" :checked="selected.has(entry.key)" @click.stop="toggleKey(entry.key)">
            <span class="kv-key-name">{{ entry.key }}</span>
            <span class="kv-key-size">{{ entry.yaml.length }} bytes</span>
          </div>
        </div>
      </div>

      <div class="modal-footer">
        <button @click="emit('close')">Cancel</button>
        <button
          v-if="entries && entries.length > 0"
          @click="loadSelected"
          :disabled="selected.size === 0"
          class="primary-btn"
        >
          Load {{ selected.size }} File{{ selected.size !== 1 ? 's' : '' }}
        </button>
      </div>
    </div>
  </div>
</template>

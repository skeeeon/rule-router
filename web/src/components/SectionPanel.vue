<script setup>
import { ref } from 'vue'

const props = defineProps({
  title: { type: String, required: true },
  defaultExpanded: { type: Boolean, default: true },
})

const expanded = ref(props.defaultExpanded)
</script>

<template>
  <div class="section section-panel" :class="{ collapsed: !expanded }">
    <div class="section-header">
      <button
        type="button"
        class="section-toggle"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <span class="section-chevron">{{ expanded ? '▾' : '▸' }}</span>
        <span class="section-title">{{ title }}</span>
      </button>
      <div v-if="$slots['header-actions']" class="section-header-actions">
        <slot name="header-actions"></slot>
      </div>
    </div>
    <div v-show="expanded" class="section-body">
      <slot></slot>
    </div>
  </div>
</template>

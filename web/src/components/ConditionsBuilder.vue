<script setup>
import { createCondition, createConditions } from '../utils/state.js'
import ConditionItem from './ConditionItem.vue'

const props = defineProps({
  modelValue: Object,
  errorFor: Function,
  prefix: { type: String, default: 'conditions' },
  depth: { type: Number, default: 0 },
})

const emit = defineEmits(['update:modelValue'])

const MAX_DEPTH = 4

function addCondition() {
  props.modelValue.items.push(createCondition())
}

function removeCondition(index) {
  props.modelValue.items.splice(index, 1)
}

function addGroup() {
  if (props.depth >= MAX_DEPTH) return
  props.modelValue.groups.push(createConditions())
}

function removeGroup(index) {
  props.modelValue.groups.splice(index, 1)
}
</script>

<template>
  <div class="conditions-builder" :class="`depth-${depth}`">
    <div class="operator-toggle">
      <label><input type="radio" v-model="modelValue.operator" value="and"> AND</label>
      <label><input type="radio" v-model="modelValue.operator" value="or"> OR</label>
    </div>

    <div class="condition-list">
      <ConditionItem
        v-for="(item, i) in modelValue.items"
        :key="i"
        :item="item"
        :error-for="errorFor"
        :prefix="`${prefix}.items.${i}`"
        :depth="depth"
        @remove="removeCondition(i)"
      />
    </div>

    <!-- Nested groups -->
    <div v-for="(group, i) in modelValue.groups" :key="'g' + i" class="condition-group">
      <div class="group-header">
        <span class="group-label">Group</span>
        <button class="remove-btn" @click="removeGroup(i)" title="Remove group">&times;</button>
      </div>
      <ConditionsBuilder
        v-model="modelValue.groups[i]"
        :error-for="errorFor"
        :prefix="`${prefix}.groups.${i}`"
        :depth="depth + 1"
      />
    </div>

    <div class="condition-actions">
      <button class="small-btn" @click="addCondition">+ Condition</button>
      <button v-if="depth < MAX_DEPTH" class="small-btn" @click="addGroup">+ Group</button>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { createCondition, createConditions, countConditions } from '../utils/state.js'
import ConditionItem from './ConditionItem.vue'
import ConfirmModal from './ConfirmModal.vue'

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

// Index of the group awaiting delete confirmation. An empty group is removed
// straight away — nothing to lose, so no prompt.
const pendingGroup = ref(null)

function removeGroup(index) {
  if (countConditions(props.modelValue.groups[index]) === 0) {
    props.modelValue.groups.splice(index, 1)
    return
  }
  pendingGroup.value = index
}

function confirmRemoveGroup() {
  props.modelValue.groups.splice(pendingGroup.value, 1)
  pendingGroup.value = null
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
        :key="item.id"
        :item="item"
        :error-for="errorFor"
        :prefix="`${prefix}.items.${i}`"
        :depth="depth"
        @remove="removeCondition(i)"
      />
    </div>

    <!-- Nested groups -->
    <div v-for="(group, i) in modelValue.groups" :key="group.id" class="condition-group">
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

    <ConfirmModal
      v-if="pendingGroup !== null"
      title="Remove group?"
      :message="`This group and its ${countConditions(modelValue.groups[pendingGroup])} condition(s) will be removed. This cannot be undone.`"
      confirm-label="Remove group"
      :danger="true"
      @confirm="confirmRemoveGroup"
      @close="pendingGroup = null"
    />
  </div>
</template>

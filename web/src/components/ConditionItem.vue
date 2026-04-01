<script setup>
import { computed } from 'vue'
import { VALID_OPERATORS, ARRAY_OPERATORS } from '../utils/validate.js'
import { createConditions } from '../utils/state.js'
import ConditionsBuilder from './ConditionsBuilder.vue'
import FieldSuggestInput from './FieldSuggestInput.vue'

const LIST_OPERATORS = ['in', 'not_in']

const props = defineProps({
  item: Object,
  errorFor: Function,
  prefix: String,
  depth: { type: Number, default: 0 },
})

const emit = defineEmits(['remove'])

const isArrayOp = computed(() => ARRAY_OPERATORS.includes(props.item.operator))
const isListOp = computed(() => LIST_OPERATORS.includes(props.item.operator))

// For in/not_in: display the array as a comma-separated string
const listValueDisplay = computed({
  get() {
    if (Array.isArray(props.item.value)) {
      return props.item.value.join(', ')
    }
    return String(props.item.value ?? '')
  },
  set(text) {
    props.item.value = text.split(',').map(v => v.trim()).filter(v => v !== '')
  },
})

function onOperatorChange() {
  if (ARRAY_OPERATORS.includes(props.item.operator)) {
    if (!props.item.conditions) {
      props.item.conditions = createConditions()
    }
    props.item.value = ''
  } else if (LIST_OPERATORS.includes(props.item.operator)) {
    props.item.conditions = null
    // Convert existing string value to array
    if (typeof props.item.value === 'string' && props.item.value) {
      props.item.value = props.item.value.split(',').map(v => v.trim()).filter(v => v !== '')
    } else if (!Array.isArray(props.item.value)) {
      props.item.value = []
    }
  } else {
    props.item.conditions = null
    // Convert array back to string
    if (Array.isArray(props.item.value)) {
      props.item.value = props.item.value.join(', ')
    }
  }
}
</script>

<template>
  <div class="condition-item">
    <div class="condition-row">
      <div class="field compact">
        <FieldSuggestInput
          v-model="item.field"
          placeholder="{field}"
          :error="!!errorFor(`${prefix}.field`)"
        />
        <span class="field-error" v-if="errorFor(`${prefix}.field`)">
          {{ errorFor(`${prefix}.field`).message }}
        </span>
      </div>
      <div class="field compact">
        <select v-model="item.operator" @change="onOperatorChange">
          <option v-for="op in VALID_OPERATORS" :key="op" :value="op">{{ op }}</option>
        </select>
      </div>

      <!-- in/not_in: comma-separated list input -->
      <div v-if="isListOp" class="field compact">
        <input
          v-model="listValueDisplay"
          placeholder="val1, val2, val3"
          :class="{ error: errorFor(`${prefix}.value`) }"
        >
        <span class="field-error" v-if="errorFor(`${prefix}.value`)">
          {{ errorFor(`${prefix}.value`).message }}
        </span>
      </div>

      <!-- Standard value input -->
      <div v-else-if="!isArrayOp && item.operator !== 'exists'" class="field compact">
        <input
          v-model="item.value"
          placeholder="value"
          :class="{ error: errorFor(`${prefix}.value`) }"
        >
        <span class="field-error" v-if="errorFor(`${prefix}.value`)">
          {{ errorFor(`${prefix}.value`).message }}
        </span>
      </div>

      <button class="remove-btn" @click="emit('remove')" title="Remove condition">&times;</button>
    </div>

    <!-- Nested conditions for array operators -->
    <div v-if="isArrayOp && item.conditions" class="nested-conditions">
      <ConditionsBuilder
        v-model="item.conditions"
        :error-for="errorFor"
        :prefix="`${prefix}.conditions`"
        :depth="depth + 1"
      />
    </div>
  </div>
</template>

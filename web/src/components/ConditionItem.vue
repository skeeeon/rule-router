<script setup>
import { computed } from 'vue'
import { VALID_OPERATORS, ARRAY_OPERATORS } from '../utils/validate.js'
import { createConditions } from '../utils/state.js'
import ConditionsBuilder from './ConditionsBuilder.vue'

const props = defineProps({
  item: Object,
  errorFor: Function,
  prefix: String,
  depth: { type: Number, default: 0 },
})

const emit = defineEmits(['remove'])

const isArrayOp = computed(() => ARRAY_OPERATORS.includes(props.item.operator))

function onOperatorChange() {
  if (ARRAY_OPERATORS.includes(props.item.operator)) {
    if (!props.item.conditions) {
      props.item.conditions = createConditions()
    }
    props.item.value = ''
  } else {
    props.item.conditions = null
  }
}
</script>

<template>
  <div class="condition-item">
    <div class="condition-row">
      <div class="field compact">
        <input
          v-model="item.field"
          placeholder="{field}"
          :class="{ error: errorFor(`${prefix}.field`) }"
        >
        <span class="field-error" v-if="errorFor(`${prefix}.field`)">
          {{ errorFor(`${prefix}.field`).message }}
        </span>
      </div>
      <div class="field compact">
        <select v-model="item.operator" @change="onOperatorChange">
          <option v-for="op in VALID_OPERATORS" :key="op" :value="op">{{ op }}</option>
        </select>
      </div>
      <div v-if="!isArrayOp && item.operator !== 'exists'" class="field compact">
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

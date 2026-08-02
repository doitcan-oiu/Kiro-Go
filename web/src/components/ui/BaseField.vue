<script setup>
// Label + control + hint wrapper. Generates the id/aria wiring once so every
// form row in the app is labelled consistently (§12).
import { useId } from 'vue'

defineProps({
  label: { type: String, default: '' },
  hint: { type: String, default: '' },
  error: { type: String, default: '' },
  required: { type: Boolean, default: false },
})

const uid = useId()
const controlId = `f-${uid}`
const hintId = `h-${uid}`
</script>

<template>
  <div>
    <label v-if="label" :for="controlId" class="field-label">
      {{ label }}
      <span v-if="required" class="text-error" aria-hidden="true">*</span>
    </label>
    <!-- Children bind :id/:aria-describedby from the slot props. -->
    <slot :id="controlId" :described-by="hint || error ? hintId : undefined" />
    <p v-if="error" :id="hintId" class="field-hint text-error">{{ error }}</p>
    <p v-else-if="hint" :id="hintId" class="field-hint">{{ hint }}</p>
  </div>
</template>

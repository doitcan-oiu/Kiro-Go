<script setup>
// Checkbox with an accent-filled checked state. Supports `indeterminate` for
// "some rows selected" headers.
import { computed, useId, watch, ref, onMounted } from 'vue'
import { PhCheck, PhMinus } from '@phosphor-icons/vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  indeterminate: { type: Boolean, default: false },
  label: { type: String, default: '' },
  ariaLabel: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue', 'change'])
const inputId = `cb-${useId()}`
const el = ref(null)

// `indeterminate` is a DOM property, not an attribute, so it must be assigned.
function syncIndeterminate() {
  if (el.value) el.value.indeterminate = props.indeterminate && !props.modelValue
}
onMounted(syncIndeterminate)
watch(() => [props.indeterminate, props.modelValue], syncIndeterminate)

const showMinus = computed(() => props.indeterminate && !props.modelValue)

function onChange(event) {
  const next = event.target.checked
  emit('update:modelValue', next)
  emit('change', next)
}
</script>

<template>
  <label
    :for="inputId"
    class="inline-flex cursor-pointer items-center gap-2 select-none"
    :class="disabled && 'cursor-not-allowed opacity-55'"
  >
    <span class="relative inline-flex h-[18px] w-[18px] shrink-0 items-center justify-center">
      <input
        :id="inputId"
        ref="el"
        type="checkbox"
        class="peer sr-only"
        :checked="modelValue"
        :disabled="disabled"
        :aria-label="ariaLabel || undefined"
        @change="onChange"
      />
      <span
        class="absolute inset-0 rounded-[4px] border border-line-strong bg-surface-input transition-colors duration-[var(--dur-fast)] peer-checked:border-accent peer-checked:bg-accent peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent"
        :class="showMinus && 'border-accent! bg-accent!'"
      />
      <PhCheck
        v-if="modelValue"
        :size="13"
        weight="bold"
        class="relative text-txt-inverse"
        aria-hidden="true"
      />
      <PhMinus
        v-else-if="showMinus"
        :size="13"
        weight="bold"
        class="relative text-txt-inverse"
        aria-hidden="true"
      />
    </span>
    <span v-if="label" class="text-body-sm text-txt-secondary">{{ label }}</span>
  </label>
</template>

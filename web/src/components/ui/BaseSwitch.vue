<script setup>
// Accessible toggle. Renders a real checkbox so it participates in forms and
// screen-reader semantics; the visual track/thumb is styled on top of it.
import { computed, useId } from 'vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  label: { type: String, default: '' },
  hint: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
  /** Optional text shown after the track, e.g. "已启用" / "已停用". */
  stateText: { type: String, default: '' },
})

const emit = defineEmits(['update:modelValue', 'change'])
const inputId = `sw-${useId()}`
const hintId = computed(() => (props.hint ? `${inputId}-hint` : undefined))

function onChange(event) {
  const next = event.target.checked
  emit('update:modelValue', next)
  emit('change', next)
}
</script>

<template>
  <div class="flex items-start gap-3">
    <!-- 44px hit area (§11.2) via the padded label wrapper. -->
    <label
      :for="inputId"
      class="relative inline-flex shrink-0 cursor-pointer items-center py-[10px]"
      :class="disabled && 'cursor-not-allowed opacity-55'"
    >
      <input
        :id="inputId"
        type="checkbox"
        class="peer sr-only"
        :checked="modelValue"
        :disabled="disabled"
        :aria-describedby="hintId"
        @change="onChange"
      />
      <span
        class="block h-6 w-[44px] rounded-full border border-line bg-surface-input transition-colors duration-[var(--dur-fast)] ease-[var(--ease-out-expo)] peer-checked:border-[var(--border-active)] peer-checked:bg-accent-soft peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent"
      />
      <span
        class="pointer-events-none absolute left-[3px] h-[18px] w-[18px] rounded-full bg-txt-tertiary transition-[transform,background-color] duration-[var(--dur-fast)] ease-[var(--ease-spring)] peer-checked:translate-x-[20px] peer-checked:bg-accent peer-checked:shadow-[0_0_10px_var(--accent-glow)]"
      />
    </label>

    <div v-if="label || hint || stateText" class="min-w-0 pt-[10px]">
      <label :for="inputId" class="flex cursor-pointer flex-wrap items-center gap-2">
        <span v-if="label" class="text-body font-medium text-txt">{{ label }}</span>
        <span v-if="stateText" class="text-caption-sm text-txt-tertiary">{{ stateText }}</span>
      </label>
      <p v-if="hint" :id="hintId" class="field-hint">{{ hint }}</p>
    </div>
  </div>
</template>

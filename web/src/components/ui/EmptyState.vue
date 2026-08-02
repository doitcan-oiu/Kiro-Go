<script setup>
defineProps({
  message: { type: String, default: '' },
  /** Reduces vertical padding for use inside small panels. */
  compact: { type: Boolean, default: false },
})
</script>

<template>
  <div
    class="flex flex-col items-center justify-center gap-3 text-center"
    :class="compact ? 'py-xl' : 'py-2xl'"
  >
    <span v-if="$slots.icon" class="text-txt-tertiary opacity-60" aria-hidden="true">
      <slot name="icon" />
    </span>
    <p class="text-body-sm text-txt-tertiary">
      <slot>{{ message }}</slot>
    </p>
    <div v-if="$slots.action"><slot name="action" /></div>
  </div>
</template>

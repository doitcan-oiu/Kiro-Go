<script setup>
// §9.8 状态标签 + §12「色彩不唯一」: every badge pairs its colour with a dot
// shape and a text label, so status never depends on hue alone.
import { computed } from 'vue'

const props = defineProps({
  label: { type: String, required: true },
  /** green | yellow | red | blue | gray */
  tone: { type: String, default: 'gray' },
  /** Renders the leading status dot. */
  dot: { type: Boolean, default: false },
})

const badgeClass = computed(() => `badge badge-${props.tone}`)
const dotClass = computed(
  () =>
    ({
      green: 'status-dot status-dot-green',
      yellow: 'status-dot status-dot-yellow',
      red: 'status-dot status-dot-red',
      blue: 'status-dot status-dot-blue',
      gray: 'status-dot status-dot-idle',
    })[props.tone] || 'status-dot status-dot-idle',
)
</script>

<template>
  <span :class="badgeClass">
    <span v-if="dot" :class="dotClass" aria-hidden="true" />
    <slot>{{ label }}</slot>
  </span>
</template>

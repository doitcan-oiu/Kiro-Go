<script setup>
// §9.2 统计卡片: 120px tall, glass-thin, title + icon top, big number centred,
// change/sub description at the bottom. Upgrades to glass-regular on hover.
import { computed } from 'vue'

const props = defineProps({
  label: { type: String, required: true },
  value: { type: [String, Number], default: '—' },
  sub: { type: String, default: '' },
  /** Tints the value: accent | success | warning | error | neutral */
  tone: { type: String, default: 'neutral' },
  loading: { type: Boolean, default: false },
  /** Icon component (e.g. a Phosphor icon); rendered at 20px per §7.2. */
  icon: { type: [Object, Function], default: null },
})

const toneClass = computed(
  () =>
    ({
      accent: 'text-accent',
      success: 'text-success',
      warning: 'text-warning',
      error: 'text-error',
      neutral: 'text-txt',
    })[props.tone] || 'text-txt',
)
</script>

<template>
  <div
    class="glass-thin glass-hover flex min-h-[120px] min-w-0 flex-col justify-between rounded-2xl p-lg"
  >
    <div class="flex items-start justify-between gap-2">
      <span class="text-caption font-medium tracking-wide text-txt-secondary">{{ label }}</span>
      <span class="shrink-0 text-txt-tertiary" aria-hidden="true">
        <slot name="icon"><component :is="icon" v-if="icon" :size="20" /></slot>
      </span>
    </div>

    <div v-if="loading" class="skeleton h-8 w-24" />
    <div v-else class="text-display-lg tnum leading-none" :class="toneClass">{{ value }}</div>

    <p class="text-caption text-txt-tertiary">
      <slot name="sub">{{ sub }}</slot>
    </p>
  </div>
</template>

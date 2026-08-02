<script setup>
// Quota bar. Colour escalates with utilisation, and the numeric label always
// carries the same information so the bar is never the only signal (§12).
import { computed } from 'vue'

const props = defineProps({
  label: { type: String, default: '' },
  used: { type: Number, default: 0 },
  limit: { type: Number, default: 0 },
  /** Pre-computed 0–100 percentage; overrides used/limit when provided. */
  percent: { type: Number, default: null },
  /** Right-aligned text; defaults to "used / limit". */
  valueText: { type: String, default: '' },
  suffix: { type: String, default: '' },
})

const pct = computed(() => {
  if (props.percent !== null && Number.isFinite(props.percent)) {
    return Math.max(0, Math.min(100, props.percent))
  }
  if (!props.limit || props.limit <= 0) return 0
  return Math.max(0, Math.min(100, (props.used / props.limit) * 100))
})

const level = computed(() => (pct.value >= 95 ? 'critical' : pct.value >= 80 ? 'high' : 'normal'))

const fillClass = computed(
  () =>
    ({
      normal: 'bg-accent',
      high: 'bg-warning',
      critical: 'bg-error',
    })[level.value],
)
</script>

<template>
  <div class="min-w-0">
    <div v-if="label || valueText" class="mb-1.5 flex items-baseline justify-between gap-2">
      <span class="text-caption text-txt-tertiary">{{ label }}</span>
      <span class="text-caption tnum text-txt-secondary">
        {{ valueText }}<span v-if="suffix">{{ suffix }}</span>
      </span>
    </div>
    <div
      class="meter"
      role="progressbar"
      :aria-valuenow="Math.round(pct)"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-label="label || undefined"
    >
      <div class="meter-fill" :class="fillClass" :style="{ width: pct + '%' }" />
    </div>
  </div>
</template>

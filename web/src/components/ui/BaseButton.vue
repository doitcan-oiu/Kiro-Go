<script setup>
// Capsule button per §9.3 / §9.4. Renders as <button> or <a> depending on `href`.
import { computed } from 'vue'
import { PhSpinnerGap } from '@phosphor-icons/vue'

const props = defineProps({
  variant: { type: String, default: 'glass' }, // primary | glass | danger | ghost
  size: { type: String, default: 'md' }, // md | sm | xs
  icon: { type: Boolean, default: false },
  loading: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  href: { type: String, default: '' },
  type: { type: String, default: 'button' },
})

const classes = computed(() => [
  'btn',
  `btn-${props.variant}`,
  props.size === 'sm' && 'btn-sm',
  props.size === 'xs' && 'btn-xs',
  props.icon && 'btn-icon',
])

const isDisabled = computed(() => props.disabled || props.loading)
// Spinner replaces the leading icon slot so the label does not shift.
const iconSize = computed(() => (props.size === 'xs' ? 15 : props.size === 'sm' ? 16 : 18))
</script>

<template>
  <a v-if="href" :href="href" :class="classes" target="_blank" rel="noopener noreferrer">
    <slot name="icon" />
    <slot />
  </a>
  <button v-else :type="type" :class="classes" :disabled="isDisabled" :aria-busy="loading || undefined">
    <PhSpinnerGap v-if="loading" :size="iconSize" class="anim-spin shrink-0" />
    <slot v-else name="icon" />
    <slot />
  </button>
</template>

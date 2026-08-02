<script setup>
// Glass container for the navigation/control layer (§3.1). Content-layer
// surfaces should use `surface-card` instead — see §3.3.
import { computed } from 'vue'

const props = defineProps({
  material: { type: String, default: 'thin' }, // thin | regular | clear | thick
  radius: { type: String, default: 'lg' }, // md | lg | xl
  hover: { type: Boolean, default: false },
  as: { type: String, default: 'div' },
})

const radiusMap = { md: 'rounded-[10px]', lg: 'rounded-2xl', xl: 'rounded-[20px]' }

const classes = computed(() => [
  `glass-${props.material}`,
  radiusMap[props.radius] || radiusMap.lg,
  props.hover && 'glass-hover',
])
</script>

<template>
  <component :is="as" :class="classes">
    <slot />
  </component>
</template>

<script setup>
// Cycles system → light → dark, mirroring the legacy panel's three-state toggle.
import { computed } from 'vue'
import { PhCircleHalf, PhMoon, PhSun } from '@phosphor-icons/vue'
import { cycleTheme, themePref } from '@/lib/theme'
import { useI18n } from '@/lib/i18n'

const { t } = useI18n()

const icon = computed(
  () => ({ system: PhCircleHalf, light: PhSun, dark: PhMoon })[themePref.value] || PhCircleHalf,
)
// Announces the *current* state rather than the next one, so a screen-reader
// user can tell what is active without activating the control.
const label = computed(() => t('theme.status', t(`theme.${themePref.value}`)))
</script>

<template>
  <button
    type="button"
    class="btn btn-ghost btn-icon"
    :title="label"
    :aria-label="label"
    @click="cycleTheme()"
  >
    <component :is="icon" :size="18" />
  </button>
</template>

<script setup>
// Renders the global toast stack. Mounted once from App.vue.
import { PhCheckCircle, PhInfo, PhWarning, PhWarningCircle, PhX } from '@phosphor-icons/vue'
import { dismissToast, pauseToast, resumeToast, toasts } from '@/lib/toast'
import { t } from '@/lib/i18n'

const ICONS = {
  success: PhCheckCircle,
  error: PhWarningCircle,
  warning: PhWarning,
  info: PhInfo,
}

const TONE = {
  success: 'text-accent',
  error: 'text-error',
  warning: 'text-warning',
  info: 'text-accent-secondary',
}

function onBodyClick(item) {
  if (!item.onClick) return
  dismissToast(item.id)
  item.onClick()
}
</script>

<template>
  <div
    class="pointer-events-none fixed inset-x-0 bottom-0 z-[var(--z-toast)] flex flex-col items-center gap-sm p-md sm:items-end sm:p-lg"
    role="region"
    :aria-label="t('aria.notifications')"
  >
    <TransitionGroup name="list">
      <div
        v-for="item in toasts"
        :key="item.id"
        class="glass-thick pointer-events-auto flex w-full max-w-[420px] items-start gap-sm rounded-[10px] p-3 shadow-[var(--sh-lg)]"
        :role="item.variant === 'error' ? 'alert' : 'status'"
        :aria-live="item.variant === 'error' ? 'assertive' : 'polite'"
        @mouseenter="pauseToast(item.id)"
        @mouseleave="resumeToast(item.id)"
      >
        <component
          :is="ICONS[item.variant] || ICONS.info"
          :size="18"
          weight="fill"
          class="mt-px shrink-0"
          :class="TONE[item.variant] || TONE.info"
        />
        <p
          class="min-w-0 flex-1 text-body-sm break-words text-txt"
          :class="item.onClick && 'cursor-pointer'"
          @click="onBodyClick(item)"
        >
          {{ item.message }}
        </p>
        <button
          type="button"
          class="-m-1 shrink-0 rounded p-1 text-txt-tertiary transition-colors hover:text-txt"
          :aria-label="t('common.dismiss')"
          @click="dismissToast(item.id)"
        >
          <PhX :size="14" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

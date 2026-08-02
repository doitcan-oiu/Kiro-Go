<script setup>
// Modal per §9.7: glass-thick panel, 20px radius, scale-in, backdrop blur.
//
// Behaviour carried over from the legacy panel:
//   - focus moves into the dialog on open and is restored on close
//   - Tab is trapped inside the dialog
//   - Esc closes
//   - a backdrop click only closes when both pointerdown *and* click land on the
//     backdrop, so a drag that starts inside the panel and ends outside it does
//     not dismiss the dialog
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { PhX } from '@phosphor-icons/vue'
import { t } from '@/lib/i18n'

const props = defineProps({
  /** `v-model` form. Use this, or `open` + `@close` where a parent owns the state. */
  modelValue: { type: Boolean, default: null },
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  size: { type: String, default: 'md' }, // sm | md | lg | xl
  closeOnBackdrop: { type: Boolean, default: true },
})

const emit = defineEmits(['update:modelValue', 'close'])

// `modelValue` defaults to null so "not bound" is distinguishable from `false`.
const isOpen = computed(() => (props.modelValue === null ? props.open : props.modelValue))

function requestClose() {
  emit('update:modelValue', false)
  emit('close')
}

const titleId = useId()
const panel = ref(null)
let lastFocused = null
let pressedOnBackdrop = false

const widths = {
  sm: 'max-w-[400px]',
  md: 'max-w-[560px]',
  lg: 'max-w-[720px]',
  xl: 'max-w-[960px]',
}
const widthClass = computed(() => widths[props.size] || widths.md)

const FOCUSABLE =
  'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),[tabindex]:not([tabindex="-1"])'

function focusables() {
  if (!panel.value) return []
  return Array.from(panel.value.querySelectorAll(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
}

function onKeydown(e) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    requestClose()
    return
  }
  if (e.key !== 'Tab') return
  const items = focusables()
  if (!items.length) {
    e.preventDefault()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}

// Scroll lock. Using `overflow:hidden` on <body> plus a compensating padding for
// the scrollbar avoids the layout shift that `position:fixed` would cause.
let lockCount = 0
function lockScroll() {
  if (lockCount++) return
  const gap = window.innerWidth - document.documentElement.clientWidth
  document.body.style.overflow = 'hidden'
  if (gap > 0) document.body.style.paddingRight = `${gap}px`
}
function unlockScroll() {
  if (--lockCount > 0) return
  lockCount = 0
  document.body.style.overflow = ''
  document.body.style.paddingRight = ''
}

watch(
  isOpen,
  async (open) => {
    if (open) {
      lastFocused = document.activeElement
      lockScroll()
      await nextTick()
      const items = focusables()
      // Prefer the first field over the close button so keyboard users land on
      // the content, not the dismiss control.
      const target = items.find((el) => !el.dataset.modalClose) || panel.value
      target?.focus?.()
    } else {
      unlockScroll()
      lastFocused?.focus?.()
      lastFocused = null
    }
  },
)

onBeforeUnmount(() => {
  if (isOpen.value) unlockScroll()
})

function onBackdropPointerDown(e) {
  pressedOnBackdrop = e.target === e.currentTarget
}

function onBackdropClick(e) {
  if (!props.closeOnBackdrop) return
  if (pressedOnBackdrop && e.target === e.currentTarget) requestClose()
  pressedOnBackdrop = false
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div
        v-if="isOpen"
        class="fixed inset-0 z-[var(--z-modal)] flex items-start justify-center overflow-y-auto overscroll-contain bg-[rgb(0_0_0/0.6)] p-md backdrop-blur-[2px] sm:items-center sm:p-lg"
        @pointerdown="onBackdropPointerDown"
        @click="onBackdropClick"
      >
        <div
          ref="panel"
          class="glass-thick anim-modal my-auto w-full rounded-[20px] shadow-[var(--sh-lg)] outline-none"
          :class="widthClass"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="title ? titleId : undefined"
          tabindex="-1"
          @keydown="onKeydown"
        >
          <header
            v-if="title || $slots.header"
            class="flex items-start justify-between gap-md border-b border-line px-lg py-md"
          >
            <slot name="header">
              <h2 :id="titleId" class="font-brand text-title-sm text-txt">{{ title }}</h2>
            </slot>
            <button
              type="button"
              data-modal-close="1"
              class="-m-1 shrink-0 rounded-md p-1.5 text-txt-tertiary transition-colors hover:bg-surface-hover hover:text-txt"
              :aria-label="t('common.close')"
              @click="requestClose()"
            >
              <PhX :size="16" />
            </button>
          </header>

          <div class="px-lg py-md">
            <slot />
          </div>

          <footer
            v-if="$slots.footer"
            class="flex flex-wrap items-center justify-end gap-sm border-t border-line px-lg py-md"
          >
            <slot name="footer" />
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

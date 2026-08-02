<script setup>
// Accessible custom listbox replacing the legacy hand-rolled `.custom-select`.
//
// Kept as a real listbox (not a native <select>) so the popup can carry the
// glass material from §3.1. Behaviour preserved from the old implementation:
// fixed positioning that flips above the trigger when space below is tight,
// full keyboard support, and outside-click dismissal.
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { PhCaretDown, PhCheck } from '@phosphor-icons/vue'

const props = defineProps({
  modelValue: { type: [String, Number], default: '' },
  // Options accept `{ value, label, disabled }` or bare strings.
  options: { type: Array, default: () => [] },
  placeholder: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
  ariaLabel: { type: String, default: '' },
  labelledBy: { type: String, default: '' },
  /** 'md' = 40px（默认，与 .field 一致）；'sm' = 36px，用于紧凑工具栏一行内对齐。 */
  size: { type: String, default: 'md' },
})
const emit = defineEmits(['update:modelValue', 'change'])

const uid = useId()
const listboxId = `${uid}-listbox`
const valueId = `${uid}-value`

const open = ref(false)
const activeIndex = ref(-1)
const triggerEl = ref(null)
const listEl = ref(null)
const popupStyle = ref({})

const items = computed(() =>
  props.options.map((opt) =>
    typeof opt === 'object' && opt !== null
      ? { value: opt.value, label: opt.label ?? String(opt.value), disabled: !!opt.disabled }
      : { value: opt, label: String(opt), disabled: false },
  ),
)

const selectedIndex = computed(() => items.value.findIndex((i) => i.value === props.modelValue))
const selectedLabel = computed(() => items.value[selectedIndex.value]?.label ?? '')

/**
 * Positions the popup in the viewport. Opens upward when there is under 180px
 * below and more room above; height is clamped to 96–224px so a long list stays
 * scrollable rather than overflowing the screen.
 */
function place() {
  const trigger = triggerEl.value
  if (!trigger) return
  const rect = trigger.getBoundingClientRect()
  const below = window.innerHeight - rect.bottom
  const above = rect.top
  const flip = below < 180 && above > below
  const available = (flip ? above : below) - 12
  const maxHeight = Math.max(96, Math.min(224, available))

  popupStyle.value = {
    position: 'fixed',
    left: `${rect.left}px`,
    width: `${rect.width}px`,
    maxHeight: `${maxHeight}px`,
    ...(flip ? { bottom: `${window.innerHeight - rect.top + 6}px` } : { top: `${rect.bottom + 6}px` }),
  }
}

function openMenu() {
  if (props.disabled || open.value) return
  open.value = true
  activeIndex.value = selectedIndex.value >= 0 ? selectedIndex.value : firstEnabled()
  place()
  nextTick(() => {
    listEl.value?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  })
}

function closeMenu({ refocus = false } = {}) {
  if (!open.value) return
  open.value = false
  activeIndex.value = -1
  if (refocus) triggerEl.value?.focus()
}

function firstEnabled() {
  return items.value.findIndex((i) => !i.disabled)
}

function pick(index) {
  const item = items.value[index]
  if (!item || item.disabled) return
  if (item.value !== props.modelValue) {
    emit('update:modelValue', item.value)
    emit('change', item.value)
  }
  closeMenu({ refocus: true })
}

/** Moves the active option by `step`, wrapping around and skipping disabled. */
function move(step) {
  const list = items.value
  if (!list.length) return
  let next = activeIndex.value
  for (let i = 0; i < list.length; i += 1) {
    next = (next + step + list.length) % list.length
    if (!list[next].disabled) {
      activeIndex.value = next
      nextTick(() => {
        listEl.value?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
      })
      return
    }
  }
}

function onTriggerKeydown(event) {
  switch (event.key) {
    case 'ArrowDown':
    case 'ArrowUp':
    case 'Enter':
    case ' ':
      event.preventDefault()
      openMenu()
      break
    case 'Escape':
      closeMenu()
      break
    default:
      break
  }
}

function onListKeydown(event) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      move(1)
      break
    case 'ArrowUp':
      event.preventDefault()
      move(-1)
      break
    case 'Home':
      event.preventDefault()
      activeIndex.value = firstEnabled()
      break
    case 'End':
      event.preventDefault()
      activeIndex.value = items.value.findLastIndex((i) => !i.disabled)
      break
    case 'Enter':
    case ' ':
      event.preventDefault()
      pick(activeIndex.value)
      break
    case 'Escape':
      event.preventDefault()
      closeMenu({ refocus: true })
      break
    case 'Tab':
      closeMenu()
      break
    default:
      break
  }
}

function onDocumentPointerDown(event) {
  if (triggerEl.value?.contains(event.target) || listEl.value?.contains(event.target)) return
  closeMenu()
}

// Listeners exist only while the popup is open. `scroll` is captured because the
// popup is fixed-positioned and must follow a trigger inside a scrolled panel.
watch(open, (isOpen) => {
  if (isOpen) {
    document.addEventListener('pointerdown', onDocumentPointerDown, true)
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
  } else {
    document.removeEventListener('pointerdown', onDocumentPointerDown, true)
    window.removeEventListener('resize', place)
    window.removeEventListener('scroll', place, true)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  window.removeEventListener('resize', place)
  window.removeEventListener('scroll', place, true)
})
</script>

<template>
  <div class="relative">
    <button
      ref="triggerEl"
      type="button"
      class="field flex items-center justify-between gap-2 text-left"
      :class="[{ 'border-accent!': open }, size === 'sm' && 'h-9 text-body-sm']"
      :disabled="disabled"
      role="combobox"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="listboxId"
      :aria-label="ariaLabel || undefined"
      :aria-labelledby="labelledBy ? `${labelledBy} ${valueId}` : undefined"
      @click="open ? closeMenu() : openMenu()"
      @keydown="onTriggerKeydown"
    >
      <span :id="valueId" class="truncate" :class="{ 'text-txt-tertiary': !selectedLabel }">
        {{ selectedLabel || placeholder }}
      </span>
      <PhCaretDown
        :size="14"
        class="shrink-0 text-txt-tertiary transition-transform duration-[--dur-fast]"
        :class="{ 'rotate-180': open }"
      />
    </button>

    <Teleport to="body">
      <Transition name="fade">
        <ul
          v-if="open"
          :id="listboxId"
          ref="listEl"
          role="listbox"
          tabindex="-1"
          :style="popupStyle"
          class="glass-regular anim-pop z-[var(--z-popover)] overflow-y-auto rounded-[10px] p-1 shadow-[var(--sh-md)] outline-none"
          @keydown="onListKeydown"
          v-focus-on-mount
        >
          <li
            v-for="(item, index) in items"
            :key="`${item.value}`"
            role="option"
            :aria-selected="index === selectedIndex"
            :data-active="index === activeIndex"
            :class="[
              'flex cursor-pointer items-center justify-between gap-2 rounded-[6px] px-3 py-2 text-[13px]',
              item.disabled && 'pointer-events-none opacity-40',
              index === activeIndex ? 'bg-white/10 text-txt' : 'text-txt-secondary',
            ]"
            @click="pick(index)"
            @mousemove="activeIndex = index"
          >
            <span class="truncate">{{ item.label }}</span>
            <PhCheck v-if="index === selectedIndex" :size="14" class="shrink-0 text-accent" />
          </li>
          <li v-if="!items.length" class="px-3 py-2 text-[13px] text-txt-tertiary">—</li>
        </ul>
      </Transition>
    </Teleport>
  </div>
</template>

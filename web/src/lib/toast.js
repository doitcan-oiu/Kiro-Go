// Toast notifications. Reactive queue rendered by <ToastHost>.
//
// API mirrors the legacy `window.toast(...)`: every call returns a dismiss
// function, and `duration <= 0` makes the toast sticky — the pattern used for
// "processing…" messages that are closed when the work finishes.
import { ref } from 'vue'

export const toasts = ref([])

const MAX_STACK = 5
const DEFAULT_DURATION = 4000
const MIN_DURATION = 2000

let seq = 0
const timers = new Map()

export const VARIANTS = ['success', 'error', 'warning', 'info']

function normalizeVariant(v) {
  if (VARIANTS.includes(v)) return v
  // Legacy aliases kept so ported call sites behave the same.
  if (v === 'danger') return 'error'
  if (v === 'primary' || v === 'secondary' || v === 'ghost') return 'info'
  return 'info'
}

export function dismissToast(id) {
  const timer = timers.get(id)
  if (timer) {
    clearTimeout(timer)
    timers.delete(id)
  }
  toasts.value = toasts.value.filter((item) => item.id !== id)
}

export function dismissAllToasts() {
  for (const timer of timers.values()) clearTimeout(timer)
  timers.clear()
  toasts.value = []
}

function schedule(id, duration) {
  if (duration <= 0) return // sticky
  timers.set(
    id,
    setTimeout(() => dismissToast(id), Math.max(MIN_DURATION, duration)),
  )
}

/**
 * Show a toast.
 * @param {string} message plain text (never rendered as HTML)
 * @param {string} [variant] success | error | warning | info
 * @param {{duration?: number}} [opts] duration <= 0 keeps it open indefinitely
 * @returns {() => void} dismiss
 */
export function toast(message, variant = 'info', opts = {}) {
  const id = ++seq
  const duration = opts.duration === undefined ? DEFAULT_DURATION : opts.duration
  const entry = {
    id,
    message: String(message ?? ''),
    variant: normalizeVariant(variant),
    sticky: duration <= 0,
  }

  const next = [...toasts.value, entry]
  // Drop the oldest when the stack overflows so the newest is always visible.
  while (next.length > MAX_STACK) {
    const evicted = next.shift()
    const timer = timers.get(evicted.id)
    if (timer) {
      clearTimeout(timer)
      timers.delete(evicted.id)
    }
  }
  toasts.value = next

  schedule(id, duration)
  return () => dismissToast(id)
}

toast.success = (msg, opts) => toast(msg, 'success', opts)
toast.error = (msg, opts) => toast(msg, 'error', opts)
toast.warning = (msg, opts) => toast(msg, 'warning', opts)
toast.info = (msg, opts) => toast(msg, 'info', opts)

/** Pause auto-dismiss while the pointer rests on a toast. */
export function pauseToast(id) {
  const timer = timers.get(id)
  if (timer) {
    clearTimeout(timer)
    timers.delete(id)
  }
}

/** Resume with a short grace period after the pointer leaves. */
export function resumeToast(id) {
  const entry = toasts.value.find((item) => item.id === id)
  if (!entry || entry.sticky || timers.has(id)) return
  timers.set(
    id,
    setTimeout(() => dismissToast(id), 1500),
  )
}

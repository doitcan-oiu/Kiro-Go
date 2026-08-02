// Global confirm dialog.
//
// Replaces the legacy panel's mix of a custom modal and three stray
// `window.confirm` calls with one promise-based API, so every destructive
// action gets the same styled dialog:
//
//   if (await confirm({ message: t('accounts.confirmDelete') })) { … }
import { ref } from 'vue'

const defaults = {
  title: '',
  message: '',
  confirmKey: 'common.confirm',
  cancelKey: 'common.cancel',
  danger: false,
}

export const confirmState = ref({ ...defaults, open: false })

let resolveCurrent = null

/** Opens the dialog; resolves true on confirm, false on cancel/Esc/backdrop. */
export function confirm(options = {}) {
  // A second call while one is pending resolves the first as cancelled rather
  // than leaking a forever-pending promise.
  if (resolveCurrent) resolveCurrent(false)

  confirmState.value = { ...defaults, ...options, open: true }
  return new Promise((resolve) => {
    resolveCurrent = resolve
  })
}

export function settleConfirm(result) {
  confirmState.value = { ...confirmState.value, open: false }
  const resolve = resolveCurrent
  resolveCurrent = null
  if (resolve) resolve(result)
}

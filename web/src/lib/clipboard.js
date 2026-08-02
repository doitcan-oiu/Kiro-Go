// Clipboard + file download helpers.
import { t } from '@/lib/i18n'
import { toast } from '@/lib/toast'

/**
 * Copy text to the clipboard.
 *
 * `value` may be a string or a Promise<string>. The Promise form matters for
 * async sources (e.g. fetching an account's full credentials on click): browsers
 * require the clipboard write to happen inside the user gesture, so where
 * `ClipboardItem` is supported we hand the pending Promise straight to
 * `navigator.clipboard.write` instead of awaiting first, which would lose the
 * gesture and get the write rejected.
 */
export async function copyText(value) {
  const isPromise = value && typeof value.then === 'function'

  if (isPromise && typeof window.ClipboardItem === 'function' && navigator.clipboard?.write) {
    try {
      const blob = value.then((text) => new Blob([String(text ?? '')], { type: 'text/plain' }))
      await navigator.clipboard.write([new window.ClipboardItem({ 'text/plain': blob })])
      return true
    } catch {
      // Fall through to the await + writeText path below.
    }
  }

  let text
  try {
    text = String((isPromise ? await value : value) ?? '')
  } catch {
    return false
  }
  if (!text) return false

  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Insecure context or a denied permission — try the legacy path.
  }
  return legacyCopy(text)
}

/**
 * execCommand fallback for non-HTTPS origins, where navigator.clipboard is
 * unavailable. Self-hosted panels are frequently served over plain HTTP on a
 * LAN address, so this path is load-bearing rather than vestigial.
 */
function legacyCopy(text) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.cssText = 'position:fixed;top:-1000px;left:-1000px;opacity:0'
  document.body.appendChild(ta)
  try {
    ta.select()
    ta.setSelectionRange(0, ta.value.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(ta)
  }
}

/** Copy + toast in one call; the common case at every call site. */
export async function copyWithToast(value, successKey = 'common.copied') {
  const ok = await copyText(value)
  if (ok) toast(t(successKey), 'success')
  else toast(t('common.failed'), 'error')
  return ok
}

/** Trigger a client-side file download of `content`. */
export function downloadFile(filename, content, type = 'application/json') {
  const blob = new Blob([content], { type: `${type};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // Revoking synchronously can cancel the download in Safari.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** Download `data` as pretty-printed JSON. */
export function downloadJson(filename, data) {
  downloadFile(filename, JSON.stringify(data, null, 2))
}

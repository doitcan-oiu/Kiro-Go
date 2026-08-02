// Theme preference: system → light → dark, persisted in localStorage.
//
// The token layer (styles/tokens.css) keys off `.dark` on <html>, plus a
// `data-theme-pref` attribute so the toggle button can show which of the three
// states is active (not merely which one is resolved).
import { ref } from 'vue'

const STORAGE_KEY = 'kiro_theme'
export const THEME_ORDER = ['system', 'light', 'dark']

export const themePref = ref(readPref())
export const resolvedTheme = ref('dark')

function readPref() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved && THEME_ORDER.includes(saved)) return saved
  } catch {
    /* private mode */
  }
  return 'system'
}

function prefersDark() {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? true
}

/** Resolves a preference to a concrete theme. */
export function resolveTheme(pref) {
  if (pref === 'dark' || pref === 'light') return pref
  return prefersDark() ? 'dark' : 'light'
}

function apply(pref) {
  const resolved = resolveTheme(pref)
  resolvedTheme.value = resolved
  const root = document.documentElement
  root.classList.toggle('dark', resolved === 'dark')
  root.dataset.themePref = pref
  root.style.colorScheme = resolved
}

export function setTheme(pref) {
  if (!THEME_ORDER.includes(pref)) return
  themePref.value = pref
  try {
    localStorage.setItem(STORAGE_KEY, pref)
  } catch {
    /* ignore */
  }
  apply(pref)
}

/** Cycles system → light → dark → system. */
export function cycleTheme() {
  const next = THEME_ORDER[(THEME_ORDER.indexOf(themePref.value) + 1) % THEME_ORDER.length]
  setTheme(next)
}

export function initTheme() {
  apply(themePref.value)
  // Track OS changes, but only re-apply while following the system.
  window
    .matchMedia?.('(prefers-color-scheme: dark)')
    .addEventListener('change', () => {
      if (themePref.value === 'system') apply('system')
    })
}

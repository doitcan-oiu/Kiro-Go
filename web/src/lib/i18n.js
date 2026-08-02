// Flat-key i18n, ported from the legacy panel so the existing 643-key
// zh/en dictionaries keep working verbatim.
//
// Two deliberate differences from the old implementation:
//   1. Dictionaries are bundled (static imports) instead of fetched at runtime,
//      so there is no flash of untranslated keys on first paint.
//   2. Placeholder substitution replaces *every* occurrence of {0}/{1}/… and also
//      supports named tokens ({count}); the old version used String.replace with
//      a string pattern and silently substituted only the first occurrence.
import { computed, ref } from 'vue'
import en from '@/locales/en.json'
import zh from '@/locales/zh.json'

const DICTS = { zh, en }
export const LANGS = ['zh', 'en']
const STORAGE_KEY = 'kiro_lang'

function initialLang() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved && LANGS.includes(saved)) return saved
  } catch {
    /* private mode */
  }
  const nav = (navigator.language || 'zh').toLowerCase()
  return nav.startsWith('zh') ? 'zh' : 'en'
}

export const lang = ref(initialLang())

/** Escapes a token so it can be embedded in a RegExp safely. */
function escapeRe(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * Translate `key`, substituting placeholders.
 *
 * Positional: t('batch.refreshResult', 3, 1) → "{0}"→3, "{1}"→1
 * Named:      t('api.totalModels', { count: 42 }) → "{count}"→42
 */
export function t(key, ...args) {
  const active = DICTS[lang.value] || {}
  let text = active[key] ?? DICTS.zh[key] ?? key
  if (!args.length) return text
  const [first] = args
  if (args.length === 1 && first && typeof first === 'object' && !Array.isArray(first)) {
    for (const [name, value] of Object.entries(first)) {
      text = text.replace(new RegExp(`\\{${escapeRe(name)}\\}`, 'g'), String(value))
    }
    return text
  }
  args.forEach((value, idx) => {
    text = text.replace(new RegExp(`\\{${idx}\\}`, 'g'), String(value))
  })
  return text
}

/** True when `key` exists in either dictionary — used for optional labels. */
export function hasKey(key) {
  return Boolean(DICTS[lang.value]?.[key] ?? DICTS.zh[key])
}

export function setLang(next) {
  if (!LANGS.includes(next) || next === lang.value) return
  lang.value = next
  try {
    localStorage.setItem(STORAGE_KEY, next)
  } catch {
    /* ignore */
  }
  applyDocumentLang()
}

export function applyDocumentLang() {
  document.documentElement.lang = lang.value === 'zh' ? 'zh-CN' : 'en'
  document.title = t('app.title')
}

/**
 * Composable for templates. `t` is wrapped so it re-evaluates when `lang`
 * changes: reading `lang.value` inside the wrapper registers the dependency.
 */
export function useI18n() {
  const translate = (key, ...args) => {
    void lang.value
    return t(key, ...args)
  }
  return {
    t: translate,
    lang,
    setLang,
    isZh: computed(() => lang.value === 'zh'),
  }
}

// Verifies that every i18n key referenced in src/ exists in both dictionaries.
//
// The old panel resolved keys at runtime and silently rendered the raw key on a
// typo. Since the dictionaries are flat and keys are plain strings, a static
// scan catches those typos at build time instead.
//
//   node scripts/check-i18n.mjs
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const srcDir = join(root, 'src')

const zh = JSON.parse(readFileSync(join(srcDir, 'locales/zh.json'), 'utf8'))
const en = JSON.parse(readFileSync(join(srcDir, 'locales/en.json'), 'utf8'))

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) out.push(...walk(full))
    else if (/\.(vue|js)$/.test(full)) out.push(full)
  }
  return out
}

// Matches t('some.key') / t("some.key") — the only form used in this codebase.
// Dynamic keys (t(variable), t('a.' + b)) are intentionally not matched; those
// are asserted separately where they occur.
const CALL_RE = /\bt\(\s*'([A-Za-z][\w.]*)'|\bt\(\s*"([A-Za-z][\w.]*)"/g

const refs = new Map()
for (const file of walk(srcDir)) {
  if (file.includes('/locales/')) continue
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(CALL_RE)) {
    const key = m[1] || m[2]
    if (!refs.has(key)) refs.set(key, relative(root, file))
  }
}

const missing = []
for (const [key, file] of refs) {
  const inZh = Object.hasOwn(zh, key)
  const inEn = Object.hasOwn(en, key)
  if (!inZh || !inEn) missing.push({ key, file, inZh, inEn })
}

// Keys present in one dictionary but not the other would fall back silently.
const onlyZh = Object.keys(zh).filter((k) => !Object.hasOwn(en, k))
const onlyEn = Object.keys(en).filter((k) => !Object.hasOwn(zh, k))

console.log(`dictionaries: zh=${Object.keys(zh).length} en=${Object.keys(en).length}`)
console.log(`static key references: ${refs.size}`)

let failed = false

if (missing.length) {
  failed = true
  console.error(`\n${missing.length} referenced key(s) missing:`)
  for (const m of missing) {
    console.error(`  ${m.key}  zh=${m.inZh ? 'ok' : 'MISS'} en=${m.inEn ? 'ok' : 'MISS'}  (${m.file})`)
  }
}

if (onlyZh.length || onlyEn.length) {
  failed = true
  if (onlyZh.length) console.error(`\n${onlyZh.length} key(s) only in zh: ${onlyZh.join(', ')}`)
  if (onlyEn.length) console.error(`\n${onlyEn.length} key(s) only in en: ${onlyEn.join(', ')}`)
}

if (failed) process.exit(1)
console.log('i18n OK: all referenced keys present in both dictionaries')

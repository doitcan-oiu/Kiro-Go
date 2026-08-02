// Verifies that every `@/...` import in src/ resolves to a real file.
// Vite reports only the first unresolved import per build, so checking them all
// up front is faster than iterating on build failures.
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const srcDir = join(root, 'src')

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) out.push(...walk(full))
    else if (/\.(vue|js)$/.test(entry)) out.push(full)
  }
  return out
}

const EXTS = ['', '.js', '.vue', '.json', '/index.js']
const files = walk(srcDir)
const missing = []

for (const file of files) {
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(/from\s+['"](@\/[^'"]+)['"]/g)) {
    const spec = m[1]
    const base = join(srcDir, spec.slice(2))
    if (!EXTS.some((ext) => existsSync(base + ext))) {
      missing.push({ spec, file: file.replace(root + '/', '') })
    }
  }
}

console.log(`files scanned: ${files.length}`)
if (missing.length) {
  console.error(`\n${missing.length} unresolved import(s):`)
  for (const m of missing) console.error(`  ${m.spec}  <- ${m.file}`)
  process.exit(1)
}
console.log('imports OK: every @/ specifier resolves')

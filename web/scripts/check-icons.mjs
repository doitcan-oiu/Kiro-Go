// Verifies every Ph* icon imported from @phosphor-icons/vue actually exists.
// Rolldown only reports the first missing export per build, so checking them
// all up front is much faster than fixing them one build at a time.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import * as icons from '@phosphor-icons/vue'

const available = new Set(Object.keys(icons))

function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (/\.(vue|js)$/.test(entry)) out.push(full)
  }
  return out
}

const missing = []
const used = new Set()

for (const file of walk('src')) {
  const src = readFileSync(file, 'utf8')
  // Match the whole import block from the icon package, then pull identifiers.
  const blocks = src.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]@phosphor-icons\/vue['"]/g)
  for (const [, names] of blocks) {
    for (const raw of names.split(',')) {
      const name = raw.trim().split(/\s+as\s+/)[0].trim()
      if (!name) continue
      used.add(name)
      if (!available.has(name)) missing.push({ name, file })
    }
  }
}

console.log(`icons available: ${available.size}`)
console.log(`icons imported: ${used.size}`)
if (missing.length) {
  console.log(`\n${missing.length} missing icon(s):`)
  for (const m of missing) console.log(`  ${m.name}  <- ${m.file}`)
  process.exit(1)
}
console.log('all imported icons exist')

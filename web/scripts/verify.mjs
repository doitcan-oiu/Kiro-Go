// Runs every static check in sequence and exits non-zero if any fail.
//
// These checks exist because Vue SFC templates are not type-checked: a missing
// i18n key, a non-existent Phosphor icon or a typo'd import path all compile
// fine and only surface as a blank spot (or a hard crash) at runtime.
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

const checks = [
  'check-imports.mjs',
  'check-icons.mjs',
  'check-i18n.mjs',
  'check-tokens.mjs',
]

let failed = 0
for (const script of checks) {
  const res = spawnSync(process.execPath, [join(here, script)], { stdio: 'inherit' })
  if (res.status !== 0) failed++
}

if (failed) {
  console.error(`\n${failed} check(s) failed.`)
  process.exit(1)
}
console.log('\nAll static checks passed.')

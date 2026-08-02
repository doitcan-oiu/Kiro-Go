// Verifies the CSS custom-property contract.
//
// Two failure modes this catches, both of which bit during the glass rework:
//   1. A utility references a token that was renamed (e.g. --glass-clear-blur
//      after it became --glass-clear-filter). CSS fails silently: the property
//      resolves to nothing and the effect just disappears.
//   2. A token exists in :root (dark) but is never overridden in .light, so the
//      light theme silently inherits a dark-mode value.
//
// Tokens that are deliberately scoped inside a utility (set locally with a
// var(..., fallback)) are allowed via LOCAL_TOKENS.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const SRC = new URL('../src/', import.meta.url).pathname

// Set per-utility rather than globally; each read site supplies a fallback.
const LOCAL_TOKENS = new Set([
  '--glass-sheen-strength',
  '--glass-sheen-opacity',
  '--glass-sheen-angle',
])

// Tokens intentionally identical across themes (geometry, timing, stacking).
const THEME_AGNOSTIC = /^--(space|radius|dur|ease|z|sidebar|topbar|font|text|breakpoint)/

const files = ['styles/tokens.css', 'styles/index.css']
const sources = new Map()
for (const rel of files) {
  sources.set(rel, readFileSync(join(SRC, rel), 'utf8'))
}

const tokensCss = sources.get('styles/tokens.css')

/** Extracts the body of a top-level block whose selector matches `selector`. */
function blockBody(css, selector) {
  const start = css.indexOf(selector)
  if (start === -1) return ''
  const open = css.indexOf('{', start)
  if (open === -1) return ''
  let depth = 0
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    else if (css[i] === '}') {
      depth -= 1
      if (depth === 0) return css.slice(open + 1, i)
    }
  }
  return ''
}

function declaredIn(css) {
  const found = new Set()
  for (const m of css.matchAll(/(--[a-z0-9-]+)\s*:/gi)) found.add(m[1])
  return found
}

const rootTokens = declaredIn(blockBody(tokensCss, ':root'))
const lightTokens = declaredIn(blockBody(tokensCss, '.light'))

// Every declaration anywhere counts as "defined" for the resolution check,
// since a utility may legitimately define its own scoped token.
const allDeclared = new Set()
for (const css of sources.values()) for (const t of declaredIn(css)) allDeclared.add(t)

// Collect var() reads with their originating file, across CSS and components.
const { globSync } = await import('node:fs')
const componentFiles = globSync('**/*.vue', { cwd: SRC }).map((f) => [f, readFileSync(join(SRC, f), 'utf8')])
const scanned = [...sources.entries(), ...componentFiles]

const reads = new Map() // token -> Set(file)
for (const [rel, css] of scanned) {
  for (const m of css.matchAll(/var\(\s*(--[a-z0-9-]+)/gi)) {
    if (!reads.has(m[1])) reads.set(m[1], new Set())
    reads.get(m[1]).add(rel)
  }
}

const problems = []

for (const [token, where] of reads) {
  if (LOCAL_TOKENS.has(token)) continue
  if (!allDeclared.has(token)) {
    problems.push(`unresolved  ${token}  read by ${[...where].join(', ')}`)
  }
}

for (const token of rootTokens) {
  if (LOCAL_TOKENS.has(token) || THEME_AGNOSTIC.test(token)) continue
  if (!lightTokens.has(token)) {
    problems.push(`no light override  ${token}`)
  }
}

console.log(`:root tokens: ${rootTokens.size}   .light overrides: ${lightTokens.size}`)
console.log(`var() reads: ${reads.size}`)

if (problems.length) {
  console.error(`\n${problems.length} token problem(s):`)
  for (const p of problems.sort()) console.error(`  ${p}`)
  process.exit(1)
}
console.log('tokens OK: every var() resolves; every themed token has a light override')

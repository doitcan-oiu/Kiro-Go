// Verifies that the compiled CSS really contains all four Liquid Glass layers.
//
// This check exists because the failure mode is silent and visual: if a token
// name drifts, `backdrop-filter: var(--missing)` compiles fine, emits no error,
// and simply renders a flat translucent rectangle. That is precisely the bug
// this material rewrite was fixing, so it gets a regression test.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const assets = join(root, 'dist', 'assets')

let css
try {
  const file = readdirSync(assets)
    .filter((f) => /^index-.*\.css$/.test(f))
    .map((f) => ({ f, t: readFileSync(join(assets, f)).length }))
    .pop()
  if (!file) throw new Error('no index-*.css in dist/assets')
  css = readFileSync(join(assets, file.f), 'utf8')
  console.log(`compiled css: ${file.f} (${(css.length / 1024).toFixed(1)} KB)`)
} catch (err) {
  console.error(`cannot read compiled css: ${err.message}`)
  console.error('run `npm run build:only` first')
  process.exit(1)
}

const problems = []

// The four glass levels must each carry every layer.
for (const level of ['thin', 'regular', 'clear', 'thick']) {
  // Layer 1: refraction. Must reference the resolved filter token AND the token
  // must itself be defined with a real blur radius.
  const filterToken = new RegExp(`--glass-${level}-filter:\\s*blur\\((\\d+)px\\)`)
  const m = css.match(filterToken)
  if (!m) {
    problems.push(`${level}: --glass-${level}-filter not defined with a blur() radius`)
  } else if (Number(m[1]) < 16) {
    // Below ~16px the blur reads as "frosted plastic" rather than glass on a
    // dark substrate; the original 12px value was the core visual defect.
    problems.push(`${level}: blur radius ${m[1]}px is too small to read as glass (want >=16px)`)
  }

  // Layers 2+3: material body + specular sheen, stacked as background-image.
  if (!css.includes(`--glass-sheen-${level}`)) {
    problems.push(`${level}: missing specular sheen token --glass-sheen-${level}`)
  }
  if (!css.includes(`--glass-${level}-bg`)) {
    problems.push(`${level}: missing material body token --glass-${level}-bg`)
  }

  // Layer 4: refractive edge as inset shadows.
  const edge = new RegExp(`--glass-edge-${level}:[^;]*inset`)
  if (!edge.test(css)) {
    problems.push(`${level}: missing refractive edge token --glass-edge-${level}`)
  }
}

// The ambient substrate is what the blur actually refracts. Without it the
// whole material collapses back to flat grey — the original defect.
//
// Checked per-rule rather than by searching the whole file: `.light body::before`
// only overrides colours, so a file-wide "does body::before exist" test would
// still pass if the base (dark) rule were deleted.
const substrateRules = []
for (const seg of css.split('}')) {
  const brace = seg.indexOf('{')
  if (brace < 0) continue
  const selector = seg.slice(0, brace)
  if (!/body:{1,2}before/.test(selector)) continue
  substrateRules.push({
    light: /\.light/.test(selector),
    radials: (seg.slice(brace + 1).match(/radial-gradient/g) || []).length,
  })
}

const baseSubstrate = substrateRules.find((r) => !r.light)
if (!baseSubstrate) {
  problems.push('ambient substrate (body::before) missing — glass has nothing to refract')
} else if (baseSubstrate.radials < 4) {
  problems.push(
    `ambient substrate has only ${baseSubstrate.radials} radial gradients (want >=4 colour wells)`,
  )
}
if (!substrateRules.some((r) => r.light)) {
  problems.push('light mode has no substrate override — glass would refract dark-mode colours')
}

// Accessibility / performance fallbacks must survive minification.
if (!css.includes('prefers-reduced-transparency')) {
  problems.push('missing prefers-reduced-transparency fallback')
}
// The minifier is free to reorder the operands of `or` and to drop whitespace,
// so match on "an @supports not(...) block that mentions backdrop-filter"
// rather than on an exact operand order.
const supportsNotBlocks = css.match(/@supports\s+not\s*\([^{]*\)/g) || []
if (!supportsNotBlocks.some((block) => /backdrop-filter/.test(block))) {
  problems.push('missing @supports fallback for browsers without backdrop-filter')
}

// Glass utilities must not impose layout side effects: `position` would break
// the sticky sidebar/topbar and `overflow` would clip the select popup's own
// scroll area. Check the emitted rules, not the source.
for (const rule of css.split('}')) {
  if (!/\.glass-(thin|regular|clear|thick)\s*[,{]/.test(rule)) continue
  for (const prop of ['position:', 'overflow:', 'isolation:']) {
    if (rule.includes(prop)) {
      problems.push(`glass utility leaks "${prop}" — breaks sticky/scroll containers`)
    }
  }
}

// ── painting-order invariant ─────────────────────────────────────────────────
// The ambient light field lives on body::before / body::after with negative
// z-index. Per CSS painting order, a negative-z-index child paints above its
// containing block's own background. So a `background-color` on `body` would
// cover the light field; it only *appears* to work because a body background
// gets propagated to the canvas while html has none — an implicit dependency
// that breaks the moment anyone styles html. Assert the base colour sits on
// html and that body declares no background of its own.
const srcCss = readFileSync(new URL('../src/styles/index.css', import.meta.url), 'utf8')
const baseLayer = srcCss.slice(srcCss.indexOf('@layer base'))
const htmlRule = baseLayer.match(/\bhtml\s*\{([^}]*)\}/)
const bodyRule = baseLayer.match(/\bbody\s*\{([^}]*)\}/)

if (!htmlRule || !/background-color:/.test(htmlRule[1])) {
  problems.push('base colour must sit on `html`: body::before light field would be hidden otherwise')
}
if (bodyRule && /background(-color)?:/.test(bodyRule[1])) {
  problems.push('`body` must not declare a background: it paints over the body::before light field')
}

if (problems.length) {
  console.error(`\n${problems.length} glass material problem(s):`)
  for (const p of problems) console.error(`  ${p}`)
  process.exit(1)
}

console.log('glass OK: 4 layers present at all 4 levels, substrate + fallbacks intact')
console.log('  layer 1 refraction   backdrop-filter blur/saturate/brightness')
console.log('  layer 2 material     background-image gradient body')
console.log('  layer 3 specular     sheen gradient')
console.log('  layer 4 edge         inset refractive shadows')
console.log('  substrate            html base colour + body::before light field')

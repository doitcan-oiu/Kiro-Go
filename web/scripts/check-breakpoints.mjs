// 断点单位一致性检查。
//
// 这个检查存在的原因是一次真实的、静默的回归：自定义断点用 px 声明
// （--breakpoint-laptop: 1024px），而 Tailwind 默认断点是 rem。Tailwind 按数值
// 排序 media query 以保证「大断点覆盖小断点」，但它无法跨单位比较，于是把所有
// px 断点整体排在 rem 断点之前，产出：
//
//   @media (width>=1024px) { .laptop\:grid-cols-3 }
//   @media (width>=1440px) { .desktop\:grid-cols-4 }
//   @media (width>=40rem)  { .sm\:grid-cols-2 }    ← 排在最后，宽屏上胜出
//
// 结果是「一行 4 个」在宽屏上被 sm: 的 2 列覆盖。CSS 不报错、构建通过、
// 只有在特定宽度下肉眼才能发现，因此必须由断言守住。
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const problems = []

// ── 1. 源码层：所有 --breakpoint-* 必须同单位 ──────────────────────────────
const src = readFileSync(join(root, 'src/styles/index.css'), 'utf8')
const declared = [...src.matchAll(/--breakpoint-([\w-]+)\s*:\s*([\d.]+)(px|rem|em)/g)].map(
  (m) => ({ name: m[1], value: Number(m[2]), unit: m[3] }),
)

if (declared.length) {
  const units = new Set(declared.map((b) => b.unit))
  if (units.size > 1) {
    problems.push(
      `自定义断点混用了单位 ${[...units].join(' / ')}：` +
        declared.map((b) => `${b.name}=${b.value}${b.unit}`).join(', ') +
        '\n    Tailwind 无法跨单位排序 media query，会把 px 断点整体排在 rem 之前，' +
        '导致小断点反而覆盖大断点。统一为 rem。',
    )
  }
  // Tailwind 默认断点是 rem，所以自定义断点也必须是 rem
  for (const b of declared) {
    if (b.unit !== 'rem') {
      problems.push(`--breakpoint-${b.name} 使用 ${b.unit}，必须用 rem（与 Tailwind 默认断点一致）`)
    }
  }
}

// ── 2. 产物层：min-width media query 必须按升序排列 ────────────────────────
// 这是真正要守的不变量。即使单位统一，若排序错乱依然会出现覆盖倒置。
let css = ''
try {
  const file = readdirSync(join(root, 'dist/assets'))
    .filter((f) => /^index-.*\.css$/.test(f))
    .pop()
  if (file) css = readFileSync(join(root, 'dist/assets', file), 'utf8')
} catch {
  /* 产物不存在时跳过第 2 项，由 build 流程保证先构建 */
}

if (css) {
  const ROOT_FONT = 16
  const seen = []
  for (const m of css.matchAll(/@media\s*\(width>=([\d.]+)(rem|px)\)/g)) {
    const px = m[2] === 'rem' ? Number(m[1]) * ROOT_FONT : Number(m[1])
    seen.push({ raw: `${m[1]}${m[2]}`, px, index: m.index })
  }

  // Tailwind 会分多段输出（utilities / variants），因此只要求「不出现大断点在前、
  // 小断点在后」的倒置对，而不要求全局单调。
  for (let i = 0; i < seen.length; i++) {
    for (let j = i + 1; j < seen.length; j++) {
      if (seen[j].px < seen[i].px) {
        // 允许分段重启：若两者之间距离很远（不同输出段），跳过。
        const between = css.slice(seen[i].index, seen[j].index)
        const restarted = between.includes('@media (hover:hover)')
        if (restarted) continue
        problems.push(
          `产物中 media query 顺序倒置：${seen[i].raw} 出现在 ${seen[j].raw} 之前。` +
            `\n    同等优先级下后者胜出，小断点会覆盖大断点。`,
        )
        i = seen.length // 报一次即可，避免噪声
        break
      }
    }
  }
}

if (problems.length) {
  console.error(`\n${problems.length} 个断点问题：`)
  for (const p of problems) console.error(`  ${p}`)
  process.exit(1)
}

console.log(
  `breakpoints OK: ${declared.length} 个自定义断点单位一致（rem）` +
    (css ? '，产物中 media query 顺序正确' : ''),
)

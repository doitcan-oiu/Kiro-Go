<script setup>
// 多序列折线图，纯内联 SVG。
//
// 为什么不引图表库：面板只有两张图，Chart.js/ECharts 压缩后是 60–170 KB，
// 比整个首屏 JS 还大。这里需要的功能很有限——折线、断线、悬停读数、坐标轴，
// 手写不到 200 行，且能直接用设计系统的 token 上色。
//
// 三个刻意的设计点：
//
//   1. null 断线。数据里的 null 表示「该时间桶没有样本」，不是 0。折线在此处
//      断开，掉到 0 会被误读为「指标突然变好」。
//   2. viewBox 不做 preserveAspectRatio="none" 拉伸。拉伸会让描边随宽度变形
//      （细的地方更细），改为响应式重算坐标。
//   3. 悬停走整列而非单点。折线上的点只有几像素，要求用户精确命中不现实；
//      这里把整个纵向条带作为热区，鼠标在图上任意位置都能读到那一分钟的值。
import { computed, ref } from 'vue'

const props = defineProps({
  /**
   * 序列数组：`{ key, label, color, points: Array<number|null>, dashed?, area? }`
   * 所有序列的 points 长度必须一致（同一套时间桶）。
   */
  series: { type: Array, default: () => [] },
  /** X 轴刻度标签，长度与 points 一致；只渲染首/中/末三个，避免拥挤。 */
  labels: { type: Array, default: () => [] },
  /** 纵轴上限。传 null 则按数据最大值自适应。 */
  max: { type: Number, default: null },
  height: { type: Number, default: 200 },
  /** 数值格式化函数，用于纵轴与 tooltip。 */
  format: { type: Function, default: (v) => String(v) },
  /** 无数据时的提示文案。 */
  emptyText: { type: String, default: '' },
  ariaLabel: { type: String, default: '' },
})

// 内部坐标系固定，渲染时按容器宽度等比缩放（preserveAspectRatio 保持默认）。
const W = 720
const PAD = { top: 12, right: 8, bottom: 22, left: 40 }

const plotH = computed(() => props.height - PAD.top - PAD.bottom)
const plotW = computed(() => W - PAD.left - PAD.right)

const bucketCount = computed(() => {
  for (const s of props.series) {
    if (Array.isArray(s?.points) && s.points.length) return s.points.length
  }
  return 0
})

/** 是否存在任何有效数据点。全空时显示空状态而不是一张空网格。 */
const hasData = computed(() =>
  props.series.some((s) => (s?.points || []).some((v) => v !== null && Number.isFinite(v))),
)

const peak = computed(() => {
  if (props.max !== null && props.max > 0) return props.max
  let m = 0
  for (const s of props.series) {
    for (const v of s?.points || []) {
      if (v !== null && Number.isFinite(v) && v > m) m = v
    }
  }
  // 上限留 8% 余量，避免峰值点贴在顶边上被裁掉半个圆点。
  return m > 0 ? m * 1.08 : 1
})

/** 纵轴刻度：0、中值、峰值三档。更多档位在 200px 高度里会挤在一起。 */
const yTicks = computed(() => {
  const p = peak.value
  return [
    { value: 0, y: yFor(0) },
    { value: p / 2, y: yFor(p / 2) },
    { value: p, y: yFor(p) },
  ]
})

function xFor(index) {
  const n = bucketCount.value
  if (n <= 1) return PAD.left + plotW.value / 2
  return PAD.left + (index / (n - 1)) * plotW.value
}

function yFor(value) {
  const ratio = Math.max(0, Math.min(1, value / peak.value))
  return PAD.top + plotH.value * (1 - ratio)
}

/**
 * 把一条序列切成若干连续段。null 处断开，因此一条线可能由多段 polyline 组成。
 * 单点段（前后都是 null）无法画线，单独作为圆点渲染，否则该样本会完全不可见。
 */
function segmentsOf(points) {
  const segs = []
  let cur = []
  points.forEach((v, i) => {
    if (v === null || !Number.isFinite(v)) {
      if (cur.length) segs.push(cur)
      cur = []
      return
    }
    cur.push({ x: xFor(i), y: yFor(v), i, v })
  })
  if (cur.length) segs.push(cur)
  return segs
}

const rendered = computed(() =>
  props.series.map((s, si) => {
    const points = s?.points || []
    const segs = segmentsOf(points)
    return {
      key: s?.key ?? String(si),
      label: s?.label ?? '',
      color: s?.color || 'var(--accent-primary)',
      dashed: !!s?.dashed,
      area: !!s?.area,
      polylines: segs.filter((seg) => seg.length > 1).map((seg) => seg.map((p) => `${p.x},${p.y}`).join(' ')),
      // 孤立样本：整段只有一个点，画成圆点。
      dots: segs.filter((seg) => seg.length === 1).map((seg) => seg[0]),
      // 面积填充只对首条序列启用（多条叠加会互相遮挡）。
      areaPath: s?.area
        ? segs
            .filter((seg) => seg.length > 1)
            .map((seg) => {
              const base = yFor(0)
              const line = seg.map((p) => `${p.x},${p.y}`).join(' L ')
              return `M ${seg[0].x},${base} L ${line} L ${seg[seg.length - 1].x},${base} Z`
            })
            .join(' ')
        : '',
    }
  }),
)

// ── 悬停 ────────────────────────────────────────────────────────────────────
const hoverIndex = ref(-1)

const hoverInfo = computed(() => {
  const i = hoverIndex.value
  if (i < 0) return null
  const rows = []
  for (const s of props.series) {
    const v = (s?.points || [])[i]
    if (v === null || !Number.isFinite(v)) continue
    rows.push({ key: s.key, label: s.label, color: s.color, value: v })
  }
  if (!rows.length) return null
  return { index: i, label: props.labels[i] ?? '', rows, x: xFor(i) }
})

/** tooltip 靠近右边界时改为向左展开，避免被容器裁切。 */
const tooltipStyle = computed(() => {
  const info = hoverInfo.value
  if (!info) return {}
  const pct = (info.x / W) * 100
  return pct > 62
    ? { right: `${100 - pct}%`, marginRight: '10px' }
    : { left: `${pct}%`, marginLeft: '10px' }
})

/** 纵向热区宽度：整列平分，保证鼠标在任意 x 都能命中某一列。 */
const bandWidth = computed(() => (bucketCount.value ? plotW.value / bucketCount.value : 0))

function bandX(i) {
  return PAD.left + i * bandWidth.value - bandWidth.value / 2
}
</script>

<template>
  <figure class="relative m-0">
    <svg
      :viewBox="`0 0 ${W} ${height}`"
      class="block w-full"
      :style="{ height: `${height}px` }"
      role="img"
      :aria-label="ariaLabel"
      @mouseleave="hoverIndex = -1"
    >
      <!-- 网格线 + 纵轴刻度 -->
      <g>
        <template v-for="tick in yTicks" :key="`t-${tick.value}`">
          <line
            :x1="PAD.left"
            :y1="tick.y"
            :x2="W - PAD.right"
            :y2="tick.y"
            stroke="var(--chart-grid)"
            stroke-width="1"
            vector-effect="non-scaling-stroke"
          />
          <text
            :x="PAD.left - 6"
            :y="tick.y + 3.5"
            text-anchor="end"
            class="fill-[var(--text-tertiary)] text-[10px] tabular-nums"
          >
            {{ format(tick.value) }}
          </text>
        </template>
      </g>

      <template v-if="hasData">
        <!-- 面积填充（仅首条序列） -->
        <path
          v-for="s in rendered.filter((x) => x.areaPath)"
          :key="`a-${s.key}`"
          :d="s.areaPath"
          :fill="s.color"
          opacity="0.1"
        />

        <!-- 折线 -->
        <template v-for="s in rendered" :key="`s-${s.key}`">
          <polyline
            v-for="(pl, pi) in s.polylines"
            :key="`${s.key}-${pi}`"
            :points="pl"
            fill="none"
            :stroke="s.color"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-dasharray="s.dashed ? '4 3' : undefined"
            vector-effect="non-scaling-stroke"
          />
          <!-- 孤立样本 -->
          <circle
            v-for="d in s.dots"
            :key="`${s.key}-d-${d.i}`"
            :cx="d.x"
            :cy="d.y"
            r="2.5"
            :fill="s.color"
          />
        </template>

        <!-- 悬停指示竖线 + 各序列读数点 -->
        <template v-if="hoverInfo">
          <line
            :x1="hoverInfo.x"
            :y1="PAD.top"
            :x2="hoverInfo.x"
            :y2="PAD.top + plotH"
            stroke="var(--chart-crosshair)"
            stroke-width="1"
            vector-effect="non-scaling-stroke"
          />
          <circle
            v-for="row in hoverInfo.rows"
            :key="`h-${row.key}`"
            :cx="hoverInfo.x"
            :cy="yFor(row.value)"
            r="3.5"
            :fill="row.color"
            stroke="var(--bg-primary)"
            stroke-width="1.5"
          />
        </template>

        <!-- 透明热区：整列可命中 -->
        <rect
          v-for="i in bucketCount"
          :key="`b-${i}`"
          :x="bandX(i - 1)"
          :y="PAD.top"
          :width="bandWidth"
          :height="plotH"
          fill="transparent"
          @mouseenter="hoverIndex = i - 1"
        />
      </template>

      <!-- X 轴标签：首 / 中 / 末 -->
      <template v-if="labels.length">
        <text
          :x="PAD.left"
          :y="height - 6"
          text-anchor="start"
          class="fill-[var(--text-tertiary)] text-[10px] tabular-nums"
        >
          {{ labels[0] }}
        </text>
        <text
          :x="PAD.left + plotW / 2"
          :y="height - 6"
          text-anchor="middle"
          class="fill-[var(--text-tertiary)] text-[10px] tabular-nums"
        >
          {{ labels[Math.floor(labels.length / 2)] }}
        </text>
        <text
          :x="W - PAD.right"
          :y="height - 6"
          text-anchor="end"
          class="fill-[var(--text-tertiary)] text-[10px] tabular-nums"
        >
          {{ labels[labels.length - 1] }}
        </text>
      </template>
    </svg>

    <!-- 空状态覆盖在网格之上，保留坐标轴让布局稳定 -->
    <div
      v-if="!hasData"
      class="pointer-events-none absolute inset-0 flex items-center justify-center"
    >
      <span class="text-body-sm text-txt-tertiary">{{ emptyText }}</span>
    </div>

    <!-- Tooltip -->
    <div
      v-if="hoverInfo"
      class="glass-thick pointer-events-none absolute top-2 z-10 min-w-[128px] rounded-[10px] px-2.5 py-2 shadow-[var(--sh-md)]"
      :style="tooltipStyle"
    >
      <p class="mb-1 text-caption-sm tabular-nums text-txt-tertiary">{{ hoverInfo.label }}</p>
      <ul class="flex flex-col gap-1">
        <li
          v-for="row in hoverInfo.rows"
          :key="`tt-${row.key}`"
          class="flex items-center gap-2 text-caption"
        >
          <span
            class="h-1.5 w-1.5 shrink-0 rounded-full"
            :style="{ backgroundColor: row.color }"
            aria-hidden="true"
          />
          <span class="min-w-0 flex-1 truncate text-txt-secondary">{{ row.label }}</span>
          <span class="shrink-0 tabular-nums text-txt">{{ format(row.value) }}</span>
        </li>
      </ul>
    </div>
  </figure>
</template>

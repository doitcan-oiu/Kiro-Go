// 日志统计：把 /admin/api/logs 返回的原始条目聚合成图表可直接消费的序列。
//
// 全部是纯函数，不依赖 Vue，因此可以单独跑测试（见 scripts/test-stats.mjs）。
// 这一点是刻意的：分桶与百分位的边界条件（空数据、单点、时间倒流、模型数超过
// 调色板容量）用眼睛看图很难发现，用断言很容易。

/** 一分钟的毫秒数。所有分桶都以分钟为最小粒度。 */
const MINUTE_MS = 60_000

/**
 * 把日志按分钟分桶。
 *
 * 返回 `buckets` 个桶，索引 0 最旧、末位是当前分钟。落在窗口外或时间戳非法的
 * 条目被丢弃而不是夹到边界——夹边界会在首末桶堆出假峰值。
 *
 * @param {Array} logs 日志数组，读取 `time`（unix 秒）
 * @param {number} buckets 桶数量
 * @param {number} nowMs 当前时刻（可注入，便于测试）
 * @returns {{indexOf: (log:any)=>number, count:number}}
 */
function bucketIndexer(buckets, nowMs) {
  // 两端都向下取整到分钟，再按「分钟序号之差」定位桶。
  //
  // 这里必须比较「分钟起点之差」，不能直接用 (currentMinuteStart - ts) 求差：
  // 后者对当前分钟内的日志会得到负数。例如 now=14:30:10（分钟起点 14:30:00）、
  // 日志时间 14:30:07 时，差值为 -7000ms，floor 后是 -1，会被 ageMin < 0 判为
  // 「未来时间」而丢弃——于是当前分钟的请求全部消失，恰好是最新、最该被看到的
  // 那一批数据。改为先把日志时间也向下取整到分钟，同一分钟内 ageMin 恒为 0。
  const currentMinute = Math.floor(nowMs / MINUTE_MS)
  return {
    count: buckets,
    indexOf(log) {
      const ts = Number(log?.time)
      if (!Number.isFinite(ts) || ts <= 0) return -1
      const logMinute = Math.floor((ts * 1000) / MINUTE_MS)
      const ageMin = currentMinute - logMinute
      // ageMin < 0 表示日志时间晚于当前分钟（时钟漂移或伪造时间戳）：丢弃而不是
      // 夹到末桶，否则会在最新一格堆出假峰值。
      if (ageMin < 0 || ageMin >= buckets) return -1
      return buckets - 1 - ageMin
    },
  }
}

/**
 * 每分钟请求数（RPM）序列。
 *
 * @returns {{series:number[], current:number, peak:number, total:number}}
 *   `current` 取「上一个完整分钟」而非当前分钟：当前分钟尚未走完，用它当 RPM
 *   会在每分钟开头显示一个偏低的数字，看起来像流量骤降。
 */
export function requestsPerMinute(logs, { buckets = 30, nowMs = Date.now() } = {}) {
  const idx = bucketIndexer(buckets, nowMs)
  const series = new Array(buckets).fill(0)
  let total = 0

  for (const log of logs || []) {
    const i = idx.indexOf(log)
    if (i < 0) continue
    series[i] += 1
    total += 1
  }

  // 末位是当前（未走完的）分钟，倒数第二位才是最近一个完整分钟。
  const current = buckets >= 2 ? series[buckets - 2] : series[buckets - 1] || 0
  return {
    series,
    current,
    peak: series.length ? Math.max(...series) : 0,
    total,
  }
}

/**
 * 按模型拆分的流量序列，每个模型一条线。
 *
 * 模型数量可能很多（/v1/models 有上百个），全画出来只会得到一团毛线。因此按
 * 总请求数排序后只保留前 `topN`，其余合并成一条「其他」。
 *
 * @returns {{series:Array<{key:string,label:string,points:number[],total:number}>,
 *            buckets:number, peak:number}}
 */
export function trafficByModel(
  logs,
  { buckets = 30, topN = 5, nowMs = Date.now(), otherLabel = 'other' } = {},
) {
  const idx = bucketIndexer(buckets, nowMs)

  // 先按模型累计，确定谁进 topN。
  const totals = new Map()
  const rows = []
  for (const log of logs || []) {
    const i = idx.indexOf(log)
    if (i < 0) continue
    const model = String(log?.model || '-')
    totals.set(model, (totals.get(model) || 0) + 1)
    rows.push({ i, model })
  }

  const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  const keep = new Set(ranked.slice(0, topN).map(([model]) => model))
  const hasOther = ranked.length > topN

  const points = new Map()
  for (const model of keep) points.set(model, new Array(buckets).fill(0))
  if (hasOther) points.set(otherLabel, new Array(buckets).fill(0))

  for (const { i, model } of rows) {
    const key = keep.has(model) ? model : otherLabel
    const arr = points.get(key)
    if (arr) arr[i] += 1
  }

  const series = []
  for (const [model] of ranked.slice(0, topN)) {
    series.push({
      key: model,
      label: model,
      points: points.get(model),
      total: totals.get(model),
    })
  }
  if (hasOther) {
    const arr = points.get(otherLabel)
    series.push({
      key: '__other__',
      label: otherLabel,
      points: arr,
      total: arr.reduce((a, b) => a + b, 0),
    })
  }

  let peak = 0
  for (const s of series) for (const v of s.points) if (v > peak) peak = v

  return { series, buckets, peak }
}

/**
 * 首字延迟（TTFT）随时间的分布：每分钟计算 P50 / P95。
 *
 * 只统计带 `ttft > 0` 的条目。非流式请求不上报该字段（后端刻意留空），把它们
 * 按 0 计入会压低分位数；按 duration 代入又会抬高——两者都会让图表失真。
 *
 * 空桶用 `null` 表示「该分钟没有样本」，折线在此处断开而不是掉到 0。掉到 0
 * 会被读成「延迟突然变好」，而真相是没有数据。
 *
 * @returns {{p50:Array<number|null>, p95:Array<number|null>, buckets:number,
 *            peak:number, sampleCount:number, overallP50:number|null,
 *            overallP95:number|null}}
 */
export function ttftOverTime(logs, { buckets = 30, nowMs = Date.now() } = {}) {
  const idx = bucketIndexer(buckets, nowMs)
  const perBucket = Array.from({ length: buckets }, () => [])
  const all = []

  for (const log of logs || []) {
    const ttft = Number(log?.ttft)
    // 后端约定：非流式路径不写该字段，因此缺失/0 都表示「无此指标」。
    if (!Number.isFinite(ttft) || ttft <= 0) continue
    const i = idx.indexOf(log)
    if (i < 0) continue
    perBucket[i].push(ttft)
    all.push(ttft)
  }

  const p50 = perBucket.map((vals) => percentile(vals, 50))
  const p95 = perBucket.map((vals) => percentile(vals, 95))

  let peak = 0
  for (const v of p95) if (v !== null && v > peak) peak = v
  // P95 全为空时退回看 P50，否则纵轴会塌成 0 高度。
  if (peak === 0) for (const v of p50) if (v !== null && v > peak) peak = v

  return {
    p50,
    p95,
    buckets,
    peak,
    sampleCount: all.length,
    overallP50: percentile(all, 50),
    overallP95: percentile(all, 95),
  }
}

/**
 * 线性插值百分位。空数组返回 null（而不是 0）——「没有样本」和「延迟为 0」
 * 是两件不同的事，混淆它们会让空窗口显示成完美延迟。
 *
 * @param {number[]} values
 * @param {number} p 0–100
 */
export function percentile(values, p) {
  if (!values || values.length === 0) return null
  const sorted = [...values].sort((a, b) => a - b)
  if (sorted.length === 1) return sorted[0]

  const rank = (p / 100) * (sorted.length - 1)
  const lo = Math.floor(rank)
  const hi = Math.ceil(rank)
  if (lo === hi) return sorted[lo]
  // 插值：小样本下直接取整会让 P95 长期等于最大值，掩盖真实的尾部变化。
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (rank - lo)
}

/**
 * 成功率（0–100）。没有请求时返回 null，让调用方显示「—」而不是 0%——
 * 零流量不等于全部失败。
 */
export function successRate(logs) {
  const rows = logs || []
  if (!rows.length) return null
  let ok = 0
  for (const log of rows) if (log?.status === 'success') ok += 1
  return (ok / rows.length) * 100
}

/** 图表调色板。取自设计规范的强调色与语义色，保证与其余 UI 同源。 */
export const CHART_COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
  'var(--chart-6)',
]

/** 按索引取色，超出调色板长度后循环。 */
export function chartColor(index) {
  return CHART_COLORS[index % CHART_COLORS.length]
}

/**
 * 时间桶的 X 轴刻度标签（HH:MM）。
 *
 * 与 requestsPerMinute / ttftOverTime 的分桶口径严格一致：末位是当前分钟，
 * 索引每减 1 就往前一分钟。两者若不一致，图上的时间会与数据错位一格——这种
 * 偏差肉眼几乎看不出来，却会让人把延迟尖峰归因到错误的时刻。
 *
 * @param {number} buckets
 * @param {number} nowMs
 * @returns {string[]}
 */
export function bucketLabels(buckets, nowMs = Date.now()) {
  // 与 bucketIndexer 共用同一个锚点（向下取整到分钟），否则标签与数据可能错开
  // 一分钟。这种错位在图上完全看不出来，却会把延迟尖峰归因到错误的时刻。
  const currentMinuteStart = Math.floor(nowMs / MINUTE_MS) * MINUTE_MS
  const out = []
  for (let i = 0; i < buckets; i++) {
    const minutesAgo = buckets - 1 - i
    const d = new Date(currentMinuteStart - minutesAgo * MINUTE_MS)
    out.push(`${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`)
  }
  return out
}

/**
 * 每个账号的 RPM / TPM（近 `windowMinutes` 分钟的均值）。
 *
 * 返回 Map<accountId, {rpm, tpm, requests, tokens}>，账号卡片按 id 取用。
 * 一次遍历算出全部账号，避免每张卡片各自过滤一遍日志（N 张卡 × M 条日志）。
 *
 * 取均值而非「最后一分钟的瞬时值」：单个账号的流量比全局稀疏得多，瞬时值会在
 * 0 与峰值之间剧烈跳动，读不出任何趋势。窗口默认 5 分钟，是「能反映当前负载」
 * 与「足够平滑」之间的折中。
 *
 * @param {Array} logs
 * @param {{windowMinutes?:number, nowMs?:number}} opts
 * @returns {Map<string, {rpm:number, tpm:number, requests:number, tokens:number}>}
 */
export function accountThroughput(logs, { windowMinutes = 5, nowMs = Date.now() } = {}) {
  const out = new Map()
  if (windowMinutes <= 0) return out

  const cutoff = nowMs - windowMinutes * MINUTE_MS

  for (const log of logs || []) {
    const id = log?.accountId
    if (!id) continue
    const ts = Number(log?.time)
    if (!Number.isFinite(ts) || ts <= 0) continue
    if (ts * 1000 < cutoff) continue

    let row = out.get(id)
    if (!row) {
      row = { rpm: 0, tpm: 0, requests: 0, tokens: 0 }
      out.set(id, row)
    }
    row.requests += 1
    // 输入 + 输出。缓存读写不计入：它们不产生新的生成量，计入会让 TPM 虚高。
    const inTok = Number(log?.inputTokens) || 0
    const outTok = Number(log?.outputTokens) || 0
    row.tokens += inTok + outTok
  }

  for (const row of out.values()) {
    row.rpm = row.requests / windowMinutes
    row.tpm = row.tokens / windowMinutes
  }
  return out
}

/**
 * 存活时长（秒）。
 *
 * 从 `createdAt` 起算；账号已禁用且有 `disabledAt` 时到该时刻为止（停表），
 * 否则算到 `nowMs`（继续走表）。
 *
 * 返回 null 表示无法计算——`createdAt` 缺失（历史数据）时不猜测，由前端显示「—」。
 * 返回 0 与返回 null 是两件事：前者表示刚导入，后者表示不知道。
 *
 * @returns {{seconds:number, running:boolean}|null}
 */
export function accountLifetime(account, nowMs = Date.now()) {
  const created = Number(account?.createdAt)
  if (!Number.isFinite(created) || created <= 0) return null

  const disabledAt = Number(account?.disabledAt)
  const stopped = !account?.enabled && Number.isFinite(disabledAt) && disabledAt > 0

  const endMs = stopped ? disabledAt * 1000 : nowMs
  const seconds = Math.max(0, Math.floor((endMs - created * 1000) / 1000))
  return { seconds, running: !stopped }
}

/**
 * 账号利润 = 收入 − 成本。
 *
 * 两个值都由后端给出：`revenue` 是运行态累计（按模型单价折算并乘过全局倍率），
 * `cost` 是导入时绑定到该 Key 的采购单价。前端只做减法，不重算收入——收入需要
 * 逐条日志按模型查价，而日志接口默认只返回最近若干条，在前端算会得到一个随
 * 返回条数变化的假数字。
 *
 * @returns {{revenue:number, cost:number, profit:number, hasData:boolean}}
 *   `hasData` 为 false 表示既没有收入也没有成本（全新账号），此时应显示「—」
 *   而不是 $0.00 —— 后者会被读成「已经跑过但没赚到钱」。
 */
export function accountProfit(account) {
  const revenue = Number(account?.revenue) || 0
  const cost = Number(account?.cost) || 0
  return {
    revenue,
    cost,
    profit: revenue - cost,
    hasData: revenue > 0 || cost > 0,
  }
}

/**
 * 账号池的利润汇总，用于仪表盘卡片。
 *
 * 成本对所有账号累加（包括已停用的）：钱已经花了，停用不会退款，把停用账号排除
 * 会让总利润虚高。
 */
export function poolProfit(accounts) {
  let revenue = 0
  let cost = 0
  for (const acc of accounts || []) {
    revenue += Number(acc?.revenue) || 0
    cost += Number(acc?.cost) || 0
  }
  return { revenue, cost, profit: revenue - cost, hasData: revenue > 0 || cost > 0 }
}

/**
 * 日志维度的收入汇总，用于「这段时间赚了多少」。
 *
 * 只累加带 `revenue` 且未被标记 `revenueUnpriced` 的条目：查不到单价的模型
 * 收入不可信，计入会让数字偏低且无从察觉。返回值里的 `unpricedCount` 让调用方
 * 可以显式提示「有 N 条未计价」。
 */
export function logsRevenue(logs) {
  let revenue = 0
  let priced = 0
  let unpricedCount = 0
  for (const log of logs || []) {
    if (log?.revenueUnpriced) {
      unpricedCount += 1
      continue
    }
    const v = Number(log?.revenue)
    if (!Number.isFinite(v) || v <= 0) continue
    revenue += v
    priced += 1
  }
  return { revenue, priced, unpricedCount }
}

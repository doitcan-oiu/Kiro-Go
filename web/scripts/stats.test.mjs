// Tests for src/lib/stats.js — run with `npm run test:stats`.
//
// Uses node:test + node:assert so there is no test-runner dependency to install.
// The bucketing/percentile edge cases below are exactly the ones that are
// invisible when eyeballing a chart: an off-by-one in bucket indexing looks
// plausible, an empty-window percentile of 0 looks like great latency, and a
// clamped out-of-window entry looks like a traffic spike.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  accountLifetime,
  accountProfit,
  accountThroughput,
  bucketLabels,
  logsRevenue,
  percentile,
  poolProfit,
  profitTone,
  requestsPerMinute,
  successRate,
  trafficByModel,
  ttftOverTime,
} from '../src/lib/stats.js'

// A fixed "now" on an exact minute boundary keeps the arithmetic obvious.
const NOW = Date.parse('2026-08-02T12:00:00.000Z')
const MIN = 60_000

/** Builds a log entry `minutesAgo` before NOW. */
function at(minutesAgo, extra = {}) {
  return { time: Math.floor((NOW - minutesAgo * MIN) / 1000), status: 'success', ...extra }
}

test('percentile: empty input is null, not zero', () => {
  // Returning 0 here would render as "0ms latency" on the chart.
  assert.equal(percentile([], 50), null)
  assert.equal(percentile(undefined, 95), null)
})

test('percentile: single sample returns that sample', () => {
  assert.equal(percentile([42], 50), 42)
  assert.equal(percentile([42], 95), 42)
})

test('percentile: interpolates rather than rounding to a member', () => {
  // With [10,20] the 50th percentile sits exactly between them.
  assert.equal(percentile([10, 20], 50), 15)
  // Rounding down would make P95 equal max() for every small sample.
  assert.equal(percentile([0, 10, 20, 30, 40], 95), 38)
})

test('percentile: unsorted input is handled', () => {
  assert.equal(percentile([30, 10, 20], 50), 20)
})

test('requestsPerMinute: buckets oldest-first with current minute last', () => {
  const logs = [at(0), at(0), at(1), at(3)]
  const { series } = requestsPerMinute(logs, { buckets: 5, nowMs: NOW })

  assert.equal(series.length, 5)
  assert.equal(series[4], 2, 'current minute holds both 0-minute-old entries')
  assert.equal(series[3], 1, 'one minute ago')
  assert.equal(series[2], 0)
  assert.equal(series[1], 1, 'three minutes ago')
  assert.equal(series[0], 0)
})

test('requestsPerMinute: current uses the last COMPLETE minute', () => {
  // The in-progress minute is partially elapsed, so reporting it as RPM would
  // show an artificial dip at the top of every minute.
  const logs = [at(0), at(1), at(1), at(1)]
  const { current } = requestsPerMinute(logs, { buckets: 10, nowMs: NOW })
  assert.equal(current, 3)
})

test('requestsPerMinute: out-of-window entries are dropped, not clamped', () => {
  // Clamping would pile old traffic onto the first bucket as a fake spike.
  const logs = [at(0), at(99), at(-5)]
  const { series, total } = requestsPerMinute(logs, { buckets: 5, nowMs: NOW })
  assert.equal(total, 1)
  assert.equal(
    series.reduce((a, b) => a + b, 0),
    1,
  )
})

test('requestsPerMinute: malformed timestamps are ignored', () => {
  const logs = [{ time: null }, { time: 'x' }, { time: 0 }, { time: -1 }, at(0)]
  const { total } = requestsPerMinute(logs, { buckets: 5, nowMs: NOW })
  assert.equal(total, 1)
})

test('requestsPerMinute: empty input yields all-zero series', () => {
  const { series, current, peak, total } = requestsPerMinute([], { buckets: 3, nowMs: NOW })
  assert.deepEqual(series, [0, 0, 0])
  assert.equal(current, 0)
  assert.equal(peak, 0)
  assert.equal(total, 0)
})

test('trafficByModel: one series per model, ranked by volume', () => {
  const logs = [
    at(0, { model: 'sonnet' }),
    at(0, { model: 'sonnet' }),
    at(1, { model: 'sonnet' }),
    at(0, { model: 'haiku' }),
  ]
  const { series } = trafficByModel(logs, { buckets: 3, topN: 5, nowMs: NOW })

  assert.equal(series.length, 2)
  assert.equal(series[0].label, 'sonnet', 'highest volume first')
  assert.equal(series[0].total, 3)
  assert.equal(series[1].label, 'haiku')
  assert.equal(series[0].points.length, 3)
})

test('trafficByModel: collapses the tail into a single "other" series', () => {
  // Without this the chart becomes unreadable once a deployment uses many models.
  const logs = []
  for (let m = 0; m < 8; m += 1) {
    // model0 gets 8 requests, model1 gets 7, ... so ranking is deterministic.
    for (let n = 0; n < 8 - m; n += 1) logs.push(at(0, { model: `model${m}` }))
  }
  const { series } = trafficByModel(logs, {
    buckets: 3,
    topN: 3,
    nowMs: NOW,
    otherLabel: 'other',
  })

  assert.equal(series.length, 4, '3 kept + 1 aggregate')
  assert.deepEqual(
    series.slice(0, 3).map((s) => s.label),
    ['model0', 'model1', 'model2'],
  )
  const other = series[3]
  assert.equal(other.label, 'other')
  // model3..model7 => 5+4+3+2+1 = 15
  assert.equal(other.total, 15)
})

test('trafficByModel: no "other" series when models fit within topN', () => {
  const logs = [at(0, { model: 'a' }), at(0, { model: 'b' })]
  const { series } = trafficByModel(logs, { buckets: 3, topN: 5, nowMs: NOW })
  assert.equal(series.length, 2)
  assert.ok(!series.some((s) => s.key === '__other__'))
})

test('trafficByModel: missing model falls back to a placeholder key', () => {
  const logs = [at(0, {}), at(0, { model: '' })]
  const { series } = trafficByModel(logs, { buckets: 3, topN: 5, nowMs: NOW })
  assert.equal(series.length, 1)
  assert.equal(series[0].total, 2)
})

test('ttftOverTime: only counts entries that actually report ttft', () => {
  const logs = [
    at(0, { ttft: 100 }),
    at(0, { ttft: 300 }),
    at(0, { duration: 5000 }), // non-stream: no ttft field at all
    at(0, { ttft: 0 }), // explicit "no data" sentinel
  ]
  const { sampleCount, p50 } = ttftOverTime(logs, { buckets: 2, nowMs: NOW })

  assert.equal(sampleCount, 2, 'non-stream rows must not become samples')
  assert.equal(p50[1], 200)
})

test('ttftOverTime: empty buckets are null so the line breaks', () => {
  // A 0 here would read as "latency improved dramatically" instead of "no data".
  const logs = [at(2, { ttft: 250 })]
  const { p50, p95 } = ttftOverTime(logs, { buckets: 4, nowMs: NOW })

  assert.equal(p50[1], 250)
  assert.equal(p50[0], null)
  assert.equal(p50[2], null)
  assert.equal(p50[3], null)
  assert.equal(p95[0], null)
})

test('ttftOverTime: p95 >= p50 within each bucket', () => {
  const logs = [10, 20, 30, 40, 500].map((ttft) => at(0, { ttft }))
  const { p50, p95, peak } = ttftOverTime(logs, { buckets: 2, nowMs: NOW })

  assert.ok(p95[1] >= p50[1], `p95 ${p95[1]} should be >= p50 ${p50[1]}`)
  assert.equal(peak, p95[1], 'peak tracks the p95 line')
})

test('ttftOverTime: peak falls back to p50 when p95 is all null', () => {
  // Can only happen when every bucket is empty; peak must not stay 0 while a
  // p50 value exists, or the y-axis collapses.
  const { peak, sampleCount } = ttftOverTime([], { buckets: 3, nowMs: NOW })
  assert.equal(sampleCount, 0)
  assert.equal(peak, 0)
})

test('ttftOverTime: overall percentiles span the whole window', () => {
  const logs = [at(0, { ttft: 100 }), at(1, { ttft: 200 }), at(2, { ttft: 300 })]
  const { overallP50, overallP95 } = ttftOverTime(logs, { buckets: 5, nowMs: NOW })
  assert.equal(overallP50, 200)
  assert.ok(overallP95 >= 200 && overallP95 <= 300)
})

test('successRate: null for empty input, percentage otherwise', () => {
  // Zero traffic is not the same as total failure.
  assert.equal(successRate([]), null)
  assert.equal(successRate([{ status: 'success' }, { status: 'error' }]), 50)
  assert.equal(successRate([{ status: 'success' }]), 100)
  assert.equal(successRate([{ status: 'error' }]), 0)
})

// ── bucketLabels ─────────────────────────────────────────────────────────────
// 标签必须与分桶口径严格对齐。错开一格在图上几乎看不出来，却会把延迟尖峰
// 归因到错误的分钟，所以这里显式钉住首/末两端与数据序列的对应关系。

test('bucketLabels: last label is the current minute, first is buckets-1 ago', () => {
  const now = new Date('2026-08-02T14:30:45Z').getTime()
  const labels = bucketLabels(5, now)

  assert.equal(labels.length, 5)

  const fmt = (ms) => {
    const d = new Date(ms)
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }
  // 末位 = 当前分钟；首位 = 4 分钟前。
  assert.equal(labels[4], fmt(now))
  assert.equal(labels[0], fmt(now - 4 * 60_000))
})

test('bucketLabels: indices align with requestsPerMinute buckets', () => {
  const now = new Date('2026-08-02T14:30:45Z').getTime()
  const buckets = 6
  // 一条 3 分钟前的日志，应当落在索引 buckets-1-3 上。
  const logs = [{ time: Math.floor((now - 3 * 60_000) / 1000), status: 'success' }]

  const { series } = requestsPerMinute(logs, { buckets, nowMs: now })
  const labels = bucketLabels(buckets, now)

  const hitIndex = series.findIndex((v) => v === 1)
  assert.equal(hitIndex, buckets - 1 - 3)

  // 该索引对应的标签必须真的是「3 分钟前」的那一分钟。
  const expected = new Date(now - 3 * 60_000)
  const hhmm = `${String(expected.getHours()).padStart(2, '0')}:${String(expected.getMinutes()).padStart(2, '0')}`
  assert.equal(labels[hitIndex], hhmm)
})

test('bucketLabels: length always matches the requested bucket count', () => {
  // 图表按桶位索引标签数组，长度不一致会让整条 X 轴读数偏移一分钟。
  for (const n of [1, 2, 30, 60]) {
    assert.equal(bucketLabels(n, NOW).length, n)
  }
})

test('bucketLabels: mid-minute now does not drop the current minute', () => {
  // 回归测试。此前 bucketIndexer 以「向下取整到整分」为锚点计算 age，
  // 当 now 落在分钟中间（现实中的绝大多数时刻）时，本分钟内的日志算出
  // age = -1 而被 `ageMin < 0` 判为窗口外整批丢弃——恰好丢掉最新、最该被
  // 看到的数据。当时的测试把 NOW 钉在整分边界上，因此完全没有暴露这个问题。
  const now = Date.parse('2026-08-02T14:30:45.000Z')
  const logs = [
    { time: Math.floor(now / 1000), status: 'success' }, // 本分钟
    { time: Math.floor((now - 1 * MIN) / 1000), status: 'success' },
    { time: Math.floor((now - 3 * MIN) / 1000), status: 'success' },
  ]
  const { series, total } = requestsPerMinute(logs, { buckets: 6, nowMs: now })

  assert.equal(total, 3, '本分钟的日志不能被丢弃')
  assert.equal(series[5], 1, '本分钟落在末位桶')
  assert.equal(series[4], 1, '一分钟前')
  assert.equal(series[2], 1, '三分钟前')
})

test('bucketLabels: mid-minute labels align with mid-minute buckets', () => {
  // 分桶与标签必须共用同一个锚点，否则 tooltip 会把尖峰归因到错误的分钟。
  const now = Date.parse('2026-08-02T14:30:45.000Z')
  const buckets = 6
  const logs = [{ time: Math.floor((now - 3 * MIN) / 1000), status: 'success' }]

  const { series } = requestsPerMinute(logs, { buckets, nowMs: now })
  const labels = bucketLabels(buckets, now)

  const hit = series.findIndex((v) => v === 1)
  assert.equal(hit, buckets - 1 - 3, '3 分钟前应落在倒数第 4 个桶')

  const expected = new Date(now - 3 * MIN)
  const hhmm = `${String(expected.getHours()).padStart(2, '0')}:${String(expected.getMinutes()).padStart(2, '0')}`
  assert.equal(labels[hit], hhmm, '命中桶的标签必须是该分钟')
})

// ── accountThroughput ────────────────────────────────────────────────────────

test('accountThroughput: rpm/tpm are per-minute averages over the window', () => {
  const logs = [
    { time: Math.floor(NOW / 1000), accountId: 'a', inputTokens: 100, outputTokens: 50 },
    { time: Math.floor((NOW - 1 * MIN) / 1000), accountId: 'a', inputTokens: 200, outputTokens: 100 },
    { time: Math.floor((NOW - 2 * MIN) / 1000), accountId: 'b', inputTokens: 10, outputTokens: 5 },
  ]
  const m = accountThroughput(logs, { windowMinutes: 5, nowMs: NOW })

  const a = m.get('a')
  assert.equal(a.requests, 2)
  assert.equal(a.tokens, 450)
  assert.equal(a.rpm, 2 / 5)
  assert.equal(a.tpm, 450 / 5)

  const b = m.get('b')
  assert.equal(b.requests, 1)
  assert.equal(b.tokens, 15)
})

test('accountThroughput: entries outside the window are excluded', () => {
  // A burst an hour ago must not show up as current load.
  const logs = [
    { time: Math.floor((NOW - 60 * MIN) / 1000), accountId: 'a', inputTokens: 9999 },
    { time: Math.floor(NOW / 1000), accountId: 'a', inputTokens: 1, outputTokens: 1 },
  ]
  const m = accountThroughput(logs, { windowMinutes: 5, nowMs: NOW })
  assert.equal(m.get('a').requests, 1)
  assert.equal(m.get('a').tokens, 2)
})

test('accountThroughput: cache tokens are not counted toward tpm', () => {
  // Cache reads generate no new tokens; counting them inflates TPM.
  const logs = [
    {
      time: Math.floor(NOW / 1000),
      accountId: 'a',
      inputTokens: 10,
      outputTokens: 5,
      cacheRead: 5000,
      cacheCreation: 3000,
    },
  ]
  const m = accountThroughput(logs, { windowMinutes: 1, nowMs: NOW })
  assert.equal(m.get('a').tokens, 15)
})

test('accountThroughput: logs without accountId are skipped', () => {
  const logs = [{ time: Math.floor(NOW / 1000), inputTokens: 100 }]
  assert.equal(accountThroughput(logs, { nowMs: NOW }).size, 0)
})

test('accountThroughput: unknown account yields no entry, not a zero row', () => {
  // The card falls back to "—" for absent entries; a zero row would render
  // "0.0 rpm", implying the account is idle when it may simply be new.
  const m = accountThroughput([], { nowMs: NOW })
  assert.equal(m.get('nobody'), undefined)
})

// ── accountLifetime ──────────────────────────────────────────────────────────

test('accountLifetime: enabled account keeps counting up to now', () => {
  const acc = { enabled: true, createdAt: Math.floor((NOW - 90 * MIN) / 1000) }
  const life = accountLifetime(acc, NOW)
  assert.equal(life.running, true)
  assert.equal(life.seconds, 90 * 60)
})

test('accountLifetime: disabled account stops at disabledAt', () => {
  // The clock must stop, otherwise a long-disabled account appears to have the
  // longest uptime of all.
  const created = Math.floor((NOW - 10 * MIN) / 1000)
  const disabled = Math.floor((NOW - 4 * MIN) / 1000)
  const life = accountLifetime({ enabled: false, createdAt: created, disabledAt: disabled }, NOW)
  assert.equal(life.running, false)
  assert.equal(life.seconds, 6 * 60)
})

test('accountLifetime: disabled without disabledAt still counts to now', () => {
  // Legacy rows have no disabledAt; counting to now is the lesser evil versus
  // reporting 0, which would read as "died instantly".
  const life = accountLifetime(
    { enabled: false, createdAt: Math.floor((NOW - 5 * MIN) / 1000) },
    NOW,
  )
  assert.equal(life.running, true)
  assert.equal(life.seconds, 5 * 60)
})

test('accountLifetime: missing createdAt returns null, not zero', () => {
  // null renders as "—" (unknown); 0 would render as "just added".
  assert.equal(accountLifetime({ enabled: true }, NOW), null)
  assert.equal(accountLifetime({ enabled: true, createdAt: 0 }, NOW), null)
  assert.equal(accountLifetime(undefined, NOW), null)
})

test('accountLifetime: clock skew clamps to zero instead of going negative', () => {
  const life = accountLifetime(
    { enabled: true, createdAt: Math.floor((NOW + 10 * MIN) / 1000) },
    NOW,
  )
  assert.equal(life.seconds, 0)
})

test('accountLifetime: seconds is never NaN, even when nowMs is the wrong type', () => {
  // The regression this pins: AccountCard called accountLifetime(acc, { nowMs })
  // instead of accountLifetime(acc, nowMs). The old code produced
  // { seconds: NaN }, which is neither null (so the caller's "—" branch was
  // skipped) nor a usable number (so the formatter's `|| 0` turned it into
  // "0秒"). A wrong argument type must degrade to "unknown", not to zero.
  const acc = { enabled: true, createdAt: Math.floor((NOW - 90 * MIN) / 1000) }
  assert.equal(accountLifetime(acc, { nowMs: NOW }), null)
  assert.equal(accountLifetime(acc, 'not-a-number'), null)
  assert.equal(accountLifetime(acc, NaN), null)

  // The valid call still works, and seconds stays finite.
  const life = accountLifetime(acc, NOW)
  assert.equal(Number.isFinite(life.seconds), true)
  assert.equal(life.seconds, 90 * 60)
})

test('accountLifetime: a stopped clock ignores nowMs entirely', () => {
  // disabledAt fixes both ends of the interval, so a bad nowMs cannot corrupt
  // the result — it is never read on this path.
  const created = Math.floor((NOW - 10 * MIN) / 1000)
  const disabled = Math.floor((NOW - 4 * MIN) / 1000)
  const acc = { enabled: false, createdAt: created, disabledAt: disabled }
  const life = accountLifetime(acc, { nowMs: NOW })
  assert.equal(life.running, false)
  assert.equal(life.seconds, 6 * 60)
})

// ── profit ───────────────────────────────────────────────────────────────────
// 利润是「收入 − 成本」，两个数都可能缺失。核心断言是「缺数据」与「零利润」必须
// 可区分：把全新账号显示成 $0.00 会被读成「跑过但没赚钱」，而真相是还没跑过。

test('accountProfit: revenue minus cost', () => {
  const got = accountProfit({ revenue: 12.5, cost: 4 })
  assert.equal(got.revenue, 12.5)
  assert.equal(got.cost, 4)
  assert.equal(got.profit, 8.5)
  assert.equal(got.hasData, true)
})

test('accountProfit: negative profit is preserved, not clamped', () => {
  // 买了号但还没跑够量时利润本来就是负的，夹到 0 会掩盖亏损。
  const got = accountProfit({ revenue: 1, cost: 20 })
  assert.equal(got.profit, -19)
  assert.equal(got.hasData, true)
})

test('accountProfit: brand-new account has no data (not zero profit)', () => {
  const got = accountProfit({})
  assert.equal(got.profit, 0)
  assert.equal(got.hasData, false, 'no revenue and no cost means "unknown", not "$0 profit"')
})

test('accountProfit: cost alone counts as data', () => {
  // 刚买进来还没用过：成本已经发生，应当显示为负利润而不是「—」。
  const got = accountProfit({ cost: 20 })
  assert.equal(got.profit, -20)
  assert.equal(got.hasData, true)
})

test('accountProfit: malformed values degrade to 0', () => {
  const got = accountProfit({ revenue: 'abc', cost: null })
  assert.equal(got.profit, 0)
  assert.equal(got.hasData, false)
})

test('poolProfit: sums across accounts including disabled ones', () => {
  // 停用账号的成本必须计入：钱已经花了，排除它会让总利润虚高。
  const accounts = [
    { revenue: 10, cost: 3, enabled: true },
    { revenue: 0, cost: 20, enabled: false },
    { revenue: 5, cost: 2, enabled: true },
  ]
  const got = poolProfit(accounts)
  assert.equal(got.revenue, 15)
  assert.equal(got.cost, 25)
  assert.equal(got.profit, -10)
})

test('poolProfit: empty pool has no data', () => {
  assert.equal(poolProfit([]).hasData, false)
  assert.equal(poolProfit(undefined).hasData, false)
})

test('logsRevenue: sums priced entries only', () => {
  const logs = [
    { revenue: 1.5 },
    { revenue: 2.5 },
    { revenue: 9, revenueUnpriced: true }, // 未计价，必须排除
    { revenue: 0 },
  ]
  const got = logsRevenue(logs)
  assert.equal(got.revenue, 4)
  assert.equal(got.priced, 2)
  assert.equal(got.unpricedCount, 1)
})

test('logsRevenue: unpriced entries are counted so the UI can warn', () => {
  const got = logsRevenue([{ revenueUnpriced: true }, { revenueUnpriced: true }])
  assert.equal(got.revenue, 0)
  assert.equal(got.unpricedCount, 2, 'the count is what lets the panel surface a stale price table')
})

// ── profitTone ───────────────────────────────────────────────────────────────
// Shared by the account card, the detail modal and the dashboard. The whole
// point of extracting it is that the same figure cannot end up a different
// colour in two places, so the boundary cases are what matter here.

test('profitTone: profit is success, loss is error', () => {
  assert.equal(profitTone({ profit: 12.5, hasData: true }), 'success')
  assert.equal(profitTone({ profit: -3, hasData: true }), 'error')
})

test('profitTone: break-even is neutral, not green', () => {
  // Exactly 0 earned nothing; colouring it green would read as a profit.
  assert.equal(profitTone({ profit: 0, hasData: true }), null)
})

test('profitTone: no data is neutral', () => {
  // The value renders as "—" in this case; tinting a placeholder is meaningless.
  assert.equal(profitTone({ profit: 0, hasData: false }), null)
  assert.equal(profitTone({ profit: 99, hasData: false }), null)
  assert.equal(profitTone(null), null)
  assert.equal(profitTone(undefined), null)
})

test('profitTone: non-finite profit is neutral rather than green', () => {
  // Neither value can arise from real data: revenue and cost are both finite, so
  // their difference is too. Both mean the input is corrupt, and a corrupt figure
  // must not be painted as a confident green profit — that is the same class of
  // mistake as formatDurationCompact swallowing NaN into "0秒".
  assert.equal(profitTone({ profit: NaN, hasData: true }), null)
  assert.equal(profitTone({ profit: Infinity, hasData: true }), null)
})

test('profitTone: agrees with accountProfit and poolProfit outputs', () => {
  // Guards the real call shape: the helper takes the whole result object, so a
  // field rename in either producer must surface here.
  assert.equal(profitTone(accountProfit({ revenue: 10, cost: 4 })), 'success')
  assert.equal(profitTone(accountProfit({ revenue: 1, cost: 4 })), 'error')
  assert.equal(profitTone(accountProfit({})), null)
  assert.equal(profitTone(poolProfit([{ revenue: 10, cost: 1 }])), 'success')
  assert.equal(profitTone(poolProfit([])), null)
})

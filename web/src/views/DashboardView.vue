<script setup>
// 仪表盘：数据优先的落地页（规范 §10.1）。
//
// 四张统计卡片强制单行（§10.1「统计卡片行：4 列网格」），下方两张折线图：
//   - 首字延迟分布：P50 / P95 双线，只统计流式请求上报的 ttft
//   - 近期流量：每个模型一条线
//
// 两张图的数据都来自 /admin/api/logs（后端保留最近 500 条），在前端聚合。
// 聚合逻辑放在 lib/stats.js 里且有单测覆盖——分桶与百分位的边界条件（空窗口、
// 当前分钟未走完、模型数超过调色板容量）看图很难发现，用断言很容易。
import { computed, onMounted } from 'vue'
import { storeToRefs } from 'pinia'
import { PhCurrencyDollar, PhFlowArrow, PhGauge, PhUsers } from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { useDataStore } from '@/stores/data'
import { formatNumber, formatUsd, toFixed } from '@/lib/format'
import { poolProfit, profitTone, requestsPerMinute, successRate } from '@/lib/stats'
import StatCard from '@/components/ui/StatCard.vue'
import TtftChart from '@/components/dashboard/TtftChart.vue'
import ModelTrafficChart from '@/components/dashboard/ModelTrafficChart.vue'

/** 图表时间窗口（分钟）。30 分钟在 720px 宽度下每桶约 24px，足够分辨。 */
const WINDOW_MINUTES = 30

const { t } = useI18n()
const data = useDataStore()
const { status, accounts, logs } = storeToRefs(data)

// store 已经在轮询 status；日志只在首次进入时补一次。
onMounted(() => {
  if (!logs.value.length) data.loadLogs().catch(() => {})
})

const s = computed(() => status.value || {})

const rpm = computed(() => requestsPerMinute(logs.value, { buckets: WINDOW_MINUTES }))

const rate = computed(() => successRate(logs.value))

/**
 * 全池利润 = Σ(每账号收入 − 成本)。
 *
 * 在前端聚合而不是后端返回一个总数：倍率改动后前端能立即用新值重算，而后端存
 * 一个派生总额会与账号明细产生两处口径不一致。
 */
const profit = computed(() => poolProfit(accounts.value))

const cards = computed(() => [
  {
    key: 'rpm',
    label: t('dashboard.rpm'),
    // 取上一个完整分钟：当前分钟尚未走完，用它会在每分钟开头显示偏低的值。
    value: formatNumber(rpm.value.current),
    sub: `${t('dashboard.rpmSub')} · ${t('dashboard.rpmPeak')} ${formatNumber(rpm.value.peak)}`,
    icon: PhGauge,
    tone: 'accent',
  },
  {
    key: 'accounts',
    label: t('stats.accounts'),
    value: formatNumber(accounts.value.length),
    sub: t('dashboard.accountsSub', formatNumber(s.value.available ?? 0)),
    icon: PhUsers,
  },
  {
    key: 'requests',
    label: t('stats.requests'),
    value: formatNumber(s.value.totalRequests ?? 0),
    sub:
      rate.value === null
        ? t('stats.completed')
        : t('dashboard.requestsSub', `${toFixed(rate.value, 1)}%`),
    icon: PhFlowArrow,
  },
  {
    key: 'profit',
    label: t('dashboard.profit'),
    // 收入按模型单价 × 全局倍率累计，成本绑定在每个 Key 上。无任何数据时显示
    // 「—」而不是 $0.00：后者会被读成「不赚不亏」，而真相是还没有数据。
    value: profit.value.hasData ? formatUsd(profit.value.profit) : '—',
    sub: profit.value.hasData
      ? `${t('dashboard.revenue')} ${formatUsd(profit.value.revenue)} · ${t('dashboard.cost')} ${formatUsd(profit.value.cost)}`
      : t('dashboard.profitPlaceholder'),
    icon: PhCurrencyDollar,
    // 盈利绿、亏损红、保本或无数据中性。与账号卡片共用 profitTone，避免同一个
    // 口径的数字在两个页面显示成不同颜色。原来这里正数用的是 accent（品牌绿），
    // 与语义色 success 是两个不同的色值，卡片和仪表盘并排看时会显得像两种状态。
    tone: profitTone(profit.value) ?? 'neutral',
  },
])
</script>

<template>
  <div class="flex flex-col gap-lg">
    <!-- §10.1 统计卡片行：4 列网格，gap 24px。
         窄屏降为 2 列而不是 1 列，保持「一行看完」的意图不被完全打散。 -->
    <div class="grid grid-cols-2 gap-lg laptop:grid-cols-4">
      <StatCard
        v-for="card in cards"
        :key="card.key"
        :label="card.label"
        :value="card.value"
        :sub="card.sub"
        :icon="card.icon"
        :tone="card.tone"
      />
    </div>

    <TtftChart :logs="logs" :buckets="WINDOW_MINUTES" />

    <ModelTrafficChart :logs="logs" :buckets="WINDOW_MINUTES" />
  </div>
</template>

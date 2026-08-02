<script setup>
// 首字延迟（TTFT）分布：每分钟的 P50 与 P95 两条线。
//
// 为什么画 P50 + P95 而不是平均值：首字延迟的分布是长尾的，平均值会被少数
// 极慢请求拉高，既看不出典型体验（P50），也看不出最差体验（P95）。运维真正
// 关心的是「大多数人多快」和「最糟的一批多慢」这两个数。
//
// 数据来源是后端新增的 RequestLog.ttft，只有流式路径上报。非流式请求不参与
// 统计（详见 proxy/ttft.go 的说明），因此样本数会少于总请求数——这里把样本数
// 显式标出来，避免让人误以为图表漏了数据。
import { computed } from 'vue'
import { useI18n } from '@/lib/i18n'
import { bucketLabels, ttftOverTime } from '@/lib/stats'
import LineChart from '@/components/charts/LineChart.vue'

const props = defineProps({
  logs: { type: Array, default: () => [] },
  buckets: { type: Number, default: 30 },
})

const { t } = useI18n()

const stats = computed(() => ttftOverTime(props.logs, { buckets: props.buckets }))
const labels = computed(() => bucketLabels(props.buckets))

const series = computed(() => [
  {
    key: 'p95',
    label: t('dashboard.ttftP95'),
    color: 'var(--chart-4)',
    points: stats.value.p95,
    dashed: true,
  },
  {
    key: 'p50',
    label: t('dashboard.ttftP50'),
    color: 'var(--chart-1)',
    points: stats.value.p50,
    area: true,
  },
])

/** 毫秒 → 可读文本。超过 1s 换用秒，避免纵轴出现 5 位数。 */
function formatMs(v) {
  if (!Number.isFinite(v)) return '-'
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 0 : 1)}s`
  return `${Math.round(v)}ms`
}
</script>

<template>
  <div>
    <!-- 概览数字：整个窗口的 P50 / P95 与样本数 -->
    <div class="mb-3 flex flex-wrap items-baseline gap-x-lg gap-y-1">
      <span class="flex items-baseline gap-1.5">
        <span class="text-caption text-txt-tertiary">{{ t('dashboard.ttftP50') }}</span>
        <span class="tnum text-title-sm text-txt">
          {{ stats.overallP50 === null ? '—' : formatMs(stats.overallP50) }}
        </span>
      </span>
      <span class="flex items-baseline gap-1.5">
        <span class="text-caption text-txt-tertiary">{{ t('dashboard.ttftP95') }}</span>
        <span class="tnum text-title-sm text-txt">
          {{ stats.overallP95 === null ? '—' : formatMs(stats.overallP95) }}
        </span>
      </span>
      <span class="text-caption-sm text-txt-tertiary">
        {{ t('dashboard.ttftSamples', stats.sampleCount) }}
      </span>
    </div>

    <LineChart
      :series="series"
      :labels="labels"
      :height="196"
      :format="formatMs"
      :empty-text="t('dashboard.ttftEmpty')"
      :aria-label="t('dashboard.ttftTitle')"
    />

    <!-- 图例 -->
    <ul class="mt-2 flex flex-wrap items-center gap-x-lg gap-y-1">
      <li v-for="s in series" :key="s.key" class="flex items-center gap-1.5 text-caption">
        <span
          class="h-0.5 w-4 shrink-0 rounded-full"
          :style="{
            backgroundColor: s.color,
            opacity: s.dashed ? 0.75 : 1,
          }"
          aria-hidden="true"
        />
        <span class="text-txt-tertiary">{{ s.label }}</span>
      </li>
    </ul>
  </div>
</template>

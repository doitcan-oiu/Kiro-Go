<script setup>
// 近期流量：每个模型一条折线。
//
// 模型数量不可控（/v1/models 有上百个），全部画出来是一团毛线。stats.trafficByModel
// 按总量排序后只保留前 N 条，其余合并成「其他」——既保住了主要模型的可读性，
// 又不会让长尾流量凭空消失（总量仍然守恒）。
import { computed } from 'vue'
import { useI18n } from '@/lib/i18n'
import { bucketLabels, chartColor, trafficByModel } from '@/lib/stats'
import LineChart from '@/components/charts/LineChart.vue'

const props = defineProps({
  logs: { type: Array, default: () => [] },
  buckets: { type: Number, default: 30 },
  /** 最多单独成线的模型数，其余折叠为「其他」。 */
  topN: { type: Number, default: 6 },
})

const { t } = useI18n()

const stats = computed(() =>
  trafficByModel(props.logs, {
    buckets: props.buckets,
    topN: props.topN,
    otherLabel: t('dashboard.trafficOther'),
  }),
)

const labels = computed(() => bucketLabels(props.buckets))

const series = computed(() =>
  stats.value.series.map((s, i) => ({
    key: s.key,
    label: s.label,
    // 折叠序列固定用中性灰，不占用调色板里的高辨识度颜色。
    color: s.key === '__other__' ? 'var(--chart-other)' : chartColor(i),
    points: s.points,
    total: s.total,
  })),
)

const totalRequests = computed(() => series.value.reduce((sum, s) => sum + s.total, 0))

/** 纵轴是请求数，只可能是整数。 */
function formatCount(v) {
  return Number.isFinite(v) ? String(Math.round(v)) : '-'
}
</script>

<template>
  <div>
    <div class="mb-3 flex flex-wrap items-baseline gap-x-lg gap-y-1">
      <span class="flex items-baseline gap-1.5">
        <span class="text-caption text-txt-tertiary">{{ t('dashboard.trafficWindow', buckets) }}</span>
        <span class="tnum text-title-sm text-txt">{{ totalRequests }}</span>
      </span>
      <span class="text-caption-sm text-txt-tertiary">
        {{ t('dashboard.trafficModels', stats.series.length) }}
      </span>
    </div>

    <LineChart
      :series="series"
      :labels="labels"
      :height="196"
      :format="formatCount"
      :empty-text="t('dashboard.trafficEmpty')"
      :aria-label="t('dashboard.trafficTitle')"
    />

    <!-- 图例：带各模型窗口内总量，便于按量排查 -->
    <ul v-if="series.length" class="mt-2 flex flex-wrap items-center gap-x-lg gap-y-1">
      <li v-for="s in series" :key="s.key" class="flex items-center gap-1.5 text-caption">
        <span
          class="h-0.5 w-4 shrink-0 rounded-full"
          :style="{ backgroundColor: s.color }"
          aria-hidden="true"
        />
        <span class="max-w-[180px] truncate text-txt-tertiary" :title="s.label">{{ s.label }}</span>
        <span class="tnum text-txt-secondary">{{ s.total }}</span>
      </li>
    </ul>
  </div>
</template>

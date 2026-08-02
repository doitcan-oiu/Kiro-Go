<script setup>
// 单个账号卡片。
//
// 四栏网格下每张卡片只有约 300px 宽，因此信息按「先看状态、再看指标、最后看操作」
// 分三段排布：顶部身份与状态、中部配额与指标网格、底部操作。
//
// 禁用/封禁的账号整卡换成淡红磨砂玻璃（glass-danger），而不是仅仅把某个徽章标红：
// 四栏布局下扫视时单个小徽章太容易漏掉，整卡变色才是可靠的信号。
import { computed } from 'vue'
import {
  PhArrowsClockwise,
  PhCopy,
  PhFlask,
  PhGlobeHemisphereWest,
  PhTrash,
  PhUserCircle,
} from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import {
  formatDurationCompact,
  formatNum,
  formatShortDate,
  formatUsd,
  formatTrialExpiry,
  maskEmail,
  toFixed,
} from '@/lib/format'
import { accountLifetime, accountProfit } from '@/lib/stats'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseCheckbox from '@/components/ui/BaseCheckbox.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import UsageMeter from '@/components/ui/UsageMeter.vue'

const props = defineProps({
  account: { type: Object, required: true },
  selected: { type: Boolean, default: false },
  privacy: { type: Boolean, default: true },
  busy: { type: Boolean, default: false },
  /** 由 AccountsView 从日志算出的 `{ rpm, tpm }`；无数据时为 null。 */
  throughput: { type: Object, default: null },
})

const emit = defineEmits([
  'toggleSelect',
  'refresh',
  'detail',
  'copyJson',
  'toggle',
  'test',
  'delete',
])

const { t } = useI18n()

const a = computed(() => props.account)

const displayEmail = computed(() => {
  const email = a.value.email
  if (!email) return `${String(a.value.id || '').slice(0, 12)}…`
  return props.privacy ? maskEmail(email) : email
})

/** `banStatus` 为 'ACTIVE' 表示健康；其余值都是封禁/停用。 */
const banned = computed(() => {
  const s = a.value.banStatus
  return Boolean(s) && s !== 'ACTIVE'
})

/** 整卡告警：被封禁，或被手动停用。 */
const inactive = computed(() => banned.value || !a.value.enabled)

const SUBSCRIPTION_KEYS = {
  POWER: 'subscription.power',
  PRO_PLUS: 'subscription.proPlus',
  PRO: 'subscription.pro',
  FREE: 'subscription.free',
}

/** 订阅档位。优先用上游返回的可读名称，回退到枚举翻译。 */
const subscription = computed(() => {
  const raw = String(a.value.subscriptionType || '').toUpperCase()
  const key = SUBSCRIPTION_KEYS[raw]
  if (key) return t(key)
  return a.value.subscriptionTitle || raw || '—'
})

const subscriptionTone = computed(() => {
  const raw = String(a.value.subscriptionType || '').toUpperCase()
  // POWER / PRO_PLUS 是付费高档位，用强调色；FREE 保持中性，避免免费账号
  // 在四栏里显得比付费账号更醒目。
  if (raw === 'POWER' || raw === 'PRO_PLUS') return 'green'
  if (raw === 'PRO') return 'blue'
  return 'gray'
})

/** 导入方式（provider）。`provider` 缺失时回退到 `authMethod`。 */
const providerLabel = computed(() => {
  const raw = a.value.provider || a.value.authMethod || ''
  const known = {
    idc: t('auth.enterprise'),
    social: t('auth.social'),
    api_key: t('apikey.key'),
    apikey: t('apikey.key'),
    BuilderId: 'BuilderID',
    builderid: 'BuilderID',
    AzureAD: t('local.providerEnterprise'),
    Github: t('local.providerGithub'),
    Google: t('local.providerGoogle'),
  }[raw]
  return known || raw || '—'
})

/** 健康状态徽章：封禁 / 无 token / 已过期 / 正常，四者互斥。 */
const health = computed(() => {
  const acc = a.value
  if (banned.value) {
    return {
      label: acc.banStatus === 'SUSPENDED' ? t('accounts.suspended') : t('accounts.banned'),
      tone: 'red',
    }
  }
  if (!acc.hasToken) return { label: t('accounts.noToken'), tone: 'red' }
  if (Number(acc.expiresAt) > 0 && Number(acc.expiresAt) * 1000 < Date.now()) {
    return { label: t('accounts.expired'), tone: 'yellow' }
  }
  if (!acc.enabled) return { label: t('accounts.disabled'), tone: 'gray' }
  return { label: t('accounts.normal'), tone: 'green' }
})

/** 次要徽章：权重、试用、超额，只在非默认值时出现，避免每张卡都挂一排。 */
const extraBadges = computed(() => {
  const out = []
  const acc = a.value
  if (acc.trialStatus === 'ACTIVE' && Number(acc.trialUsageLimit) > 0) {
    out.push({ key: 'trial', label: t('accounts.trial'), tone: 'blue' })
  }
  if (Number(acc.weight) >= 2) {
    out.push({ key: 'weight', label: t('accounts.weightShort', acc.weight), tone: 'gray' })
  }
  if (acc.overageStatus === 'ENABLED') {
    out.push({ key: 'overage', label: t('accounts.overageOn'), tone: 'yellow' })
  }
  return out
})

const hasMainQuota = computed(() => Number(a.value.usageLimit) > 0)
const hasTrialQuota = computed(() => Number(a.value.trialUsageLimit) > 0)

// `usagePercent` 从服务端返回的是 0–1 的小数。
const mainPercent = computed(() => Number(a.value.usagePercent || 0) * 100)
const trialPercent = computed(() => Number(a.value.trialUsagePercent || 0) * 100)

const mainValueText = computed(
  () => `${toFixed(a.value.usageCurrent, 1)} / ${toFixed(a.value.usageLimit, 0)}`,
)
const trialValueText = computed(
  () => `${toFixed(a.value.trialUsageCurrent, 1)} / ${toFixed(a.value.trialUsageLimit, 0)}`,
)

const trialLabel = computed(() => {
  const expiry = formatTrialExpiry(a.value.trialExpiresAt)
  return expiry ? `${t('accounts.trialQuota')} · ${expiry}` : t('accounts.trialQuota')
})

/**
 * 利润 = 收入 − 成本。
 *
 * 收入由后端按模型单价 × 全局倍率累计（Account.Revenue），成本是采购时绑定到该
 * Key 的单价（Account.Cost）。两者都可能缺失：全新账号还没有请求也没有成本，
 * 此时 hasData 为 false，卡片显示「—」而不是 $0.00 —— 后者会被读成「不赚不亏」，
 * 而真相是「还没有数据」。
 */
const profit = computed(() => accountProfit(a.value))

const lifetime = computed(() =>
  accountLifetime(a.value, { nowMs: Date.now() }),
)

/**
 * 指标网格（2 列 × 3 行）。六项固定顺序，缺数据显示「—」而不是 0：
 * 0 会被读成「有数据且为零」，而多数情况是「还没有请求」。
 */
const metrics = computed(() => {
  const tp = props.throughput
  return [
    {
      key: 'rpm',
      label: t('accounts.rpm'),
      value: tp && tp.rpm > 0 ? String(tp.rpm) : '—',
      hint: t('accounts.rpmHint'),
    },
    {
      key: 'tpm',
      label: t('accounts.tpm'),
      value: tp && tp.tpm > 0 ? formatNum(tp.tpm) : '—',
      hint: t('accounts.tpmHint'),
    },
    {
      key: 'requests',
      label: t('accounts.requests'),
      value: Number(a.value.requestCount || 0).toLocaleString('en-US'),
    },
    {
      key: 'profit',
      label: t('accounts.profit'),
      // 缺数据显示「—」；亏损保留负号，不夹到 0。
      value: profit.value.hasData ? formatUsd(profit.value.profit) : '—',
      hint: profit.value.hasData
        ? `${t('accounts.revenue')} ${formatUsd(profit.value.revenue)} − ${t('accounts.cost')} ${formatUsd(profit.value.cost)}`
        : t('accounts.profitHint'),
      // 亏损标红：三栏扫视时颜色比数字更快被注意到。
      tone: profit.value.hasData && profit.value.profit < 0 ? 'error' : null,
    },
    {
      key: 'added',
      label: t('accounts.addedAt'),
      value: formatShortDate(a.value.createdAt),
    },
    {
      key: 'alive',
      label: inactive.value ? t('accounts.aliveStopped') : t('accounts.alive'),
      value: lifetime.value === null ? '—' : formatDurationCompact(lifetime.value),
      hint: inactive.value ? t('accounts.aliveStoppedHint') : t('accounts.aliveHint'),
    },
  ]
})
</script>

<template>
  <article
    class="flex flex-col rounded-2xl p-sm transition-[transform,box-shadow] duration-[var(--dur-fast)] ease-[var(--ease-out-expo)] hover:-translate-y-0.5"
    :class="[
      inactive ? 'glass-danger' : 'glass-thin',
      selected && 'ring-2 ring-[var(--accent-primary)] ring-offset-0',
    ]"
  >
    <!-- ═══ 身份 + 状态 ═══ -->
    <div class="flex items-start gap-2.5">
      <BaseCheckbox
        :model-value="selected"
        :aria-label="t('accounts.selectAccount', account.email || account.id)"
        class="pt-0.5"
        @update:model-value="emit('toggleSelect')"
      />

      <div class="min-w-0 flex-1">
        <h3
          class="truncate font-brand text-title-sm leading-tight text-txt"
          :title="account.email || account.id"
        >
          {{ displayEmail }}
        </h3>

        <!-- provider · region：身份的补充信息，用小字紧跟其下 -->
        <p class="mt-1 flex min-w-0 items-center gap-1.5 text-caption text-txt-tertiary">
          <span class="truncate" :title="t('accounts.provider')">{{ providerLabel }}</span>
          <span v-if="account.region" class="text-txt-tertiary/50" aria-hidden="true">·</span>
          <span
            v-if="account.region"
            class="inline-flex min-w-0 items-center gap-1"
            :title="t('accounts.region')"
          >
            <PhGlobeHemisphereWest :size="12" class="shrink-0" />
            <span class="truncate">{{ account.region }}</span>
          </span>
        </p>
      </div>

      <!-- 订阅档位单独右上角固定位：三栏扫视时这是最常对比的字段 -->
      <StatusBadge :label="subscription" :tone="subscriptionTone" class="shrink-0" />
    </div>

    <!-- 状态徽章行 -->
    <div class="mt-2.5 flex flex-wrap items-center gap-1.5">
      <StatusBadge :label="health.label" :tone="health.tone" dot />
      <StatusBadge v-for="b in extraBadges" :key="b.key" :label="b.label" :tone="b.tone" />
    </div>

    <!-- ═══ 配额 ═══ -->
    <div v-if="hasMainQuota || hasTrialQuota" class="mt-sm space-y-2">
      <UsageMeter
        v-if="hasMainQuota"
        :label="t('accounts.mainQuota')"
        :percent="mainPercent"
        :value-text="mainValueText"
      />
      <UsageMeter
        v-if="hasTrialQuota"
        :label="trialLabel"
        :percent="trialPercent"
        :value-text="trialValueText"
      />
    </div>

    <!-- ═══ 指标网格：3×2 ═══ -->
    <dl class="mt-sm grid grid-cols-2 gap-x-3 gap-y-2 border-t border-divider pt-sm">
      <div v-for="m in metrics" :key="m.key" class="min-w-0">
        <dt class="truncate text-caption-sm text-txt-tertiary" :title="m.hint || m.label">
          {{ m.label }}
        </dt>
        <dd
          class="tnum mt-0.5 truncate text-body-sm"
          :class="m.tone === 'error' ? 'text-error' : 'text-txt'"
          :title="m.hint || String(m.value)"
        >
          {{ m.value }}
        </dd>
      </div>
    </dl>

    <!-- ═══ 操作 ═══ -->
    <!-- mt-auto 把操作行推到卡片底部，同一行的多张卡片按钮对齐 -->
    <div class="mt-auto flex items-center gap-1 border-t border-divider pt-sm">
      <BaseButton
        v-if="!banned"
        :variant="account.enabled ? 'glass' : 'primary'"
        size="xs"
        @click="emit('toggle')"
      >
        {{ account.enabled ? t('accounts.disable') : t('accounts.enable') }}
      </BaseButton>
      <BaseButton variant="glass" size="xs" @click="emit('test')">
        <template #icon><PhFlask :size="14" /></template>
        {{ t('accounts.test') }}
      </BaseButton>

      <!-- 图标操作靠右：低频且有 tooltip，不需要占用文字宽度 -->
      <div class="ml-auto flex shrink-0 items-center">
        <BaseButton
          variant="ghost"
          size="xs"
          icon
          :loading="busy"
          :title="t('accounts.refresh')"
          :aria-label="t('accounts.refresh')"
          @click="emit('refresh')"
        >
          <template #icon><PhArrowsClockwise :size="15" /></template>
        </BaseButton>
        <BaseButton
          variant="ghost"
          size="xs"
          icon
          :title="t('accounts.detail')"
          :aria-label="t('accounts.detail')"
          @click="emit('detail')"
        >
          <template #icon><PhUserCircle :size="15" /></template>
        </BaseButton>
        <BaseButton
          variant="ghost"
          size="xs"
          icon
          :title="t('accounts.copyJSON')"
          :aria-label="t('accounts.copyJSON')"
          @click="emit('copyJson')"
        >
          <template #icon><PhCopy :size="15" /></template>
        </BaseButton>
        <BaseButton
          variant="ghost"
          size="xs"
          icon
          class="text-error hover:bg-[rgb(239_68_68/0.12)]"
          :title="t('accounts.delete')"
          :aria-label="t('accounts.delete')"
          @click="emit('delete')"
        >
          <template #icon><PhTrash :size="15" /></template>
        </BaseButton>
      </div>
    </div>
  </article>
</template>

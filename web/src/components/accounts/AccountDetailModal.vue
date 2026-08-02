<script setup>
// Account detail: read-only info plus the editable per-account fields
// (machine id, weight, overage, proxy, tags) and the model list.
//
// Every save is a partial `PUT /accounts/{id}`, so a field only ever writes
// itself and cannot clobber a sibling.
import { computed, ref, watch } from 'vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { formatDateTime, formatNum, formatUsd, maskEmail, toFixed } from '@/lib/format'
import { accountProfit, profitTone } from '@/lib/stats'
import BaseModal from '@/components/ui/BaseModal.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSwitch from '@/components/ui/BaseSwitch.vue'
import UsageMeter from '@/components/ui/UsageMeter.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  account: { type: Object, default: null },
  privacy: { type: Boolean, default: true },
  /** Cache metrics keyed by account id, from GET /stats. */
  cache: { type: Object, default: null },
})

const emit = defineEmits(['close', 'saved'])

const { t } = useI18n()

const machineId = ref('')
const weight = ref(0)
const cost = ref(0)

/**
 * 利润明细 = 收入 − 成本。
 *
 * 与卡片共用 accountProfit，避免两处各算一遍而在口径上悄悄分叉：卡片显示的
 * 数字和详情里展开的三行必须始终一致，否则用户会以为其中一处坏了。
 */
const profit = computed(() => accountProfit(acc.value || {}))
const proxyURL = ref('')
const tags = ref('')
const machineIdError = ref('')
const proxyError = ref('')
const saving = ref('')

const models = ref([])
const modelsLoading = ref(false)
const modelsLoaded = ref(false)

const overageBusy = ref(false)
const overage = ref(null)

const acc = computed(() => props.account || {})
const id = computed(() => acc.value.id)

// Reset the form whenever a different account is opened.
watch(
  () => [props.open, acc.value.id],
  ([isOpen]) => {
    if (!isOpen) return
    machineId.value = acc.value.machineId || ''
    weight.value = Number(acc.value.weight ?? 1)
    cost.value = Number(acc.value.cost ?? 0)
    proxyURL.value = acc.value.proxyURL || ''
    tags.value = (acc.value.tags || []).join(', ')
    machineIdError.value = ''
    proxyError.value = ''
    models.value = []
    modelsLoaded.value = false
    overage.value = null
  },
  { immediate: true },
)

const displayEmail = computed(() =>
  props.privacy ? maskEmail(acc.value.email) : acc.value.email || '—',
)

const basicRows = computed(() => [
  { label: t('detail.email'), value: displayEmail.value },
  { label: t('detail.userId'), value: acc.value.userId || '—' },
  { label: t('detail.authMethod'), value: acc.value.provider || acc.value.authMethod || '—' },
  { label: t('detail.region'), value: acc.value.region || 'us-east-1' },
])

// ── overage ────────────────────────────────────────────────────────────────
const overageData = computed(() => overage.value || acc.value)

const overageEnabled = computed(() => overageData.value.overageStatus === 'ENABLED')

/**
 * The switch is disabled when the upstream reports the account cannot do
 * overage. An empty capability means "unknown", which stays interactive.
 */
const overageLocked = computed(() => {
  const cap = overageData.value.overageCapability
  return Boolean(cap) && cap !== 'OVERAGE_CAPABLE'
})

const overageStateText = computed(() => {
  if (overageBusy.value) return t('detail.overageSwitching')
  const status = overageData.value.overageStatus
  if (status === 'ENABLED') return t('detail.overageEnabled')
  if (status === 'DISABLED') return t('detail.overageDisabled')
  return t('detail.overageUnknown')
})

const overageRows = computed(() => {
  const d = overageData.value
  return [
    { label: t('detail.overageStatus'), value: d.overageStatus || '—' },
    { label: t('detail.overageCap'), value: `$${toFixed(d.overageCap, 2)}` },
    { label: t('detail.overageRate'), value: `$${toFixed(d.overageRate, 4)}` },
    { label: t('detail.overageCurrent'), value: `$${toFixed(d.currentOverages, 2)}` },
    { label: t('detail.overageCheckedAt'), value: formatDateTime(d.overageCheckedAt) },
  ]
})

async function toggleOverage(next) {
  overageBusy.value = true
  try {
    const res = await api.setOverage(id.value, next)
    if (res?.success === false) throw new Error(res.error || 'failed')
    overage.value = { ...overageData.value, overageStatus: res?.overageStatus || (next ? 'ENABLED' : 'DISABLED') }
    emit('saved')
  } catch (err) {
    toast(err.message || t('accounts.overageSwitchFailed'), 'error')
  } finally {
    overageBusy.value = false
  }
}

async function refreshOverage() {
  overageBusy.value = true
  try {
    const res = await api.overage(id.value)
    if (res?.success === false) throw new Error(res.error || 'failed')
    overage.value = { ...overageData.value, ...res }
    emit('saved')
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    overageBusy.value = false
  }
}

// ── field saves ────────────────────────────────────────────────────────────
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const HEX32_RE = /^[0-9a-f]{32}$/i
const PROXY_RE = /^(socks5h?|https?):\/\/.+/i

async function save(field, patch) {
  saving.value = field
  try {
    await api.updateAccount(id.value, patch)
    toast(t('detail.saved'), 'success')
    emit('saved')
  } catch (err) {
    toast(err.message || t('detail.saveFailed'), 'error')
  } finally {
    saving.value = ''
  }
}

function saveMachineId() {
  const value = machineId.value.trim()
  if (value && !UUID_RE.test(value) && !HEX32_RE.test(value)) {
    machineIdError.value = t('detail.machineIdError')
    return
  }
  machineIdError.value = ''
  save('machineId', { machineId: value })
}

async function generateMachineId() {
  try {
    const res = await api.generateMachineId()
    if (res?.machineId) {
      machineId.value = res.machineId
      machineIdError.value = ''
    }
  } catch {
    toast(t('detail.generateFailed'), 'error')
  }
}

function saveWeight() {
  const value = Math.max(0, Math.min(10, Math.round(Number(weight.value) || 0)))
  weight.value = value
  save('weight', { weight: value })
}

/**
 * 采购成本（美元 / 单 Key）。
 *
 * 成本可改而不是只在导入时写一次：供应商会调价，历史账号也可能需要补录成本。
 * 负数夹到 0 —— 负成本会让利润凭空变高，属于明显的输入错误。
 */
function saveCost() {
  const value = Math.max(0, Number(cost.value) || 0)
  cost.value = value
  save('cost', { cost: value })
}

function saveProxy() {
  const value = proxyURL.value.trim()
  if (value && !PROXY_RE.test(value)) {
    proxyError.value = t('detail.proxyFormatError')
    return
  }
  proxyError.value = ''
  save('proxyURL', { proxyURL: value })
}

function saveTags() {
  const list = tags.value
    .split(/[,\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  save('tags', { tags: list })
}

// ── subscription / stats ───────────────────────────────────────────────────
const subscriptionRows = computed(() => {
  const d = acc.value
  const rows = [
    { label: t('detail.subscriptionType'), value: d.subscriptionTitle || d.subscriptionType || '—' },
    { label: t('detail.tokenExpiry'), value: formatDateTime(d.expiresAt) },
    { label: t('detail.resetDate'), value: d.nextResetDate || '—' },
  ]
  if (Number(d.trialUsageLimit) > 0) {
    rows.push(
      { label: t('detail.trialStatus'), value: d.trialStatus || '—' },
      { label: t('detail.trialExpiry'), value: formatDateTime(d.trialExpiresAt) },
    )
  }
  return rows
})

const statRows = computed(() => {
  const d = acc.value
  const rows = [
    { label: t('detail.requestCount'), value: Number(d.requestCount || 0).toLocaleString('en-US') },
    { label: t('detail.errorCount'), value: Number(d.errorCount || 0).toLocaleString('en-US') },
    { label: t('detail.totalTokens'), value: formatNum(d.totalTokens) },
    { label: t('detail.totalCredits'), value: toFixed(d.totalCredits, 2) },
  ]
  const c = props.cache
  if (c) {
    rows.push(
      { label: t('detail.cacheRead'), value: formatNum(c.cacheReadInputTokens) },
      { label: t('detail.cacheCreation'), value: formatNum(c.cacheCreationInputTokens) },
      {
        label: t('detail.cacheHitRate'),
        value: `${toFixed(Number(c.ratios?.cacheHitRatio || 0) * 100, 1)}%`,
      },
    )
  }
  return rows
})

// ── models ─────────────────────────────────────────────────────────────────
async function loadModels() {
  modelsLoading.value = true
  try {
    const res = await api.accountModels(id.value)
    if (res?.success === false) throw new Error(res.error || 'failed')
    models.value = Array.isArray(res?.models) ? res.models : []
    modelsLoaded.value = true
  } catch (err) {
    toast(err.message || t('detail.loadFailed'), 'error')
  } finally {
    modelsLoading.value = false
  }
}

async function refreshModelCache() {
  modelsLoading.value = true
  try {
    const res = await api.refreshAccountModels(id.value)
    if (res?.success === false) throw new Error(res.error || 'failed')
    await loadModels()
  } catch (err) {
    toast(err.message || t('detail.loadFailed'), 'error')
  } finally {
    modelsLoading.value = false
  }
}
</script>

<template>
  <BaseModal :open="open" :title="t('detail.title')" size="lg" @close="emit('close')">
    <div v-if="account" class="space-y-lg">
      <!-- basic -->
      <section>
        <h3 class="mb-md text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
          {{ t('detail.basicInfo') }}
        </h3>
        <dl class="grid gap-3 sm:grid-cols-2">
          <div v-for="row in basicRows" :key="row.label" class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ row.label }}</dt>
            <dd class="mt-0.5 truncate text-body-sm text-txt" :title="String(row.value)">
              {{ row.value }}
            </dd>
          </div>
        </dl>
      </section>

      <!-- machine id -->
      <section class="border-t border-divider pt-lg">
        <BaseField :label="t('detail.machineId')" :error="machineIdError">
          <template #default="{ id: fid, describedBy }">
            <div class="flex flex-wrap gap-sm">
              <input
                :id="fid"
                v-model="machineId"
                type="text"
                class="field min-w-[220px] flex-1 font-mono text-body-sm"
                :class="machineIdError && 'field-error'"
                :aria-describedby="describedBy"
              />
              <BaseButton variant="glass" size="sm" @click="generateMachineId">
                {{ t('detail.generate') }}
              </BaseButton>
              <BaseButton
                variant="primary"
                size="sm"
                :loading="saving === 'machineId'"
                @click="saveMachineId"
              >
                {{ t('detail.save') }}
              </BaseButton>
            </div>
          </template>
        </BaseField>
      </section>

      <!-- weight -->
      <section class="border-t border-divider pt-lg">
        <BaseField :label="t('detail.weight')" :hint="t('detail.weightHint')">
          <template #default="{ id: fid, describedBy }">
            <div class="flex gap-sm">
              <input
                :id="fid"
                v-model.number="weight"
                type="number"
                min="0"
                max="10"
                class="field w-28"
                :aria-describedby="describedBy"
              />
              <BaseButton
                variant="primary"
                size="sm"
                :loading="saving === 'weight'"
                @click="saveWeight"
              >
                {{ t('detail.save') }}
              </BaseButton>
            </div>
          </template>
        </BaseField>
      </section>

      <!-- cost: 采购成本，绑定到这个 Key 本身。
           可编辑而不是只在导入时写一次：供应商会调价，历史账号也可能需要补录
           成本（例如手工导入的号）。改这里只影响该账号的利润，不动其他账号。 -->
      <section class="border-t border-divider pt-lg">
        <BaseField :label="t('detail.cost')" :hint="t('detail.costHint')">
          <template #default="{ id: fid, describedBy }">
            <div class="flex gap-sm">
              <input
                :id="fid"
                v-model.number="cost"
                type="number"
                min="0"
                step="0.01"
                class="field tnum w-32"
                :aria-describedby="describedBy"
              />
              <BaseButton
                variant="primary"
                size="sm"
                :loading="saving === 'cost'"
                @click="saveCost"
              >
                {{ t('detail.save') }}
              </BaseButton>
            </div>
          </template>
        </BaseField>

        <!-- 利润明细：收入与成本并列，让「这个数字怎么来的」一眼可见 -->
        <dl class="mt-md grid grid-cols-3 gap-3">
          <div class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ t('detail.revenue') }}</dt>
            <dd class="tnum mt-0.5 truncate text-body-sm text-txt">
              {{ formatUsd(profit.revenue) }}
            </dd>
          </div>
          <div class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ t('detail.cost') }}</dt>
            <dd class="tnum mt-0.5 truncate text-body-sm text-txt">
              {{ formatUsd(profit.cost) }}
            </dd>
          </div>
          <div class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ t('detail.profit') }}</dt>
            <dd
              class="tnum mt-0.5 truncate text-body-sm"
              :class="
                { success: 'text-success', error: 'text-error' }[profitTone(profit)] || 'text-txt'
              "
            >
              {{ profit.hasData ? formatUsd(profit.profit) : '—' }}
            </dd>
          </div>
        </dl>
      </section>

      <!-- overage -->
      <section class="border-t border-divider pt-lg">
        <div class="mb-md flex flex-wrap items-center justify-between gap-sm">
          <h3 class="text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
            {{ t('detail.overage') }}
          </h3>
          <BaseButton
            variant="glass"
            size="xs"
            :loading="overageBusy"
            @click="refreshOverage"
          >
            {{ t('detail.overageRefresh') }}
          </BaseButton>
        </div>

        <BaseSwitch
          :model-value="overageEnabled"
          :disabled="overageLocked || overageBusy"
          :label="t('detail.overage')"
          :hint="overageLocked ? t('detail.overageNotCapable') : t('detail.overageHint')"
          :state-text="overageStateText"
          @update:model-value="toggleOverage"
        />

        <dl class="mt-md grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="row in overageRows" :key="row.label" class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ row.label }}</dt>
            <dd class="tnum mt-0.5 truncate text-body-sm text-txt">{{ row.value }}</dd>
          </div>
        </dl>
      </section>

      <!-- proxy -->
      <section class="border-t border-divider pt-lg">
        <BaseField
          :label="t('detail.proxyURL')"
          :hint="t('detail.proxyHint')"
          :error="proxyError"
        >
          <template #default="{ id: fid, describedBy }">
            <div class="flex flex-wrap gap-sm">
              <input
                :id="fid"
                v-model="proxyURL"
                type="text"
                placeholder="socks5://127.0.0.1:1080"
                class="field min-w-[220px] flex-1 font-mono text-body-sm"
                :class="proxyError && 'field-error'"
                :aria-describedby="describedBy"
              />
              <BaseButton
                variant="primary"
                size="sm"
                :loading="saving === 'proxyURL'"
                @click="saveProxy"
              >
                {{ t('detail.save') }}
              </BaseButton>
            </div>
          </template>
        </BaseField>
      </section>

      <!-- tags -->
      <section class="border-t border-divider pt-lg">
        <BaseField :label="t('detail.tags')" :hint="t('detail.tagsHint')">
          <template #default="{ id: fid, describedBy }">
            <div class="flex flex-wrap gap-sm">
              <input
                :id="fid"
                v-model="tags"
                type="text"
                class="field min-w-[220px] flex-1"
                :aria-describedby="describedBy"
              />
              <BaseButton
                variant="primary"
                size="sm"
                :loading="saving === 'tags'"
                @click="saveTags"
              >
                {{ t('detail.save') }}
              </BaseButton>
            </div>
          </template>
        </BaseField>
      </section>

      <!-- subscription -->
      <section class="border-t border-divider pt-lg">
        <h3 class="mb-md text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
          {{ t('detail.subscription') }}
        </h3>
        <UsageMeter
          v-if="Number(account.usageLimit) > 0"
          :label="t('detail.mainQuota')"
          :percent="Number(account.usagePercent || 0) * 100"
          :value-text="`${toFixed(account.usageCurrent, 1)} / ${toFixed(account.usageLimit, 0)}`"
          class="mb-md"
        />
        <UsageMeter
          v-if="Number(account.trialUsageLimit) > 0"
          :label="t('detail.trialQuota')"
          :percent="Number(account.trialUsagePercent || 0) * 100"
          :value-text="`${toFixed(account.trialUsageCurrent, 1)} / ${toFixed(account.trialUsageLimit, 0)}`"
          class="mb-md"
        />
        <dl class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="row in subscriptionRows" :key="row.label" class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ row.label }}</dt>
            <dd class="mt-0.5 truncate text-body-sm text-txt">{{ row.value }}</dd>
          </div>
        </dl>
      </section>

      <!-- statistics -->
      <section class="border-t border-divider pt-lg">
        <h3 class="mb-md text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
          {{ t('detail.statistics') }}
        </h3>
        <dl class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div v-for="row in statRows" :key="row.label" class="min-w-0">
            <dt class="text-caption-sm text-txt-tertiary">{{ row.label }}</dt>
            <dd class="tnum mt-0.5 truncate text-body-sm text-txt">{{ row.value }}</dd>
          </div>
        </dl>
      </section>

      <!-- models -->
      <section class="border-t border-divider pt-lg">
        <div class="mb-md flex flex-wrap items-center justify-between gap-sm">
          <h3 class="text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
            {{ t('detail.models') }}
          </h3>
          <div class="flex gap-sm">
            <BaseButton
              variant="glass"
              size="xs"
              :loading="modelsLoading"
              @click="loadModels"
            >
              {{ t('detail.loadModels') }}
            </BaseButton>
            <BaseButton
              variant="glass"
              size="xs"
              :loading="modelsLoading"
              @click="refreshModelCache"
            >
              {{ t('detail.refreshModelCache') }}
            </BaseButton>
          </div>
        </div>

        <div v-if="modelsLoading" class="space-y-2">
          <div v-for="i in 3" :key="i" class="skeleton h-10" />
        </div>
        <EmptyState
          v-else-if="modelsLoaded && !models.length"
          compact
          :message="t('detail.noModels')"
        />
        <ul v-else-if="models.length" class="space-y-2">
          <li
            v-for="m in models"
            :key="m.modelId"
            class="rounded-[10px] bg-surface-input px-3 py-2"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <code class="font-mono text-body-sm text-txt">{{ m.modelId }}</code>
              <span class="text-caption text-txt-tertiary">
                {{ t('detail.creditMultiplier', m.rateMultiplier ?? 1) }}
              </span>
            </div>
            <p v-if="m.description" class="mt-1 text-caption text-txt-tertiary">
              {{ m.description }}
            </p>
          </li>
        </ul>
      </section>
    </div>

    <template #footer>
      <BaseButton variant="glass" size="sm" @click="emit('close')">
        {{ t('common.close') }}
      </BaseButton>
    </template>
  </BaseModal>
</template>

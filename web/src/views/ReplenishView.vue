<script setup>
// Replenish: per-supplier connection settings, shared purchasing policy, and a
// manual run trigger.
//
// Two contracts from the Go side drive the shape of this form:
//   1. `apiKey` is never echoed back. Sending a blank value means "keep the
//      stored key", so the field starts empty and is only included when typed.
//   2. `POST /replenish` is a partial merge over `{suppliers:{...}, ...policy}`.
import { computed, onMounted, ref } from 'vue'
import {
  PhArrowClockwise,
  PhArrowsClockwise,
  PhCheckCircle,
  PhFloppyDisk,
  PhLink,
  PhPlugsConnected,
  PhWarningCircle,
} from '@phosphor-icons/vue'
import { api } from '@/lib/api'
import { useI18n } from '@/lib/i18n'
import { toast } from '@/lib/toast'
import { confirm } from '@/lib/confirm'
import { formatLocale, formatNumber } from '@/lib/format'
import SectionCard from '@/components/ui/SectionCard.vue'
import GlassPanel from '@/components/ui/GlassPanel.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import BaseSwitch from '@/components/ui/BaseSwitch.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import BaseTabs from '@/components/ui/BaseTabs.vue'

const { t } = useI18n()

const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
/** 每家一个进行中标记：一家在补号时不应把其他家的按钮也置灰。 */
const runningProviders = ref(new Set())
const runningAll = ref(false)
/** 「全部补号」那一个输入框的数量。各家自己的数量存在 supplier.manualCountDraft 上。 */
const manualCount = ref(5)

/** Server view of each supplier, plus local edit state. */
const suppliers = ref([])
const policy = ref({
  region: '',
  enabled: false,
  minPoolSize: 0,
  batchCount: 1,
  intervalSeconds: 300,
  allDeadReplenish: false,
  allDeadCount: 0,
  publicBaseUrl: '',
})
const runtime = ref({ lastRunAt: 0, lastError: '', lastResult: '' })
const credentials = ref({ total: 0, enabled: 0, allDisabled: false })
/** provider -> latest connectivity probe result. */
const testResults = ref({})

/** 当前选中的供应商 tab。四家全展开时页面要滚很久，改为一次只看一家。 */
const activeProvider = ref('')

// Display names are cosmetic and never sent back; the wire identifier stays raw.
const DISPLAY_NAMES = {
  kiross: 'kiro.ss',
  kiroappio: 'kiroapp.io',
  kiroappcc: 'kiroapp.cc',
  kiroceo: 'kiro.ceo',
}
const displayName = (provider) => DISPLAY_NAMES[provider] || provider

function zoneLabel(zone, region) {
  const key = `replenish.zone.${zone}`
  const label = t(key)
  const name = label === key ? zone : label
  return region ? `${name} · ${region}` : name
}

async function load() {
  loading.value = true
  try {
    const res = await api.replenish()
    suppliers.value = (res?.suppliers || []).map((s) => ({
      ...s,
      // Local drafts. apiKeyDraft starts blank — see the contract note above.
      enabledDraft: !!s.enabled,
      baseUrlDraft: s.baseUrl || '',
      apiKeyDraft: '',
      zoneDraft: s.zone || '',
      webhookCountDraft: Number(s.webhookCount ?? 0),
      keyPriceDraft: Number(s.keyPrice ?? 0),
      // 手动补号数量按家独立：各家单价与库存不同，共用一个数字没有意义。
      manualCountDraft: Number(s.webhookCount) > 0 ? Number(s.webhookCount) : 5,
    }))
    // 保持当前选中的 tab；首次加载或该家消失时回落到第一家。
    const names = suppliers.value.map((s) => s.provider)
    if (!names.includes(activeProvider.value)) activeProvider.value = names[0] || ''
    policy.value = {
      region: res?.region || '',
      enabled: !!res?.enabled,
      minPoolSize: Number(res?.minPoolSize ?? 0),
      batchCount: Number(res?.batchCount ?? 1),
      intervalSeconds: Number(res?.intervalSeconds ?? 300),
      allDeadReplenish: !!res?.allDeadReplenish,
      allDeadCount: Number(res?.allDeadCount ?? 0),
      publicBaseUrl: res?.publicBaseUrl || '',
    }
    runtime.value = {
      lastRunAt: Number(res?.lastRunAt ?? 0),
      lastError: res?.lastError || '',
      lastResult: res?.lastResult || '',
    }
    credentials.value = {
      total: Number(res?.credentialsTotal ?? 0),
      enabled: Number(res?.credentialsEnabled ?? 0),
      allDisabled: !!res?.credentialsAllDisabled,
    }
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

/** Builds the partial-merge payload; omits blank API keys so stored keys survive. */
function buildPayload() {
  const supplierPatch = {}
  for (const s of suppliers.value) {
    const patch = {
      enabled: s.enabledDraft,
      baseUrl: s.baseUrlDraft.trim(),
      webhookCount: Math.max(0, Number(s.webhookCountDraft) || 0),
      // 单 Key 采购成本。0 是合法值（免费号/自有号），因此不能用 `|| 默认值`
      // 的写法——那会把用户明确设置的 0 悄悄改回旧值。
      keyPrice: Math.max(0, Number(s.keyPriceDraft) || 0),
    }
    const key = s.apiKeyDraft.trim()
    if (key) patch.apiKey = key
    if (s.supportsZone && s.zoneDraft) patch.zone = s.zoneDraft
    supplierPatch[s.provider] = patch
  }
  return {
    suppliers: supplierPatch,
    region: policy.value.region.trim(),
    enabled: policy.value.enabled,
    minPoolSize: Math.max(0, Number(policy.value.minPoolSize) || 0),
    batchCount: Math.max(1, Number(policy.value.batchCount) || 1),
    intervalSeconds: Math.max(60, Number(policy.value.intervalSeconds) || 300),
    allDeadReplenish: policy.value.allDeadReplenish,
    allDeadCount: Math.max(0, Number(policy.value.allDeadCount) || 0),
    publicBaseUrl: policy.value.publicBaseUrl.trim(),
  }
}

async function save({ silent = false } = {}) {
  saving.value = true
  try {
    await api.saveReplenish(buildPayload())
    if (!silent) toast(t('replenish.saved'), 'success')
    await load()
    return true
  } catch (err) {
    toast(err.message || t('common.saveFailed'), 'error')
    return false
  } finally {
    saving.value = false
  }
}

/**
 * Connectivity probe. Saves first so the server tests the credentials currently
 * shown in the form rather than a stale stored set.
 */
async function runTest() {
  testing.value = true
  const dismiss = toast(t('replenish.test'), 'info', { duration: 0 })
  try {
    if (!(await save({ silent: true }))) return
    const res = await api.testReplenish()
    const map = {}
    for (const s of res?.suppliers || []) map[s.provider] = s
    testResults.value = map

    if (res?.success === false && res?.error) {
      toast(res.error, 'error')
      return
    }
    for (const s of res?.suppliers || []) {
      const name = displayName(s.provider)
      if (!s.ok) {
        toast(`${name}: ${s.error || t('replenish.testFailed')}`, 'error')
        continue
      }
      toast(`${name}: ${summarizeProbe(s)}`, 'success')
    }
  } catch (err) {
    toast(err.message || t('replenish.testFailed'), 'error')
  } finally {
    testing.value = false
    dismiss()
  }
}

/** Condenses a probe result into one line; suppliers expose different fields. */
function summarizeProbe(s) {
  const parts = []
  if (s.remaining != null) parts.push(`${t('replenish.balance')} ${formatNumber(s.remaining)}`)
  if (s.stock != null) parts.push(`${t('replenish.stock')} ${formatNumber(s.stock)}`)
  if (s.priceMin != null || s.priceMax != null) {
    const min = formatNumber(s.priceMin ?? s.priceMax)
    const max = formatNumber(s.priceMax ?? s.priceMin)
    parts.push(`${t('replenish.price')} ${min === max ? min : `${min}–${max}`}`)
  }
  return parts.length ? parts.join(' · ') : t('replenish.testOkBare')
}

/**
 * 手动补号。
 *
 * provider 为空表示「所有启用的供应商各买一批」（保留原有的全局按钮）；
 * 非空则只动那一家 —— 每张供应商卡片里有自己的数量输入与按钮，点谁补谁。
 *
 * 单家与全部走同一个后端端点，区别只在 body 里带不带 provider。
 */
async function runNow(provider = '') {
  const single = Boolean(provider)
  if (single) runningProviders.value = new Set(runningProviders.value).add(provider)
  else runningAll.value = true

  const label = single ? `${displayName(provider)} · ${t('replenish.runNow')}` : t('replenish.runNow')
  const dismiss = toast(label, 'info', { duration: 0 })
  try {
    const count = single ? manualCountFor(provider) : Math.max(0, Number(manualCount.value) || 0)
    const payload = {}
    if (count > 0) payload.count = count
    if (single) payload.provider = provider

    const res = await api.runReplenish(payload)
    if (res?.success === false) {
      toast(res.error || t('replenish.runFailed'), 'error')
    } else {
      const summary = t('replenish.runOk', res?.purchased ?? 0, res?.imported ?? 0, res?.skipped ?? 0)
      toast(single ? `${displayName(provider)}: ${summary}` : summary, 'success')
    }
    // 全量补号时一家失败其余仍可能成功，逐家提示；单家失败会走 catch。
    for (const s of res?.suppliers || []) {
      if (s.error) toast(`${displayName(s.provider)}: ${s.error}`, 'warning')
    }
    await load()
  } catch (err) {
    const msg = err.message || t('replenish.runFailed')
    toast(single ? `${displayName(provider)}: ${msg}` : msg, 'error')
  } finally {
    if (single) {
      const next = new Set(runningProviders.value)
      next.delete(provider)
      runningProviders.value = next
    } else {
      runningAll.value = false
    }
    dismiss()
  }
}

/** 该家卡片里输入的数量；未填或非法时回落到共享策略的 batchCount。 */
function manualCountFor(provider) {
  const s = suppliers.value.find((x) => x.provider === provider)
  const n = Math.max(0, Number(s?.manualCountDraft) || 0)
  return n > 0 ? n : 0
}

/** 该家是否正在补号（用于按钮 loading 态）。 */
const isRunning = (provider) => runningProviders.value.has(provider)

async function registerWebhook(provider) {
  try {
    if (!(await save({ silent: true }))) return
    const res = await api.registerReplenishWebhook(provider)
    if (res?.success === false) {
      toast(res.error || t('replenish.registerFailed'), 'error')
      return
    }
    for (const s of res?.suppliers || []) {
      if (s.ok) toast(`${displayName(s.provider)}: ${t('replenish.registerOk')}`, 'success')
      else if (s.error) toast(`${displayName(s.provider)}: ${s.error}`, 'error')
    }
    await load()
  } catch (err) {
    toast(err.message || t('replenish.registerFailed'), 'error')
  }
}

async function resetSecret(provider) {
  if (!(await confirm({ message: t('replenish.resetConfirm'), danger: true }))) return
  try {
    const res = await api.resetReplenishSecret(provider)
    if (res?.success === false) {
      toast(res.error || t('common.failed'), 'error')
      return
    }
    toast(t('replenish.resetOk'), 'success')
    await load()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  }
}

/**
 * 供应商 tab 列表。
 *
 * 四家全部平铺时纵向占掉 4 张大卡片，滚动才能看完，而实际上同一时刻用户只关心
 * 一家。改成 tab 后每次只渲染一家的表单，页面高度固定。
 *
 * tab 上带状态点：启用为绿、停用为灰，这样不进入某个 tab 也能看出哪几家在跑。
 * 未启用不隐藏该 tab —— 用户需要能进去把它打开。
 */
const supplierTabs = computed(() =>
  suppliers.value.map((s) => ({
    value: s.provider,
    label: displayName(s.provider),
    dot: s.enabledDraft ? 'green' : 'idle',
  })),
)

/** 当前 tab 对应的供应商对象。 */
const activeSupplier = computed(
  () => suppliers.value.find((s) => s.provider === activeProvider.value) || null,
)

const credentialsSummary = computed(() =>
  credentials.value.allDisabled
    ? t('replenish.credentialsAllDisabled')
    : t('replenish.credentialsSummary', credentials.value.enabled, credentials.value.total),
)
</script>

<template>
  <div class="space-y-lg">
    <!-- ═══ 供应商：每家一个 tab ═══
         此前是 xl:grid-cols-2 的卡片网格，四家全部展开约 1200px 高，且每加一家
         就更长。改成标签页后同一时刻只渲染一家的表单：屏幕上信息量固定，切换
         也不会把其余几家的输入框一起挂载。 -->
    <SectionCard
      :title="t('replenish.supplierTitle')"
      :hint="t('replenish.parallelIntro')"
      :icon="PhPlugsConnected"
    >
      <template #actions>
        <BaseButton variant="glass" size="sm" :loading="testing" @click="runTest">
          <template #icon><PhPlugsConnected :size="16" /></template>
          {{ t('replenish.test') }}
        </BaseButton>
        <BaseButton variant="primary" size="sm" :loading="saving" @click="save()">
          <template #icon><PhFloppyDisk :size="16" /></template>
          {{ t('replenish.save') }}
        </BaseButton>
      </template>

      <div v-if="loading" class="space-y-md">
        <div class="skeleton h-10 rounded-full" />
        <div class="skeleton h-72" />
      </div>

      <EmptyState v-else-if="!suppliers.length" :message="t('replenish.noProviders')" />

      <BaseTabs
        v-else
        v-model="activeProvider"
        :items="supplierTabs"
        :aria-label="t('replenish.supplierTitle')"
      >
        <GlassPanel v-if="activeSupplier" material="thin" class="p-lg">
          <!-- 头部：名称 + 启用开关 -->
          <div class="flex items-start justify-between gap-md">
            <div class="min-w-0">
              <h3 class="font-brand text-title-sm text-txt">
                {{ displayName(activeSupplier.provider) }}
              </h3>
              <div class="mt-1.5 flex flex-wrap items-center gap-xs">
                <StatusBadge
                  v-if="testResults[activeSupplier.provider]"
                  :tone="testResults[activeSupplier.provider].ok ? 'green' : 'red'"
                  :label="
                    testResults[activeSupplier.provider].ok
                      ? summarizeProbe(testResults[activeSupplier.provider])
                      : testResults[activeSupplier.provider].error || t('replenish.testFailed')
                  "
                  dot
                />
                <StatusBadge
                  v-if="activeSupplier.hasApiKey"
                  tone="gray"
                  :label="activeSupplier.apiKeyMasked || '••••'"
                />
              </div>
            </div>
            <BaseSwitch
              v-model="activeSupplier.enabledDraft"
              :aria-label="t('replenish.enabled')"
              :state-text="
                activeSupplier.enabledDraft ? t('replenish.stateOn') : t('replenish.stateOff')
              "
            />
          </div>

          <!-- ═══ 该家的手动补号 ═══
               放在卡片内而非独立区块：全局的「立即补号」会动每一家的余额，
               想只补一家此前得先把其余几家的开关关掉、补完再打开。这里点谁补谁。 -->
          <div
            class="mt-md flex flex-wrap items-end gap-sm rounded-[10px] border border-line-active bg-accent-soft p-3"
          >
            <div class="w-24">
              <BaseField :label="t('replenish.manualCountShort')">
                <template #default="{ id }">
                  <input
                    :id="id"
                    v-model.number="activeSupplier.manualCountDraft"
                    type="number"
                    min="1"
                    class="field tnum h-9"
                    :title="t('replenish.manualCountHint')"
                  />
                </template>
              </BaseField>
            </div>
            <BaseButton
              variant="primary"
              size="sm"
              :loading="isRunning(activeSupplier.provider)"
              :disabled="!activeSupplier.enabled"
              :title="
                activeSupplier.enabled
                  ? t('replenish.runProviderHint', displayName(activeSupplier.provider))
                  : t('replenish.runProviderDisabled')
              "
              @click="runNow(activeSupplier.provider)"
            >
              <template #icon><PhArrowClockwise :size="15" /></template>
              {{ t('replenish.runProvider') }}
            </BaseButton>
            <!-- 只有已保存为启用的家才能补：未保存的草稿状态后端还不认 -->
            <p v-if="!activeSupplier.enabled" class="text-caption text-txt-tertiary">
              {{ t('replenish.runProviderDisabled') }}
            </p>
          </div>

          <div class="mt-md space-y-md">
            <BaseField :label="t('replenish.baseUrl')" :hint="t('replenish.baseUrlHint')">
              <template #default="{ id, describedBy }">
                <input
                  :id="id"
                  v-model="activeSupplier.baseUrlDraft"
                  class="field font-mono text-body-sm"
                  :placeholder="activeSupplier.baseUrlHint"
                  :aria-describedby="describedBy"
                />
              </template>
            </BaseField>

            <BaseField :label="t('replenish.apiKey')" :hint="t('replenish.apiKeyHint')">
              <template #default="{ id, describedBy }">
                <input
                  :id="id"
                  v-model="activeSupplier.apiKeyDraft"
                  type="password"
                  autocomplete="off"
                  class="field font-mono text-body-sm"
                  :placeholder="
                    activeSupplier.hasApiKey
                      ? activeSupplier.apiKeyMasked || '••••••••'
                      : t('replenish.apiKeyPlaceholder')
                  "
                  :aria-describedby="describedBy"
                />
              </template>
            </BaseField>

            <div class="grid gap-md sm:grid-cols-2">
              <BaseField
                v-if="activeSupplier.supportsZone && activeSupplier.zones?.length"
                :label="t('replenish.zone')"
                :hint="t('replenish.zoneHint')"
              >
                <BaseSelect
                  v-model="activeSupplier.zoneDraft"
                  :aria-label="t('replenish.zone')"
                  :options="
                    activeSupplier.zones.map((z) => ({
                      value: z.zone,
                      label: zoneLabel(z.zone, z.region),
                    }))
                  "
                />
              </BaseField>

              <BaseField
                :label="t('replenish.webhookCountShort')"
                :hint="t('replenish.webhookCountHint')"
              >
                <template #default="{ id, describedBy }">
                  <input
                    :id="id"
                    v-model.number="activeSupplier.webhookCountDraft"
                    type="number"
                    min="0"
                    class="field tnum"
                    :aria-describedby="describedBy"
                  />
                </template>
              </BaseField>

              <!-- 单 Key 采购单价：利润核算的成本侧来源。
                   该值在补号导入时被写入每个账号（Account.Cost），此后调价不影响
                   已导入的号——成本必须绑定到 Key 本身，否则历史利润会随调价而变。 -->
              <BaseField :label="t('replenish.keyPrice')" :hint="t('replenish.keyPriceHint')">
                <template #default="{ id, describedBy }">
                  <input
                    :id="id"
                    v-model.number="activeSupplier.keyPriceDraft"
                    type="number"
                    min="0"
                    step="0.01"
                    class="field tnum"
                    :aria-describedby="describedBy"
                  />
                </template>
              </BaseField>
            </div>

            <!-- 回调地址：只读可复制 -->
            <BaseField :label="t('replenish.callbackUrl')" :hint="t('replenish.callbackUrlHint')">
              <template #default="{ id, describedBy }">
                <div class="flex items-center gap-xs">
                  <input
                    :id="id"
                    :value="activeSupplier.callbackUrl || ''"
                    readonly
                    class="field font-mono text-caption"
                    :placeholder="t('replenish.callbackEmptyShort')"
                    :aria-describedby="describedBy"
                  />
                  <CopyButton
                    v-if="activeSupplier.callbackUrl"
                    :value="activeSupplier.callbackUrl"
                    variant="glass"
                    :aria-label="t('replenish.copy')"
                  />
                </div>
              </template>
            </BaseField>

            <div class="flex flex-wrap items-center gap-xs">
              <BaseButton
                v-if="activeSupplier.supportsWebhookAutoRegister"
                variant="glass"
                size="xs"
                @click="registerWebhook(activeSupplier.provider)"
              >
                <template #icon><PhLink :size="15" /></template>
                {{ t('replenish.register') }}
              </BaseButton>
              <StatusBadge
                v-else
                tone="gray"
                :label="t('replenish.manualBadge')"
                :title="t('replenish.manualRegisterHint')"
              />
              <BaseButton
                variant="ghost"
                size="xs"
                @click="resetSecret(activeSupplier.provider)"
              >
                <template #icon><PhArrowsClockwise :size="15" /></template>
                {{ t('replenish.resetSecret') }}
              </BaseButton>
            </div>

            <p class="border-t border-divider pt-md text-caption text-txt-tertiary">
              <template v-if="activeSupplier.lastWebhookAt">
                {{ t('replenish.webhookLast') }} {{ formatLocale(activeSupplier.lastWebhookAt) }}
                <template v-if="activeSupplier.lastWebhookMsg">
                  <br />{{ activeSupplier.lastWebhookMsg }}
                </template>
              </template>
              <template v-else>{{ t('replenish.webhookNever') }}</template>
            </p>
          </div>
        </GlassPanel>
      </BaseTabs>
    </SectionCard>

    <!-- ═══ shared policy ═══ -->
    <SectionCard :title="t('replenish.sharedTitle')">
      <template #actions>
        <BaseButton variant="primary" size="sm" :loading="saving" @click="save()">
          <template #icon><PhFloppyDisk :size="16" /></template>
          {{ t('replenish.save') }}
        </BaseButton>
      </template>

      <div class="grid gap-md lg:grid-cols-2">
        <BaseField
          :label="t('replenish.publicBaseUrl')"
          :hint="t('replenish.publicBaseUrlHint')"
        >
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model="policy.publicBaseUrl"
              class="field font-mono text-body-sm"
              placeholder="https://my-proxy.example.com"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>

        <BaseField :label="t('replenish.region')" :hint="t('replenish.regionHint')">
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model="policy.region"
              class="field font-mono text-body-sm"
              placeholder="us-east-1"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>

        <BaseField :label="t('replenish.minPoolSize')" :hint="t('replenish.minPoolSizeHint')">
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model.number="policy.minPoolSize"
              type="number"
              min="0"
              class="field tnum"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>

        <BaseField :label="t('replenish.batchCount')" :hint="t('replenish.batchCountHint')">
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model.number="policy.batchCount"
              type="number"
              min="1"
              class="field tnum"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>

        <BaseField :label="t('replenish.interval')" :hint="t('replenish.intervalHint')">
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model.number="policy.intervalSeconds"
              type="number"
              min="60"
              class="field tnum"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>

        <BaseField :label="t('replenish.allDeadCount')" :hint="t('replenish.allDeadCountHint')">
          <template #default="{ id, describedBy }">
            <input
              :id="id"
              v-model.number="policy.allDeadCount"
              type="number"
              min="0"
              class="field tnum"
              :aria-describedby="describedBy"
            />
          </template>
        </BaseField>
      </div>

      <div class="mt-lg space-y-md border-t border-divider pt-lg">
        <BaseSwitch
          v-model="policy.enabled"
          :label="t('replenish.enabled')"
          :hint="t('replenish.enabledHint')"
        />
        <BaseSwitch
          v-model="policy.allDeadReplenish"
          :label="t('replenish.allDead')"
          :hint="t('replenish.allDeadHint')"
        />
        <GlassPanel material="clear" radius="md" class="flex items-center gap-2 px-md py-sm">
          <component
            :is="credentials.allDisabled ? PhWarningCircle : PhCheckCircle"
            :size="18"
            :class="credentials.allDisabled ? 'text-warning' : 'text-accent'"
          />
          <span class="text-body-sm text-txt-secondary">{{ credentialsSummary }}</span>
        </GlassPanel>
      </div>
    </SectionCard>

    <!-- ═══ 全量补号 + 运行态 ═══
         单家补号已移进各自的卡片，这里只保留「所有启用的供应商各买一批」。 -->
    <SectionCard :title="t('replenish.manualTitle')">
      <div class="flex flex-wrap items-end gap-md">
        <div class="w-32">
          <BaseField :label="t('replenish.runAllCount')">
            <template #default="{ id }">
              <input
                :id="id"
                v-model.number="manualCount"
                type="number"
                min="1"
                class="field tnum"
                :title="t('replenish.manualCountHint')"
              />
            </template>
          </BaseField>
        </div>
        <BaseButton variant="glass" size="sm" :loading="runningAll" @click="runNow()">
          <template #icon><PhArrowClockwise :size="16" /></template>
          {{ t('replenish.runAll') }}
        </BaseButton>
        <p class="text-caption text-txt-tertiary">{{ t('replenish.runAllHint') }}</p>
      </div>

      <div class="mt-lg border-t border-divider pt-md">
        <h3 class="text-caption font-medium tracking-wide text-txt-tertiary uppercase">
          {{ t('replenish.lastRunTitle') }}
        </h3>
        <p v-if="!runtime.lastRunAt" class="mt-2 text-body-sm text-txt-tertiary">
          {{ t('replenish.neverRun') }}
        </p>
        <div v-else class="mt-2 space-y-1">
          <p class="text-body-sm text-txt-secondary">
            {{ t('replenish.lastRun') }} {{ formatLocale(runtime.lastRunAt) }}
          </p>
          <p v-if="runtime.lastResult" class="text-body-sm text-accent">{{ runtime.lastResult }}</p>
          <p v-if="runtime.lastError" class="text-body-sm text-error">{{ runtime.lastError }}</p>
        </div>
      </div>
    </SectionCard>
  </div>
</template>

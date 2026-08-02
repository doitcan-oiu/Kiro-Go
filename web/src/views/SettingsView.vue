<script setup>
// Settings: usage, thinking, endpoint, proxy, prompt filter, rate limit,
// webhook, admin password, statistics reset.
//
// Critical contract: `POST /settings` is a *partial merge* on the server — every
// field is a pointer and an absent field stays unchanged. Each card therefore
// sends only its own keys. Sending the whole form would clobber unrelated
// settings with stale values.
import { onMounted, ref } from 'vue'
import {
  PhArrowCounterClockwise,
  PhBroom,
  PhBrain,
  PhChartBar,
  PhFunnel,
  PhGauge,
  PhLock,
  PhPlugsConnected,
  PhPlus,
  PhShieldCheck,
  PhTrash,
  PhWebhooksLogo,
} from '@phosphor-icons/vue'
import { api } from '@/lib/api'
import { useI18n } from '@/lib/i18n'
import { toast } from '@/lib/toast'
import { confirm } from '@/lib/confirm'
import { useDataStore } from '@/stores/data'
import { useSessionStore } from '@/stores/session'
import SectionCard from '@/components/ui/SectionCard.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import BaseSwitch from '@/components/ui/BaseSwitch.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const { t } = useI18n()
const data = useDataStore()
const session = useSessionStore()

const loading = ref(true)

// ── usage ────────────────────────────────────────────────────────────────────
const allowOverUsage = ref(false)
const promptCacheEnabled = ref(true)
const maxPayloadBytes = ref('2000000')
const profitMultiplier = ref('1')
const savingUsage = ref(false)

// The four sizes the legacy panel offered; labels are intentionally literal.
const payloadOptions = [
  { value: '921600', label: '900 KB' },
  { value: '2000000', label: '2 MB' },
  { value: '2100000', label: '2.1 MB' },
  { value: '4000000', label: '4 MB' },
]

// ── api key requirement ──────────────────────────────────────────────────────
const requireApiKey = ref(false)
const savingRequire = ref(false)

// ── thinking ─────────────────────────────────────────────────────────────────
const thinkingSuffix = ref('-thinking')
const openaiFormat = ref('reasoning_content')
const claudeFormat = ref('thinking')
const savingThinking = ref(false)

// ── endpoint ─────────────────────────────────────────────────────────────────
const preferredEndpoint = ref('auto')
const endpointFallback = ref(true)
const savingEndpoint = ref(false)

// ── proxy ────────────────────────────────────────────────────────────────────
const proxyType = ref('none')
const proxyHost = ref('')
const proxyPort = ref('')
const proxyUsername = ref('')
const proxyPassword = ref('')
const savingProxy = ref(false)

// ── prompt filter ────────────────────────────────────────────────────────────
const filterClaudeCode = ref(false)
const filterEnvNoise = ref(false)
const filterStripBoundaries = ref(false)
const rules = ref([])
const savingFilter = ref(false)

// ── rate limit / webhook ─────────────────────────────────────────────────────
const rateLimitRpm = ref('0')
const rateLimitPerKeyRpm = ref('0')
const rateLimitBurstSeconds = ref('0')
const savingRateLimit = ref(false)
const webhookUrl = ref('')
const savingWebhook = ref(false)

// ── password ─────────────────────────────────────────────────────────────────
const newPassword = ref('')
const savingPassword = ref(false)

const resettingStats = ref(false)

function intOrZero(value) {
  const n = parseInt(value, 10)
  return Number.isFinite(n) && n > 0 ? n : 0
}

async function loadAll() {
  loading.value = true
  const results = await Promise.allSettled([
    api.settings(),
    api.thinking(),
    api.endpoint(),
    api.proxy(),
    api.promptFilter(),
  ])
  const [settings, thinking, endpoint, proxy, filter] = results

  if (settings.status === 'fulfilled' && settings.value) {
    const s = settings.value
    requireApiKey.value = !!s.requireApiKey
    allowOverUsage.value = !!s.allowOverUsage
    // Both of these default to ON: an older backend that omits the field must
    // not render as "off". `!== false` preserves that.
    promptCacheEnabled.value = s.promptCacheEnabled !== false
    maxPayloadBytes.value = String(s.maxPayloadBytes || 2000000)
    // 后端保证返回一个正数（未配置时回落到 1），因此这里不需要再兜底。
    profitMultiplier.value = String(s.profitMultiplier ?? 1)
    rateLimitRpm.value = String(s.rateLimitRpm ?? 0)
    rateLimitPerKeyRpm.value = String(s.rateLimitPerKeyRpm ?? 0)
    rateLimitBurstSeconds.value = String(s.rateLimitBurstSeconds ?? 0)
    webhookUrl.value = s.webhookUrl || ''
  }

  if (thinking.status === 'fulfilled' && thinking.value) {
    thinkingSuffix.value = thinking.value.suffix || '-thinking'
    openaiFormat.value = thinking.value.openaiFormat || 'reasoning_content'
    claudeFormat.value = thinking.value.claudeFormat || 'thinking'
  }

  if (endpoint.status === 'fulfilled' && endpoint.value) {
    preferredEndpoint.value = endpoint.value.preferredEndpoint || 'auto'
    endpointFallback.value = endpoint.value.endpointFallback !== false
  }

  if (proxy.status === 'fulfilled' && proxy.value) {
    applyProxyUrl(proxy.value.proxyURL || '')
  }

  if (filter.status === 'fulfilled' && filter.value) {
    filterClaudeCode.value = !!filter.value.filterClaudeCode
    filterEnvNoise.value = !!filter.value.filterEnvNoise
    filterStripBoundaries.value = !!filter.value.filterStripBoundaries
    rules.value = Array.isArray(filter.value.rules) ? filter.value.rules.map((r) => ({ ...r })) : []
  }

  loading.value = false
}

/** Decomposes a stored proxy URL back into the five form fields. */
function applyProxyUrl(url) {
  if (!url) {
    proxyType.value = 'none'
    return
  }
  try {
    const parsed = new URL(url)
    proxyType.value = parsed.protocol.startsWith('socks5') ? 'socks5' : 'http'
    proxyHost.value = parsed.hostname
    proxyPort.value = parsed.port
    proxyUsername.value = decodeURIComponent(parsed.username || '')
    proxyPassword.value = decodeURIComponent(parsed.password || '')
  } catch {
    proxyType.value = 'none'
  }
}

onMounted(loadAll)

async function withSaving(flag, fn, successKey) {
  flag.value = true
  try {
    await fn()
    if (successKey) toast(t(successKey), 'success')
    return true
  } catch (err) {
    toast(err.message || t('common.saveFailed'), 'error')
    return false
  } finally {
    flag.value = false
  }
}

async function saveRequireApiKey() {
  // Turning the requirement on with no enabled key locks every client out.
  if (requireApiKey.value) {
    let enabledKeys = 0
    try {
      const res = await api.apiKeys()
      enabledKeys = (res?.apiKeys || []).filter((k) => k.enabled).length
    } catch {
      /* fall through to the warning */
    }
    if (enabledKeys === 0) {
      const ok = await confirm({
        message: t('apiKeys.requireWithoutEnabledKeyWarning'),
        danger: true,
      })
      if (!ok) return
    }
  }
  await withSaving(
    savingRequire,
    () => api.saveSettings({ requireApiKey: requireApiKey.value }),
    'common.saved',
  )
}

async function saveUsage() {
  await withSaving(
    savingUsage,
    () =>
      api.saveSettings({
        allowOverUsage: allowOverUsage.value,
        maxPayloadBytes: parseInt(maxPayloadBytes.value, 10),
        promptCacheEnabled: promptCacheEnabled.value,
        // 倍率必须为正：0 会让所有收入归零、负数会让利润随用量下降，
        // 两者都不是任何真实计费模型。非法输入回落到 1 而不是原样提交，
        // 让后端的校验只作为第二道防线。
        profitMultiplier: Math.max(0.0001, Number(profitMultiplier.value) || 1),
      }),
    'settings.overUsageSaved',
  )
}

async function saveThinking() {
  await withSaving(
    savingThinking,
    () =>
      api.saveThinking({
        suffix: thinkingSuffix.value,
        openaiFormat: openaiFormat.value,
        claudeFormat: claudeFormat.value,
      }),
    'settings.thinkingSaved',
  )
}

async function saveEndpoint() {
  await withSaving(
    savingEndpoint,
    () =>
      api.saveEndpoint({
        preferredEndpoint: preferredEndpoint.value,
        endpointFallback: endpointFallback.value,
      }),
    'settings.endpointSaved',
  )
}

async function saveProxy() {
  let url = ''
  if (proxyType.value !== 'none') {
    if (!proxyHost.value.trim() || !proxyPort.value) {
      toast(t('settings.proxyHostRequired'), 'warning')
      return
    }
    const auth = proxyUsername.value
      ? `${encodeURIComponent(proxyUsername.value)}:${encodeURIComponent(proxyPassword.value)}@`
      : ''
    url = `${proxyType.value}://${auth}${proxyHost.value.trim()}:${proxyPort.value}`
  }
  await withSaving(savingProxy, () => api.saveProxy(url), 'settings.proxySaved')
}

function addRule(type) {
  rules.value.push({
    id: `rule-${Date.now()}`,
    name: '',
    type,
    match: '',
    replace: '',
    enabled: true,
  })
}

async function savePromptFilter() {
  await withSaving(
    savingFilter,
    () =>
      api.savePromptFilter({
        filterClaudeCode: filterClaudeCode.value,
        filterEnvNoise: filterEnvNoise.value,
        filterStripBoundaries: filterStripBoundaries.value,
        rules: rules.value,
      }),
    'settings.promptFilterSaved',
  )
}

async function saveRateLimit() {
  await withSaving(
    savingRateLimit,
    () =>
      api.saveSettings({
        rateLimitRpm: intOrZero(rateLimitRpm.value),
        rateLimitPerKeyRpm: intOrZero(rateLimitPerKeyRpm.value),
        rateLimitBurstSeconds: intOrZero(rateLimitBurstSeconds.value),
      }),
    'settings.rateLimitSaved',
  )
}

async function saveWebhook() {
  await withSaving(
    savingWebhook,
    () => api.saveSettings({ webhookUrl: webhookUrl.value.trim() }),
    'settings.webhookSaved',
  )
}

async function changePassword() {
  const next = newPassword.value
  if (!next) {
    toast(t('settings.passwordRequired'), 'warning')
    return
  }
  const ok = await withSaving(savingPassword, () => api.saveSettings({ password: next }))
  if (!ok) return
  // The new password must become the active credential immediately, or every
  // subsequent request would 401.
  session.adoptPassword(next)
  newPassword.value = ''
  toast(t('settings.passwordChanged'), 'success')
}

async function resetStats() {
  const ok = await confirm({ message: t('settings.confirmReset'), danger: true })
  if (!ok) return
  resettingStats.value = true
  try {
    await api.resetStats()
    await data.loadStatus()
    toast(t('settings.statsReset'), 'success')
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    resettingStats.value = false
  }
}
</script>

<template>
  <div class="space-y-lg">
    <header>
      <h1 class="font-brand text-title-lg text-txt">{{ t('tabs.settings') }}</h1>
    </header>

    <div v-if="loading" class="space-y-lg">
      <div v-for="i in 4" :key="i" class="skeleton h-40 rounded-2xl" />
    </div>

    <div v-else class="grid gap-lg xl:grid-cols-2">
      <!-- API key requirement -->
      <SectionCard :title="t('settings.apiSettings')" :icon="PhShieldCheck">
        <div class="space-y-md">
          <BaseSwitch
            v-model="requireApiKey"
            :label="t('settings.enableApiKey')"
            :hint="t('apiKeys.requireHint')"
          />
          <BaseButton variant="primary" size="sm" :loading="savingRequire" @click="saveRequireApiKey">
            {{ t('settings.saveApiKey') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- usage -->
      <SectionCard :title="t('settings.usageSettings')" :icon="PhGauge">
        <div class="space-y-md">
          <BaseSwitch
            v-model="allowOverUsage"
            :label="t('settings.allowOverUsage')"
            :hint="t('settings.allowOverUsageHint')"
          />
          <BaseSwitch
            v-model="promptCacheEnabled"
            :label="t('settings.promptCacheEnabled')"
            :hint="t('settings.promptCacheEnabledHint')"
          />
          <BaseField :label="t('settings.maxPayloadBytes')" :hint="t('settings.maxPayloadBytesHint')">
            <BaseSelect
              v-model="maxPayloadBytes"
              :options="payloadOptions"
              :aria-label="t('settings.maxPayloadBytes')"
            />
          </BaseField>
          <BaseField
            :label="t('settings.profitMultiplier')"
            :hint="t('settings.profitMultiplierHint')"
          >
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="profitMultiplier"
                type="number"
                min="0.0001"
                step="0.1"
                class="field tnum sm:w-48"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <BaseButton variant="primary" size="sm" :loading="savingUsage" @click="saveUsage">
            {{ t('settings.saveUsage') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- thinking -->
      <SectionCard :title="t('settings.thinkingSettings')" :icon="PhBrain">
        <div class="space-y-md">
          <BaseField
            :label="t('settings.thinkingSuffix')"
            :hint="t('settings.thinkingSuffixHint')"
          >
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="thinkingSuffix"
                class="field"
                placeholder="-thinking"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <BaseField :label="t('settings.openaiFormat')">
            <BaseSelect
              v-model="openaiFormat"
              :aria-label="t('settings.openaiFormat')"
              :options="[
                { value: 'reasoning_content', label: t('settings.formatReasoningContent') },
                { value: 'thinking', label: t('settings.formatThinkingClaude') },
                { value: 'think', label: t('settings.formatThinkOpenAI') },
              ]"
            />
          </BaseField>
          <BaseField :label="t('settings.claudeFormat')">
            <BaseSelect
              v-model="claudeFormat"
              :aria-label="t('settings.claudeFormat')"
              :options="[
                { value: 'thinking', label: t('settings.formatThinkingClaude') },
                { value: 'think', label: t('settings.formatThinkOpenAI') },
              ]"
            />
          </BaseField>
          <BaseButton variant="primary" size="sm" :loading="savingThinking" @click="saveThinking">
            {{ t('settings.saveThinking') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- endpoint -->
      <SectionCard :title="t('settings.endpointSettings')" :icon="PhPlugsConnected">
        <div class="space-y-md">
          <BaseField
            :label="t('settings.preferredEndpoint')"
            :hint="t('settings.endpointHint')"
          >
            <BaseSelect
              v-model="preferredEndpoint"
              :aria-label="t('settings.preferredEndpoint')"
              :options="[
                { value: 'auto', label: t('settings.endpointAuto') },
                { value: 'kiro', label: t('settings.endpointKiro') },
                { value: 'codewhisperer', label: t('settings.endpointCodeWhisperer') },
                { value: 'amazonq', label: t('settings.endpointAmazonQ') },
              ]"
            />
          </BaseField>
          <BaseSwitch
            v-model="endpointFallback"
            :label="t('settings.endpointFallback')"
            :hint="t('settings.endpointFallbackHint')"
          />
          <BaseButton variant="primary" size="sm" :loading="savingEndpoint" @click="saveEndpoint">
            {{ t('settings.saveEndpoint') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- proxy -->
      <SectionCard :title="t('settings.proxySettings')" :icon="PhPlugsConnected">
        <div class="space-y-md">
          <BaseField :label="t('settings.proxyType')">
            <BaseSelect
              v-model="proxyType"
              :aria-label="t('settings.proxyType')"
              :options="[
                { value: 'none', label: t('settings.proxyNone') },
                { value: 'socks5', label: t('settings.proxySocks5') },
                { value: 'http', label: t('settings.proxyHttp') },
              ]"
            />
          </BaseField>

          <template v-if="proxyType !== 'none'">
            <div class="grid grid-cols-[1fr_120px] gap-sm">
              <BaseField :label="t('settings.proxyHost')">
                <template #default="{ id }">
                  <input :id="id" v-model="proxyHost" class="field" placeholder="127.0.0.1" />
                </template>
              </BaseField>
              <BaseField label="Port">
                <template #default="{ id }">
                  <input
                    :id="id"
                    v-model="proxyPort"
                    type="number"
                    min="1"
                    max="65535"
                    class="field tnum"
                    placeholder="1080"
                  />
                </template>
              </BaseField>
            </div>
            <div class="grid gap-sm sm:grid-cols-2">
              <BaseField :label="t('settings.proxyAuth')">
                <template #default="{ id }">
                  <input
                    :id="id"
                    v-model="proxyUsername"
                    class="field"
                    autocomplete="off"
                    :placeholder="t('settings.proxyUsername')"
                  />
                </template>
              </BaseField>
              <BaseField label="&nbsp;">
                <template #default="{ id }">
                  <input
                    :id="id"
                    v-model="proxyPassword"
                    type="password"
                    class="field"
                    autocomplete="new-password"
                    :placeholder="t('settings.proxyPassword')"
                  />
                </template>
              </BaseField>
            </div>
          </template>

          <BaseButton variant="primary" size="sm" :loading="savingProxy" @click="saveProxy">
            {{ t('settings.saveProxy') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- rate limit -->
      <SectionCard :title="t('settings.rateLimitSettings')" :icon="PhGauge">
        <div class="space-y-md">
          <BaseField :label="t('settings.rateLimitRpm')" :hint="t('settings.rateLimitRpmHint')">
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="rateLimitRpm"
                type="number"
                min="0"
                class="field tnum"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <BaseField
            :label="t('settings.rateLimitPerKey')"
            :hint="t('settings.rateLimitPerKeyHint')"
          >
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="rateLimitPerKeyRpm"
                type="number"
                min="0"
                class="field tnum"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <BaseField
            :label="t('settings.rateLimitBurst')"
            :hint="t('settings.rateLimitBurstHint')"
          >
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="rateLimitBurstSeconds"
                type="number"
                min="0"
                class="field tnum"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <p class="text-caption text-txt-tertiary">{{ t('settings.rateLimitRestartHint') }}</p>
          <BaseButton variant="primary" size="sm" :loading="savingRateLimit" @click="saveRateLimit">
            {{ t('settings.saveRateLimit') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- webhook -->
      <SectionCard :title="t('settings.webhookSettings')" :icon="PhWebhooksLogo">
        <div class="space-y-md">
          <BaseField :label="t('settings.webhookUrl')" :hint="t('settings.webhookUrlHint')">
            <template #default="{ id, describedBy }">
              <input
                :id="id"
                v-model="webhookUrl"
                class="field"
                :placeholder="t('settings.webhookUrl')"
                :aria-describedby="describedBy"
              />
            </template>
          </BaseField>
          <BaseButton variant="primary" size="sm" :loading="savingWebhook" @click="saveWebhook">
            {{ t('settings.saveWebhook') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- admin password -->
      <SectionCard :title="t('settings.adminPassword')" :icon="PhLock">
        <div class="space-y-md">
          <BaseField :label="t('settings.newPassword')">
            <template #default="{ id }">
              <input
                :id="id"
                v-model="newPassword"
                type="password"
                class="field"
                autocomplete="new-password"
                :placeholder="t('settings.newPasswordPlaceholder')"
                @keyup.enter="changePassword"
              />
            </template>
          </BaseField>
          <BaseButton variant="primary" size="sm" :loading="savingPassword" @click="changePassword">
            {{ t('settings.changePassword') }}
          </BaseButton>
        </div>
      </SectionCard>

      <!-- statistics -->
      <SectionCard :title="t('settings.statistics')" :icon="PhChartBar">
        <BaseButton variant="danger" size="sm" :loading="resettingStats" @click="resetStats">
          <template #icon><PhArrowCounterClockwise :size="15" /></template>
          {{ t('settings.resetStats') }}
        </BaseButton>
      </SectionCard>

      <!-- prompt filter (full width: the rule list needs the room) -->
      <SectionCard
        :title="t('settings.promptFilter')"
        :icon="PhFunnel"
        class="xl:col-span-2"
      >
        <template #actions>
          <BaseButton variant="glass" size="xs" @click="addRule('regex')">
            <template #icon><PhPlus :size="14" /></template>
            {{ t('promptFilter.addRegex') }}
          </BaseButton>
          <BaseButton variant="glass" size="xs" @click="addRule('lines-containing')">
            <template #icon><PhPlus :size="14" /></template>
            {{ t('promptFilter.addContains') }}
          </BaseButton>
          <BaseButton variant="primary" size="xs" :loading="savingFilter" @click="savePromptFilter">
            {{ t('settings.savePromptFilter') }}
          </BaseButton>
        </template>

        <div class="space-y-lg">
          <div>
            <p class="nav-group-label mb-sm">{{ t('settings.builtinFilters') }}</p>
            <div class="space-y-md">
              <BaseSwitch
                v-model="filterClaudeCode"
                :label="t('settings.filterClaudeCode')"
                :hint="t('settings.filterClaudeCodeHint')"
              />
              <BaseSwitch
                v-model="filterEnvNoise"
                :label="t('settings.filterEnvNoise')"
                :hint="t('settings.filterEnvNoiseHint')"
              />
              <BaseSwitch
                v-model="filterStripBoundaries"
                :label="t('settings.filterStripBoundaries')"
                :hint="t('settings.filterStripBoundariesHint')"
              />
            </div>
          </div>

          <div>
            <p class="nav-group-label mb-sm">{{ t('settings.customRules') }}</p>

            <EmptyState v-if="!rules.length" compact :message="t('promptFilter.noRules')">
              <template #icon><PhBroom :size="28" /></template>
            </EmptyState>

            <ul v-else class="space-y-sm">
              <li
                v-for="(rule, idx) in rules"
                :key="rule.id || idx"
                class="rounded-[10px] border border-line bg-surface-input p-md"
              >
                <div class="flex flex-wrap items-center gap-sm">
                  <BaseSwitch v-model="rule.enabled" :aria-label="t('common.confirm')" />
                  <input
                    v-model="rule.name"
                    class="field h-9 flex-1 min-w-[160px]"
                    :placeholder="t('promptFilter.unnamed')"
                  />
                  <span class="badge badge-gray">
                    {{ rule.type === 'regex' ? t('promptFilter.typeRegex') : t('promptFilter.typeContains') }}
                  </span>
                  <BaseButton
                    variant="ghost"
                    size="xs"
                    icon
                    :aria-label="t('common.remove')"
                    @click="rules.splice(idx, 1)"
                  >
                    <template #icon><PhTrash :size="15" /></template>
                  </BaseButton>
                </div>

                <div class="mt-sm grid gap-sm" :class="rule.type === 'regex' && 'sm:grid-cols-2'">
                  <BaseField :label="t('promptFilter.match')">
                    <template #default="{ id }">
                      <input
                        :id="id"
                        v-model="rule.match"
                        class="field font-mono text-body-sm"
                        :placeholder="
                          rule.type === 'regex'
                            ? t('promptFilter.matchPlaceholderRegex')
                            : t('promptFilter.matchPlaceholderContains')
                        "
                      />
                    </template>
                  </BaseField>
                  <BaseField
                    v-if="rule.type === 'regex'"
                    :label="t('promptFilter.replace')"
                    :hint="t('promptFilter.emptyRemove')"
                  >
                    <template #default="{ id, describedBy }">
                      <input
                        :id="id"
                        v-model="rule.replace"
                        class="field font-mono text-body-sm"
                        :aria-describedby="describedBy"
                      />
                    </template>
                  </BaseField>
                </div>
              </li>
            </ul>
          </div>
        </div>
      </SectionCard>
    </div>
  </div>
</template>

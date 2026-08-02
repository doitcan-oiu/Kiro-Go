<script setup>
// API tab: the five public endpoint URLs, plus viewers for the live model list
// and the runtime stats payload.
import { computed, ref } from 'vue'
import {
  PhArrowSquareOut,
  PhChartLine,
  PhMagnifyingGlass,
  PhPlug,
  PhTerminal,
} from '@phosphor-icons/vue'
import { api, fetchPublicModels } from '@/lib/api'
import { useI18n } from '@/lib/i18n'
import { copyText } from '@/lib/clipboard'
import { toast } from '@/lib/toast'
import { formatNumber, formatUptime } from '@/lib/format'
import SectionCard from '@/components/ui/SectionCard.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const { t } = useI18n()

const origin = location.origin

/**
 * The five endpoints the proxy exposes. `curl` samples are built lazily so the
 * body payloads stay readable in source.
 */
const endpoints = computed(() => [
  {
    id: 'claude',
    title: t('api.claude'),
    method: 'POST',
    path: '/v1/messages',
    copyLabel: t('api.copyClaude'),
    curl: curlBody('/v1/messages', {
      model: 'claude-sonnet-4',
      max_tokens: 1024,
      messages: [{ role: 'user', content: 'Hello' }],
    }),
  },
  {
    id: 'openai',
    title: t('api.openai'),
    method: 'POST',
    path: '/v1/chat/completions',
    copyLabel: t('api.copyOpenAI'),
    curl: curlBody('/v1/chat/completions', {
      model: 'claude-sonnet-4',
      messages: [{ role: 'user', content: 'Hello' }],
    }),
  },
  {
    id: 'responses',
    title: t('api.openaiResponses'),
    method: 'POST',
    path: '/v1/responses',
    copyLabel: t('api.copyOpenAIResponses'),
    curl: curlBody('/v1/responses', { model: 'claude-sonnet-4', input: 'Hello' }),
  },
  {
    id: 'models',
    title: t('api.modelList'),
    method: 'GET',
    path: '/v1/models',
    copyLabel: t('api.copyModels'),
    curl: `curl ${origin}/v1/models \\\n  -H "Authorization: Bearer $KIRO_API_KEY"`,
    view: openModels,
    viewLabel: t('api.viewModels'),
  },
  {
    id: 'stats',
    title: t('api.stats'),
    method: 'GET',
    path: '/v1/stats',
    copyLabel: t('api.copyStats'),
    curl: `curl ${origin}/v1/stats`,
    view: openStats,
    viewLabel: t('api.viewStats'),
  },
])

function curlBody(path, body) {
  return [
    `curl ${origin}${path} \\`,
    '  -H "Content-Type: application/json" \\',
    '  -H "Authorization: Bearer $KIRO_API_KEY" \\',
    `  -d '${JSON.stringify(body)}'`,
  ].join('\n')
}

async function copyCurl(sample) {
  await copyText(sample)
  toast(t('api.curlCopied'), 'success')
}

// ── models viewer ─────────────────────────────────────────────────────────
const modelsOpen = ref(false)
const modelsLoading = ref(false)
const modelsError = ref('')
const models = ref([])
const modelSearch = ref('')

/**
 * Model families in display order. Matching is prefix/substring based on the
 * model id, mirroring the legacy grouping.
 */
const FAMILIES = [
  { id: 'claude', label: 'Claude', match: (id) => id.includes('claude') },
  { id: 'openai', label: 'OpenAI', match: (id) => /gpt|o1|o3|o4/.test(id) },
  { id: 'deepseek', label: 'DeepSeek', match: (id) => id.includes('deepseek') },
  { id: 'qwen', label: 'Qwen', match: (id) => id.includes('qwen') },
  { id: 'gemini', label: 'Gemini', match: (id) => id.includes('gemini') },
  { id: 'meta', label: 'Llama', match: (id) => id.includes('llama') },
  { id: 'mistral', label: 'Mistral', match: (id) => id.includes('mistral') },
  { id: 'other', label: 'Other', match: () => true },
]

async function openModels() {
  modelsOpen.value = true
  if (models.value.length) return
  modelsLoading.value = true
  modelsError.value = ''
  try {
    const res = await fetchPublicModels()
    models.value = Array.isArray(res?.data) ? res.data : []
  } catch (err) {
    modelsError.value = err.message || t('api.fetchError')
  } finally {
    modelsLoading.value = false
  }
}

const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter(
    (m) =>
      String(m.id || '')
        .toLowerCase()
        .includes(q) ||
      String(m.owned_by || '')
        .toLowerCase()
        .includes(q),
  )
})

/** Groups the filtered models by family, dropping empty groups. */
const modelGroups = computed(() => {
  const buckets = new Map(FAMILIES.map((f) => [f.id, []]))
  for (const model of filteredModels.value) {
    const id = String(model.id || '').toLowerCase()
    const family = FAMILIES.find((f) => f.match(id))
    buckets.get(family.id).push(model)
  }
  return FAMILIES.map((f) => ({ ...f, models: buckets.get(f.id) })).filter(
    (g) => g.models.length > 0,
  )
})

// ── stats viewer ──────────────────────────────────────────────────────────
const statsOpen = ref(false)
const statsLoading = ref(false)
const statsError = ref('')
const statsData = ref(null)

async function openStats() {
  statsOpen.value = true
  statsLoading.value = true
  statsError.value = ''
  try {
    statsData.value = await api.status()
  } catch (err) {
    statsError.value = err.message || t('api.fetchError')
  } finally {
    statsLoading.value = false
  }
}

const statsRows = computed(() => {
  const s = statsData.value
  if (!s) return []
  return [
    { label: t('api.statsVersion'), value: s.version || '—' },
    { label: t('api.statsAccounts'), value: formatNumber(s.accounts) },
    { label: t('api.statsAvailable'), value: formatNumber(s.available) },
    { label: t('api.statsTotalReqs'), value: formatNumber(s.totalRequests) },
    { label: t('api.statsSuccessReqs'), value: formatNumber(s.successRequests) },
    { label: t('api.statsFailedReqs'), value: formatNumber(s.failedRequests) },
    { label: t('api.statsTotalTokens'), value: formatNumber(s.totalTokens) },
    { label: t('api.statsTotalCredits'), value: formatNumber(s.totalCredits) },
  ]
})
</script>

<template>
  <div class="space-y-lg">
    <SectionCard :title="t('api.endpoints')" :icon="PhPlug" :hint="t('api.statsHint')">
      <template #actions>
        <StatusBadge :label="t('api.protocolHttp')" tone="blue" />
      </template>

      <ul class="space-y-md">
        <li
          v-for="ep in endpoints"
          :key="ep.id"
          class="glass-thin flex flex-wrap items-center gap-md rounded-[10px] p-md"
        >
          <StatusBadge
            :label="ep.method"
            :tone="ep.method === 'GET' ? 'blue' : 'green'"
            class="shrink-0 font-mono"
          />

          <div class="min-w-0 flex-1">
            <p class="text-body-sm font-medium text-txt">{{ ep.title }}</p>
            <p class="mt-0.5 truncate font-mono text-caption text-txt-tertiary">
              <span class="text-txt-secondary">{{ origin }}</span>{{ ep.path }}
            </p>
          </div>

          <div class="flex shrink-0 items-center gap-xs">
            <BaseButton
              v-if="ep.view"
              variant="glass"
              size="xs"
              @click="ep.view()"
            >
              <template #icon><PhArrowSquareOut :size="15" /></template>
              {{ ep.viewLabel }}
            </BaseButton>
            <CopyButton
              :value="origin + ep.path"
              size="xs"
              :label="ep.copyLabel"
              :aria-label="ep.copyLabel"
            />
            <BaseButton
              variant="ghost"
              size="xs"
              :title="t('api.copyCurl')"
              :aria-label="t('api.copyCurl')"
              @click="copyCurl(ep.curl)"
            >
              <template #icon><PhTerminal :size="15" /></template>
              curl
            </BaseButton>
          </div>
        </li>
      </ul>
    </SectionCard>

    <!-- models viewer -->
    <BaseModal v-model="modelsOpen" :title="t('api.viewModelsTitle')" size="lg">
      <div v-if="modelsLoading" class="space-y-2">
        <div v-for="i in 6" :key="i" class="skeleton h-9" />
      </div>

      <p v-else-if="modelsError" class="text-body-sm text-error">{{ modelsError }}</p>

      <template v-else>
        <div class="mb-md flex flex-wrap items-center gap-md">
          <p class="text-body-sm text-txt-secondary">
            {{ t('api.totalModels', { count: models.length }) }}
          </p>
          <BaseField
            v-model="modelSearch"
            class="ml-auto w-full sm:w-64"
            :placeholder="t('api.searchModels')"
            :aria-label="t('api.searchModels')"
          >
            <template #prefix><PhMagnifyingGlass :size="16" /></template>
          </BaseField>
        </div>

        <EmptyState v-if="!filteredModels.length" :message="t('api.noModels')" compact />

        <div v-else class="space-y-lg">
          <section v-for="group in modelGroups" :key="group.id">
            <h3 class="nav-group-label mb-sm">{{ group.label }} · {{ group.models.length }}</h3>
            <ul class="space-y-1">
              <li
                v-for="model in group.models"
                :key="model.id"
                class="flex flex-wrap items-center gap-sm rounded-[6px] bg-surface-input px-md py-2"
              >
                <code class="min-w-0 flex-1 truncate font-mono text-caption text-txt">{{
                  model.id
                }}</code>
                <StatusBadge
                  v-if="String(model.id).endsWith('-thinking')"
                  label="thinking"
                  tone="blue"
                />
                <StatusBadge v-if="model.supports_image" label="vision" tone="green" />
              </li>
            </ul>
          </section>
        </div>
      </template>

      <template #footer>
        <BaseButton variant="glass" size="sm" @click="modelsOpen = false">
          {{ t('common.close') }}
        </BaseButton>
      </template>
    </BaseModal>

    <!-- stats viewer -->
    <BaseModal v-model="statsOpen" :title="t('api.viewStatsTitle')">
      <div v-if="statsLoading" class="grid grid-cols-2 gap-md">
        <div v-for="i in 8" :key="i" class="skeleton h-16" />
      </div>

      <p v-else-if="statsError" class="text-body-sm text-error">{{ statsError }}</p>

      <template v-else>
        <dl class="grid grid-cols-2 gap-md sm:grid-cols-4">
          <div
            v-for="row in statsRows"
            :key="row.label"
            class="rounded-[10px] bg-surface-input p-md"
          >
            <dt class="text-caption-sm text-txt-tertiary">{{ row.label }}</dt>
            <dd class="mt-1 tnum text-title-sm text-txt">{{ row.value }}</dd>
          </div>
        </dl>
        <p class="mt-md flex items-center gap-2 text-caption text-txt-tertiary">
          <PhChartLine :size="14" />
          {{ t('api.statsUptime') }}: {{ formatUptime(statsData?.uptime) }}
        </p>
      </template>

      <template #footer>
        <BaseButton variant="glass" size="sm" @click="statsOpen = false">
          {{ t('common.close') }}
        </BaseButton>
      </template>
    </BaseModal>
  </div>
</template>

<script setup>
// Request log table with client-side filtering and an optional 5s auto-refresh.
//
// The server returns the full ring buffer in one shot (`GET /logs`), so every
// filter here is client-side; only the refresh itself hits the network.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { PhArrowsClockwise, PhListDashes, PhTrash } from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { useDataStore } from '@/stores/data'
import { usePrefsStore } from '@/stores/prefs'
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { toast } from '@/lib/toast'
import {
  errorTypeLabel,
  formatLogTime,
  formatNum,
  maskEmail,
  toFixed,
} from '@/lib/format'
import SectionCard from '@/components/ui/SectionCard.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import BaseCheckbox from '@/components/ui/BaseCheckbox.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const { t } = useI18n()
const data = useDataStore()
const prefs = usePrefsStore()
const { logs, loadingLogs, accounts } = storeToRefs(data)

const statusFilter = ref('all')
const keyFilter = ref('')
const modelFilter = ref('')
const endpointFilter = ref('')
const autoRefresh = ref(false)

const statusOptions = computed(() => [
  { value: 'all', label: t('logs.filterAll') },
  { value: 'success', label: t('logs.filterSuccess') },
  { value: 'error', label: t('logs.filterError') },
])

/** Builds a `[{value,label}]` list of distinct values for one log field. */
function distinctOptions(field, labelField) {
  const seen = new Map()
  for (const log of logs.value) {
    const value = log[field]
    if (!value || seen.has(value)) continue
    seen.set(value, (labelField && log[labelField]) || value)
  }
  return [...seen].map(([value, label]) => ({ value, label }))
}

const keyOptions = computed(() => [
  { value: '', label: t('logs.filterKey') },
  ...distinctOptions('apiKeyId', 'apiKeyName'),
])
const modelOptions = computed(() => [
  { value: '', label: t('logs.filterModel') },
  ...distinctOptions('model'),
])
const endpointOptions = computed(() => [
  { value: '', label: t('logs.filterEndpoint') },
  ...distinctOptions('endpoint'),
])

const filtered = computed(() =>
  logs.value.filter((log) => {
    if (statusFilter.value !== 'all' && log.status !== statusFilter.value) return false
    if (keyFilter.value && log.apiKeyId !== keyFilter.value) return false
    if (modelFilter.value && log.model !== modelFilter.value) return false
    if (endpointFilter.value && log.endpoint !== endpointFilter.value) return false
    return true
  }),
)

/** Totals over the *filtered* rows, matching the legacy summary semantics. */
const summary = computed(() => {
  const acc = {
    total: 0,
    success: 0,
    errors: 0,
    input: 0,
    output: 0,
    cacheRead: 0,
    cacheCreation: 0,
  }
  for (const log of filtered.value) {
    acc.total += 1
    if (log.status === 'success') acc.success += 1
    else acc.errors += 1
    acc.input += Number(log.inputTokens || 0)
    acc.output += Number(log.outputTokens || 0)
    acc.cacheRead += Number(log.cacheRead || 0)
    acc.cacheCreation += Number(log.cacheCreation || 0)
  }
  // Hit rate counts cache reads against everything that could have been read
  // from cache (fresh input + cached input).
  const readable = acc.input + acc.cacheRead
  acc.hitRate = readable > 0 ? (acc.cacheRead / readable) * 100 : 0
  return acc
})

const summaryItems = computed(() => [
  { label: t('logs.total'), value: summary.value.total },
  { label: t('logs.success'), value: summary.value.success, tone: 'text-success' },
  { label: t('logs.errors'), value: summary.value.errors, tone: 'text-error' },
  { label: t('logs.input'), value: formatNum(summary.value.input) },
  { label: t('logs.output'), value: formatNum(summary.value.output) },
  { label: t('logs.cacheRead'), value: formatNum(summary.value.cacheRead) },
  { label: t('logs.cacheCreation'), value: formatNum(summary.value.cacheCreation) },
  { label: t('logs.cacheHitRate'), value: `${toFixed(summary.value.hitRate, 1)}%` },
])

/** Resolves an account id to a display label, honouring privacy mode. */
function accountLabel(id) {
  if (!id) return '—'
  const match = accounts.value.find((a) => a.id === id)
  if (!match?.email) return id.slice(0, 8)
  return prefs.privacyMode ? maskEmail(match.email) : match.email
}

function tokenParts(log) {
  const parts = []
  if (log.inputTokens) parts.push(`${t('logs.input')} ${formatNum(log.inputTokens)}`)
  if (log.outputTokens) parts.push(`${t('logs.output')} ${formatNum(log.outputTokens)}`)
  if (log.cacheRead) parts.push(`${t('logs.cacheRead')} ${formatNum(log.cacheRead)}`)
  if (log.cacheCreation) parts.push(`${t('logs.cacheCreation')} ${formatNum(log.cacheCreation)}`)
  return parts
}

async function refresh() {
  try {
    await data.loadLogs()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  }
}

async function clearLogs() {
  const ok = await confirm({ message: t('logs.clearConfirm'), danger: true })
  if (!ok) return
  try {
    await api.clearLogs()
    await data.loadLogs()
    toast(t('logs.cleared'), 'success')
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  }
}

function syncPolling() {
  if (autoRefresh.value) data.startLogsPolling()
  else data.stopLogsPolling()
}

onMounted(refresh)
// Leaving the view must stop the poll; otherwise it keeps firing in the
// background for the lifetime of the session.
onBeforeUnmount(() => data.stopLogsPolling())
</script>

<template>
  <div class="space-y-lg">
    <SectionCard :title="t('logs.title')" :icon="PhListDashes" flush>
      <template #actions>
        <BaseSelect
          v-model="statusFilter"
          :options="statusOptions"
          :aria-label="t('logs.filter')"
          size="sm"
          class="w-[136px]"
        />
        <BaseSelect
          v-model="keyFilter"
          :options="keyOptions"
          :aria-label="t('logs.filterKey')"
          size="sm"
          class="w-[150px]"
        />
        <BaseSelect
          v-model="modelFilter"
          :options="modelOptions"
          :aria-label="t('logs.filterModel')"
          size="sm"
          class="w-[160px]"
        />
        <BaseSelect
          v-model="endpointFilter"
          :options="endpointOptions"
          :aria-label="t('logs.filterEndpoint')"
          size="sm"
          class="w-[150px]"
        />
        <BaseCheckbox
          v-model="autoRefresh"
          :label="t('logs.autoRefresh')"
          @update:model-value="syncPolling"
        />
        <BaseButton size="sm" :loading="loadingLogs" @click="refresh">
          <template #icon><PhArrowsClockwise :size="16" /></template>
          {{ t('logs.refresh') }}
        </BaseButton>
        <BaseButton variant="danger" size="sm" @click="clearLogs">
          <template #icon><PhTrash :size="16" /></template>
          {{ t('logs.clear') }}
        </BaseButton>
      </template>

      <!-- Summary band -->
      <div class="flex flex-wrap gap-x-xl gap-y-sm border-b border-divider px-lg py-md">
        <div v-for="item in summaryItems" :key="item.label" class="flex items-baseline gap-2">
          <span class="text-caption text-txt-tertiary">{{ item.label }}</span>
          <span class="tnum text-body-sm font-semibold" :class="item.tone || 'text-txt'">
            {{ item.value }}
          </span>
        </div>
        <p class="w-full text-caption-sm text-txt-tertiary">{{ t('logs.persisted') }}</p>
      </div>

      <EmptyState v-if="!filtered.length" :message="t('logs.empty')" :icon="PhListDashes" />

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <th scope="col">{{ t('logs.time') }}</th>
              <th scope="col">{{ t('logs.status') }}</th>
              <th scope="col">{{ t('logs.endpoint') }}</th>
              <th scope="col">{{ t('logs.model') }}</th>
              <th scope="col">{{ t('logs.account') }}</th>
              <th scope="col">{{ t('logs.tokens') }}</th>
              <th scope="col" class="text-right">{{ t('logs.duration') }}</th>
              <th scope="col">{{ t('logs.detail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(log, idx) in filtered" :key="`${log.time}-${idx}`">
              <td class="tnum whitespace-nowrap text-txt-secondary">{{ formatLogTime(log.time) }}</td>
              <td>
                <StatusBadge
                  :label="log.status === 'success' ? t('logs.statusSuccess') : t('logs.statusError')"
                  :tone="log.status === 'success' ? 'green' : 'red'"
                  dot
                />
              </td>
              <td class="whitespace-nowrap font-mono text-caption text-txt-secondary">
                {{ log.endpoint || '—' }}
              </td>
              <td class="whitespace-nowrap">{{ log.model || '—' }}</td>
              <td class="whitespace-nowrap text-txt-secondary">{{ accountLabel(log.accountId) }}</td>
              <td>
                <div v-if="tokenParts(log).length" class="flex flex-wrap gap-x-3 gap-y-0.5">
                  <span
                    v-for="part in tokenParts(log)"
                    :key="part"
                    class="tnum text-caption text-txt-secondary"
                  >
                    {{ part }}
                  </span>
                </div>
                <span v-else class="text-txt-tertiary">—</span>
              </td>
              <td class="tnum whitespace-nowrap text-right text-txt-secondary">
                {{ log.duration ?? 0 }}ms
              </td>
              <td>
                <div v-if="log.status === 'error'" class="flex flex-wrap items-center gap-2">
                  <StatusBadge
                    v-if="log.errorType"
                    :label="errorTypeLabel(log.errorType)"
                    tone="red"
                  />
                  <span class="text-caption text-txt-tertiary">{{ log.error }}</span>
                </div>
                <span v-else-if="log.credits" class="tnum text-caption text-txt-secondary">
                  {{ toFixed(log.credits, 3) }} cr
                </span>
                <span v-else class="text-txt-tertiary">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </SectionCard>
  </div>
</template>

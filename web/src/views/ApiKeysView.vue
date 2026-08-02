<script setup>
// API key manager: list, filters, batch actions, create/edit, import/export.
//
// Two contracts worth keeping in mind:
//   - `key` is only ever returned in full by POST /api-keys (creation). Every
//     later read gives `keyMasked`, so the plaintext must be surfaced once, at
//     creation time, and never re-requested.
//   - a limit of 0 means unlimited, not "blocked".
import { computed, onMounted, ref } from 'vue'
import {
  PhCheck,
  PhDownloadSimple,
  PhKey,
  PhMagnifyingGlass,
  PhPencilSimple,
  PhPlus,
  PhTrash,
  PhUploadSimple,
  PhWarning,
} from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { confirm } from '@/lib/confirm'
import { downloadJson } from '@/lib/clipboard'
import { formatLocale, formatNumber, todayStamp } from '@/lib/format'
import SectionCard from '@/components/ui/SectionCard.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import BaseSwitch from '@/components/ui/BaseSwitch.vue'
import BaseCheckbox from '@/components/ui/BaseCheckbox.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import UsageMeter from '@/components/ui/UsageMeter.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import CopyButton from '@/components/ui/CopyButton.vue'

const { t } = useI18n()

const keys = ref([])
const loading = ref(false)
const loadError = ref('')

const search = ref('')
const statusFilter = ref('all')
const sortBy = ref('created')
const selected = ref(new Set())

// requireApiKey lives in /settings but is edited here, next to the key list it
// governs — enabling it with no usable key locks every client out.
const requireApiKey = ref(false)
const savingRequire = ref(false)

const statusOptions = computed(() => [
  { value: 'all', label: t('filter.all') },
  { value: 'enabled', label: t('filter.enabled') },
  { value: 'disabled', label: t('filter.disabled') },
  { value: 'overLimit', label: t('apiKeys.filterOverLimit') },
])

const sortOptions = computed(() => [
  { value: 'created', label: t('apiKeys.sortCreated') },
  { value: 'lastUsed', label: t('apiKeys.sortLastUsed') },
  { value: 'tokens', label: t('apiKeys.sortTokens') },
  { value: 'credits', label: t('apiKeys.sortCredits') },
  { value: 'requests', label: t('apiKeys.sortRequests') },
])

function isOverLimit(k) {
  const tokenOver = k.tokenLimit > 0 && k.tokensUsed >= k.tokenLimit
  const creditOver = k.creditLimit > 0 && k.creditsUsed >= k.creditLimit
  return tokenOver || creditOver
}

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  let list = keys.value.filter((k) => {
    if (q) {
      const haystack = `${k.name || ''} ${k.keyMasked || ''}`.toLowerCase()
      if (!haystack.includes(q)) return false
    }
    if (statusFilter.value === 'enabled' && !k.enabled) return false
    if (statusFilter.value === 'disabled' && k.enabled) return false
    if (statusFilter.value === 'overLimit' && !isOverLimit(k)) return false
    return true
  })

  const field = {
    created: (k) => k.createdAt ?? 0,
    lastUsed: (k) => k.lastUsedAt ?? 0,
    tokens: (k) => k.tokensUsed ?? 0,
    credits: (k) => k.creditsUsed ?? 0,
    requests: (k) => k.requestsCount ?? 0,
  }[sortBy.value]

  return [...list].sort((a, b) => field(b) - field(a))
})

const enabledCount = computed(() => keys.value.filter((k) => k.enabled).length)
const selectedIds = computed(() => filtered.value.filter((k) => selected.value.has(k.id)))
const allSelected = computed(
  () => filtered.value.length > 0 && filtered.value.every((k) => selected.value.has(k.id)),
)
const someSelected = computed(() => selectedIds.value.length > 0 && !allSelected.value)

function toggleSelectAll(next) {
  const set = new Set(selected.value)
  for (const k of filtered.value) {
    if (next) set.add(k.id)
    else set.delete(k.id)
  }
  selected.value = set
}

function toggleOne(id, next) {
  const set = new Set(selected.value)
  if (next) set.add(id)
  else set.delete(id)
  selected.value = set
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.apiKeys()
    keys.value = Array.isArray(res?.apiKeys) ? res.apiKeys : []
    selected.value = new Set()
  } catch (err) {
    loadError.value = err.message || t('apiKeys.loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadRequireFlag() {
  try {
    const res = await api.settings()
    requireApiKey.value = Boolean(res?.requireApiKey)
  } catch {
    /* non-critical */
  }
}

onMounted(() => {
  load()
  loadRequireFlag()
})

async function saveRequireApiKey() {
  // Turning this on with no enabled key would reject every inbound request.
  if (requireApiKey.value && enabledCount.value === 0) {
    const ok = await confirm({
      message: t('apiKeys.requireWithoutEnabledKeyWarning'),
      danger: true,
    })
    if (!ok) {
      requireApiKey.value = false
      return
    }
  }
  savingRequire.value = true
  try {
    await api.saveSettings({ requireApiKey: requireApiKey.value })
    toast(t('common.saved'), 'success')
  } catch (err) {
    toast(err.message || t('common.saveFailed'), 'error')
  } finally {
    savingRequire.value = false
  }
}

// ── row actions ───────────────────────────────────────────────────────────
async function toggleKey(k, next) {
  try {
    await api.updateApiKey(k.id, { enabled: next })
    k.enabled = next
  } catch (err) {
    toast(err.message || t('common.saveFailed'), 'error')
    await load()
  }
}

async function resetUsage(k) {
  const ok = await confirm({ message: t('apiKeys.confirmReset', k.name || t('apiKeys.unnamed')) })
  if (!ok) return
  try {
    await api.resetApiKeyUsage(k.id)
    toast(t('apiKeys.usageReset'), 'success')
    await load()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  }
}

async function removeKey(k) {
  const ok = await confirm({
    message: t('apiKeys.confirmDelete', k.name || t('apiKeys.unnamed')),
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteApiKey(k.id)
    toast(t('apiKeys.deleteSuccess'), 'success')
    await load()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  }
}

// ── batch actions ─────────────────────────────────────────────────────────
const batchRunning = ref(false)

/** Runs `fn` over the selection sequentially, then reports ok/fail counts. */
async function runBatch(confirmKey, fn, resultKey) {
  const items = selectedIds.value
  if (!items.length) return
  const ok = await confirm({
    message: t(confirmKey, items.length),
    danger: confirmKey.includes('Delete'),
  })
  if (!ok) return

  batchRunning.value = true
  const dismiss = toast(t('batch.processing'), 'info', { duration: 0 })
  let done = 0
  let failed = 0
  for (const item of items) {
    try {
      await fn(item)
      done += 1
    } catch {
      failed += 1
    }
  }
  dismiss()
  batchRunning.value = false
  toast(t(resultKey, done, failed), failed ? 'warning' : 'success')
  await load()
}

const batchEnable = () =>
  runBatch(
    'apiKeys.batchConfirmEnable',
    (k) => api.updateApiKey(k.id, { enabled: true }),
    'apiKeys.batchEnableResult',
  )
const batchDisable = () =>
  runBatch(
    'apiKeys.batchConfirmDisable',
    (k) => api.updateApiKey(k.id, { enabled: false }),
    'apiKeys.batchDisableResult',
  )
const batchReset = () =>
  runBatch(
    'apiKeys.batchConfirmReset',
    (k) => api.resetApiKeyUsage(k.id),
    'apiKeys.batchResetResult',
  )
const batchDelete = () =>
  runBatch('apiKeys.batchConfirmDelete', (k) => api.deleteApiKey(k.id), 'batch.deleteResult')

// ── create / edit ─────────────────────────────────────────────────────────
const formOpen = ref(false)
const editing = ref(null)
const saving = ref(false)
const form = ref(blankForm())

function blankForm() {
  return {
    name: '',
    key: '',
    enabled: true,
    tokenLimit: 0,
    creditLimit: 0,
    allowedIPs: '',
  }
}

function openCreate() {
  editing.value = null
  form.value = blankForm()
  formOpen.value = true
}

function openEdit(k) {
  editing.value = k
  form.value = {
    name: k.name || '',
    key: k.keyMasked || '',
    enabled: Boolean(k.enabled),
    tokenLimit: Number(k.tokenLimit || 0),
    creditLimit: Number(k.creditLimit || 0),
    allowedIPs: (k.allowedIPs || []).join('\n'),
  }
  formOpen.value = true
}

/** Clamps a limit to a non-negative number; NaN and negatives mean unlimited. */
function normalizeLimit(value) {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? n : 0
}

const createdKey = ref('')
const createdOpen = ref(false)

async function saveForm() {
  const payload = {
    name: form.value.name.trim(),
    enabled: form.value.enabled,
    tokenLimit: normalizeLimit(form.value.tokenLimit),
    creditLimit: normalizeLimit(form.value.creditLimit),
    allowedIPs: form.value.allowedIPs
      .split(/[,\n\r]+/)
      .map((s) => s.trim())
      .filter(Boolean),
  }

  saving.value = true
  try {
    if (editing.value) {
      await api.updateApiKey(editing.value.id, payload)
      toast(t('apiKeys.updated'), 'success')
    } else {
      // A blank key field asks the server to generate one.
      const custom = form.value.key.trim()
      if (custom) payload.key = custom
      const res = await api.createApiKey(payload)
      toast(t('apiKeys.created'), 'success')
      const plaintext = res?.key || res?.apiKey?.key || ''
      if (plaintext) {
        createdKey.value = plaintext
        createdOpen.value = true
      }
    }
    formOpen.value = false
    await load()
  } catch (err) {
    toast(err.message || t('common.saveFailed'), 'error')
  } finally {
    saving.value = false
  }
}

/** Per-model usage rows for the edit dialog, busiest first. */
const usageRows = computed(() => {
  const map = editing.value?.usageByModel
  if (!map || typeof map !== 'object') return []
  return Object.entries(map)
    .map(([model, v]) => ({
      model,
      requests: v?.requests ?? 0,
      tokens: v?.tokens ?? 0,
      credits: v?.credits ?? 0,
    }))
    .sort((a, b) => b.requests - a.requests)
})

// ── import / export ───────────────────────────────────────────────────────
const importOpen = ref(false)
const importText = ref('')
const importing = ref(false)

function exportKeys() {
  // Masked values only — the server cannot return plaintext, and writing a file
  // full of live secrets would be worse anyway.
  const payload = keys.value.map((k) => ({
    name: k.name,
    keyMasked: k.keyMasked,
    enabled: k.enabled,
    tokenLimit: k.tokenLimit,
    creditLimit: k.creditLimit,
    tokensUsed: k.tokensUsed,
    creditsUsed: k.creditsUsed,
    requestsCount: k.requestsCount,
    allowedIPs: k.allowedIPs,
  }))
  downloadJson(`kiro-api-keys-${todayStamp()}.json`, payload)
  toast(t('apiKeys.exportDone', payload.length), 'success')
}

/** Accepts a JSON array/object or one key per line. */
function parseImport(raw) {
  const text = raw.trim()
  if (!text) return []
  try {
    const parsed = JSON.parse(text)
    const list = Array.isArray(parsed) ? parsed : [parsed]
    return list
      .map((item) =>
        typeof item === 'string' ? item : item?.key || item?.api_key || item?.kiroApiKey || '',
      )
      .map((s) => String(s).trim())
      .filter(Boolean)
  } catch {
    return text
      .split(/[\n\r,]+/)
      .map((s) => s.trim())
      .filter(Boolean)
  }
}

async function runImport() {
  const list = parseImport(importText.value)
  if (!list.length) {
    toast(t('apikeyBatch.keysMissing'), 'warning')
    return
  }
  importing.value = true
  try {
    const res = await api.importApiKeysBatch(list.join('\n'))
    toast(
      t('apiKeys.importSuccess', res?.imported ?? 0, res?.total ?? list.length, res?.skipped ?? 0),
      'success',
    )
    importOpen.value = false
    importText.value = ''
    await load()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    importing.value = false
  }
}

function limitText(used, limit) {
  if (!limit || limit <= 0) return `${formatNumber(used)} / ${t('apiKeys.unlimited')}`
  return `${formatNumber(used)} / ${formatNumber(limit)}`
}
</script>

<template>
  <div class="space-y-lg">
    <!-- gate -->
    <SectionCard :title="t('settings.apiSettings')" :icon="PhKey">
      <div class="flex flex-wrap items-end justify-between gap-md">
        <BaseSwitch
          v-model="requireApiKey"
          :label="t('settings.enableApiKey')"
          :hint="t('apiKeys.requireHint')"
        />
        <BaseButton variant="primary" size="sm" :loading="savingRequire" @click="saveRequireApiKey">
          {{ t('settings.saveApiKey') }}
        </BaseButton>
      </div>
      <p
        v-if="requireApiKey && enabledCount === 0"
        class="mt-md flex items-start gap-2 rounded-[10px] bg-[rgb(245_158_11/0.1)] p-3 text-caption text-warning"
      >
        <PhWarning :size="16" class="mt-px shrink-0" />
        {{ t('apiKeys.requireWithoutEnabledKeyWarning') }}
      </p>
    </SectionCard>

    <!-- list -->
    <SectionCard :title="t('apiKeys.listTitle')" :hint="t('apiKeys.listHint')">
      <template #actions>
        <BaseButton variant="glass" size="sm" @click="exportKeys">
          <template #icon><PhDownloadSimple :size="15" /></template>
          {{ t('apiKeys.export') }}
        </BaseButton>
        <BaseButton variant="glass" size="sm" @click="importOpen = true">
          <template #icon><PhUploadSimple :size="15" /></template>
          {{ t('apiKeys.import') }}
        </BaseButton>
        <BaseButton variant="primary" size="sm" @click="openCreate">
          <template #icon><PhPlus :size="15" /></template>
          {{ t('apiKeys.add') }}
        </BaseButton>
      </template>

      <!-- filters -->
      <div class="flex flex-wrap items-center gap-sm">
        <div class="relative min-w-[200px] flex-1">
          <PhMagnifyingGlass
            :size="16"
            class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-txt-tertiary"
          />
          <input
            v-model="search"
            type="search"
            class="field pl-9"
            :placeholder="t('apiKeys.search')"
            :aria-label="t('apiKeys.search')"
          />
        </div>
        <BaseSelect
          v-model="statusFilter"
          :options="statusOptions"
          :aria-label="t('apiKeys.statusFilter')"
          class="w-[160px]"
        />
        <BaseSelect
          v-model="sortBy"
          :options="sortOptions"
          :aria-label="t('apiKeys.sort')"
          class="w-[170px]"
        />
      </div>

      <!-- batch bar -->
      <div class="mt-md flex flex-wrap items-center gap-sm border-t border-divider pt-md">
        <BaseCheckbox
          :model-value="allSelected"
          :indeterminate="someSelected"
          :label="t('batch.selectAll')"
          @update:model-value="toggleSelectAll"
        />
        <template v-if="selectedIds.length">
          <span class="text-caption text-txt-tertiary">
            {{ t('batch.selected', selectedIds.length) }}
          </span>
          <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchEnable">
            {{ t('batch.enable') }}
          </BaseButton>
          <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchDisable">
            {{ t('batch.disable') }}
          </BaseButton>
          <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchReset">
            {{ t('apiKeys.actionReset') }}
          </BaseButton>
          <BaseButton variant="danger" size="xs" :disabled="batchRunning" @click="batchDelete">
            {{ t('batch.delete') }}
          </BaseButton>
        </template>
      </div>

      <!-- rows -->
      <div v-if="loading" class="mt-md space-y-2">
        <div v-for="i in 3" :key="i" class="skeleton h-28" />
      </div>

      <p v-else-if="loadError" class="mt-md text-body-sm text-error">{{ loadError }}</p>

      <EmptyState v-else-if="!keys.length" :message="t('apiKeys.empty')">
        <template #icon><PhKey :size="28" /></template>
      </EmptyState>

      <EmptyState v-else-if="!filtered.length" :message="t('apiKeys.noMatches')" compact />

      <ul v-else class="mt-md space-y-md">
        <li v-for="k in filtered" :key="k.id" class="rounded-[10px] border border-line p-md">
          <div class="flex flex-wrap items-start justify-between gap-md">
            <div class="flex min-w-0 items-start gap-3">
              <BaseCheckbox
                :model-value="selected.has(k.id)"
                :aria-label="t('apiKeys.selectKey', k.name || t('apiKeys.unnamed'))"
                @update:model-value="(v) => toggleOne(k.id, v)"
              />
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="truncate text-title-sm text-txt">
                    {{ k.name || t('apiKeys.unnamed') }}
                  </span>
                  <StatusBadge v-if="k.migrated" :label="t('apiKeys.migrated')" tone="blue" />
                  <StatusBadge
                    v-if="!k.enabled"
                    :label="t('apiKeys.disabled')"
                    tone="gray"
                    dot
                  />
                  <StatusBadge
                    v-if="isOverLimit(k)"
                    :label="t('apiKeys.filterOverLimit')"
                    tone="red"
                    dot
                  />
                </div>
                <code class="mt-1 block truncate font-mono text-caption text-txt-tertiary">
                  {{ k.keyMasked }}
                </code>
                <p v-if="k.lastUsedAt" class="mt-1 text-caption-sm text-txt-tertiary">
                  {{ t('apiKeys.sortLastUsed') }}: {{ formatLocale(k.lastUsedAt) }}
                </p>
              </div>
            </div>

            <div class="flex shrink-0 items-center gap-xs">
              <BaseSwitch
                :model-value="Boolean(k.enabled)"
                @update:model-value="(v) => toggleKey(k, v)"
              />
              <BaseButton
                variant="ghost"
                size="xs"
                icon
                :title="t('apiKeys.actionEdit')"
                :aria-label="t('apiKeys.actionEdit')"
                @click="openEdit(k)"
              >
                <template #icon><PhPencilSimple :size="16" /></template>
              </BaseButton>
              <BaseButton
                variant="ghost"
                size="xs"
                :title="t('apiKeys.actionReset')"
                @click="resetUsage(k)"
              >
                {{ t('apiKeys.actionReset') }}
              </BaseButton>
              <BaseButton
                variant="danger"
                size="xs"
                icon
                :title="t('apiKeys.actionDelete')"
                :aria-label="t('apiKeys.actionDelete')"
                @click="removeKey(k)"
              >
                <template #icon><PhTrash :size="16" /></template>
              </BaseButton>
            </div>
          </div>

          <div class="mt-md grid gap-md sm:grid-cols-3">
            <UsageMeter
              :label="t('apiKeys.tokens')"
              :used="k.tokensUsed || 0"
              :limit="k.tokenLimit || 0"
              :value-text="limitText(k.tokensUsed || 0, k.tokenLimit || 0)"
            />
            <UsageMeter
              :label="t('apiKeys.credits')"
              :used="k.creditsUsed || 0"
              :limit="k.creditLimit || 0"
              :value-text="limitText(k.creditsUsed || 0, k.creditLimit || 0)"
            />
            <div>
              <p class="text-caption text-txt-tertiary">{{ t('apiKeys.requests') }}</p>
              <p class="tnum mt-1 text-title-sm text-txt">
                {{ formatNumber(k.requestsCount || 0) }}
              </p>
            </div>
          </div>
        </li>
      </ul>
    </SectionCard>

    <!-- create / edit -->
    <BaseModal
      v-model="formOpen"
      :title="editing ? t('apiKeys.modalTitleEdit') : t('apiKeys.modalTitleCreate')"
      size="lg"
    >
      <div class="space-y-md">
        <BaseField :label="t('apiKeys.formName')" v-slot="{ id }">
          <input
            :id="id"
            v-model="form.name"
            type="text"
            class="field"
            :placeholder="t('apiKeys.formNamePlaceholder')"
          />
        </BaseField>

        <BaseField
          :label="t('apiKeys.formKey')"
          :hint="editing ? '' : t('apiKeys.formKeyPlaceholder')"
          v-slot="{ id }"
        >
          <input
            :id="id"
            v-model="form.key"
            type="text"
            class="field font-mono"
            :readonly="Boolean(editing)"
            :placeholder="t('apiKeys.formKeyPlaceholder')"
          />
        </BaseField>

        <BaseSwitch v-model="form.enabled" :label="t('apiKeys.formEnabled')" />

        <div class="grid gap-md sm:grid-cols-2">
          <BaseField
            :label="t('apiKeys.limitTokens')"
            :hint="t('apiKeys.limitHint')"
            v-slot="{ id }"
          >
            <input :id="id" v-model="form.tokenLimit" type="number" min="0" class="field tnum" />
          </BaseField>
          <BaseField :label="t('apiKeys.limitCredits')" v-slot="{ id }">
            <input
              :id="id"
              v-model="form.creditLimit"
              type="number"
              min="0"
              step="0.01"
              class="field tnum"
            />
          </BaseField>
        </div>

        <BaseField
          :label="t('apiKeys.allowedIPs')"
          :hint="t('apiKeys.allowedIPsHint')"
          v-slot="{ id }"
        >
          <textarea
            :id="id"
            v-model="form.allowedIPs"
            class="field"
            rows="3"
            :placeholder="t('apiKeys.allowedIPsPlaceholder')"
          />
        </BaseField>

        <!-- per-model usage, read-only, edit mode only -->
        <div v-if="editing">
          <p class="field-label">{{ t('apiKeys.usageByModel') }}</p>
          <div v-if="usageRows.length" class="overflow-x-auto rounded-[10px] border border-line">
            <table class="data-table">
              <thead>
                <tr>
                  <th scope="col">{{ t('apiKeys.model') }}</th>
                  <th scope="col" class="text-right">{{ t('apiKeys.requests') }}</th>
                  <th scope="col" class="text-right">{{ t('apiKeys.tokens') }}</th>
                  <th scope="col" class="text-right">{{ t('apiKeys.credits') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in usageRows" :key="row.model">
                  <td class="font-mono text-caption">{{ row.model }}</td>
                  <td class="tnum text-right">{{ formatNumber(row.requests) }}</td>
                  <td class="tnum text-right">{{ formatNumber(row.tokens) }}</td>
                  <td class="tnum text-right">{{ formatNumber(row.credits) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <EmptyState v-else :message="t('apiKeys.noUsage')" compact />
        </div>
      </div>

      <template #footer>
        <BaseButton variant="glass" size="sm" @click="formOpen = false">
          {{ t('apiKeys.cancelBtn') }}
        </BaseButton>
        <BaseButton variant="primary" size="sm" :loading="saving" @click="saveForm">
          {{ t('apiKeys.saveBtn') }}
        </BaseButton>
      </template>
    </BaseModal>

    <!-- plaintext key, shown once -->
    <BaseModal v-model="createdOpen" :title="t('apiKeys.showTitle')" size="md">
      <p class="flex items-start gap-2 rounded-[10px] bg-[rgb(245_158_11/0.1)] p-3 text-caption text-warning">
        <PhWarning :size="16" class="mt-px shrink-0" />
        {{ t('apiKeys.showWarning') }}
      </p>
      <div class="mt-md flex items-center gap-sm">
        <input
          :value="createdKey"
          readonly
          class="field font-mono"
          :aria-label="t('apiKeys.formKey')"
          @focus="(e) => e.target.select()"
        />
        <CopyButton
          :value="createdKey"
          variant="glass"
          :label="t('apiKeys.copyBtn')"
          :success-message="t('apiKeys.copySuccess')"
        />
      </div>
      <template #footer>
        <BaseButton variant="primary" size="sm" @click="createdOpen = false">
          <template #icon><PhCheck :size="15" /></template>
          {{ t('apiKeys.closeBtn') }}
        </BaseButton>
      </template>
    </BaseModal>

    <!-- import -->
    <BaseModal v-model="importOpen" :title="t('apiKeys.importTitle')" size="md">
      <BaseField :hint="t('apiKeys.importHint')" v-slot="{ id }">
        <textarea
          :id="id"
          v-model="importText"
          class="field"
          rows="8"
          :placeholder="t('apiKeys.importPlaceholder')"
        />
      </BaseField>
      <template #footer>
        <BaseButton variant="glass" size="sm" @click="importOpen = false">
          {{ t('apiKeys.cancelBtn') }}
        </BaseButton>
        <BaseButton variant="primary" size="sm" :loading="importing" @click="runImport">
          {{ t('apiKeys.import') }}
        </BaseButton>
      </template>
    </BaseModal>
  </div>
</template>

<script setup>
// Accounts list: filters, batch operations, and the four account modals.
import { computed, onMounted, ref } from 'vue'
import {
  PhArrowsClockwise,
  PhDownloadSimple,
  PhEyeSlash,
  PhMagnifyingGlass,
  PhPlus,
  PhUsers,
} from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { confirm } from '@/lib/confirm'
import { copyText } from '@/lib/clipboard'
import { accountThroughput } from '@/lib/stats'
import { useDataStore } from '@/stores/data'
import { usePrefsStore } from '@/stores/prefs'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import BaseCheckbox from '@/components/ui/BaseCheckbox.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import AccountCard from '@/components/accounts/AccountCard.vue'
import AccountDetailModal from '@/components/accounts/AccountDetailModal.vue'
import AccountTestModal from '@/components/accounts/AccountTestModal.vue'
import AccountExportModal from '@/components/accounts/AccountExportModal.vue'
import AddAccountModal from '@/components/accounts/AddAccountModal.vue'

const { t } = useI18n()
const data = useDataStore()
const prefs = usePrefsStore()

const search = ref('')
const statusFilter = ref('all')
const tagFilter = ref('')
const selected = ref(new Set())
const busyIds = ref(new Set())
const batchRunning = ref(false)

const detailId = ref(null)
const testId = ref(null)
const exportOpen = ref(false)
const addOpen = ref(false)
const cacheStats = ref(null)

const statusOptions = computed(() => [
  { value: 'all', label: t('filter.all') },
  { value: 'enabled', label: t('filter.enabled') },
  { value: 'disabled', label: t('filter.disabled') },
  { value: 'banned', label: t('filter.banned') },
])

const tagOptions = computed(() => [
  { value: '', label: t('filter.allTags') },
  ...data.allTags.map((tag) => ({ value: tag, label: tag })),
])

/**
 * 每个账号的 RPM / TPM，由共享的日志尾部算出。
 *
 * 一次性算成 Map 而不是让每张卡各自遍历日志：日志尾部有 500 条，账号可能几十个，
 * 逐卡遍历是 O(账号数 x 日志数) 的重复扫描。
 */
const throughputMap = computed(() => accountThroughput(data.logs))

const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return data.accounts.filter((acc) => {
    if (needle && !(acc.email || '').toLowerCase().includes(needle)) return false

    const banned = acc.banStatus && acc.banStatus !== 'ACTIVE'
    if (statusFilter.value === 'enabled' && (!acc.enabled || banned)) return false
    if (statusFilter.value === 'disabled' && acc.enabled) return false
    if (statusFilter.value === 'banned' && !banned) return false

    if (tagFilter.value && !(acc.tags || []).includes(tagFilter.value)) return false
    return true
  })
})

const detailAccount = computed(() => (detailId.value ? data.findAccount(detailId.value) : null))
const testAccount = computed(() => (testId.value ? data.findAccount(testId.value) : null))

const selectedCount = computed(() => selected.value.size)
const allFilteredSelected = computed(
  () => filtered.value.length > 0 && filtered.value.every((a) => selected.value.has(a.id)),
)
const someSelected = computed(() => selectedCount.value > 0 && !allFilteredSelected.value)

function isBusy(id) {
  return busyIds.value.has(id)
}

function setBusy(id, value) {
  const next = new Set(busyIds.value)
  if (value) next.add(id)
  else next.delete(id)
  busyIds.value = next
}

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleSelectAll() {
  selected.value = allFilteredSelected.value ? new Set() : new Set(filtered.value.map((a) => a.id))
}

/** Selection is scoped to visible rows so a hidden row cannot be acted on. */
function selectedIds() {
  const visible = new Set(filtered.value.map((a) => a.id))
  return [...selected.value].filter((id) => visible.has(id))
}

async function refreshAccount(id) {
  setBusy(id, true)
  try {
    await api.refreshAccount(id)
    await data.loadAccounts()
    toast(t('common.saved'), 'success')
  } catch (err) {
    toast(err.message || t('accounts.refreshFailed'), 'error')
  } finally {
    setBusy(id, false)
  }
}

async function toggleAccount(account) {
  const next = !account.enabled
  setBusy(account.id, true)
  // Optimistic: the switch flips immediately and rolls back if the call fails.
  data.patchAccount(account.id, { enabled: next })
  try {
    await api.updateAccount(account.id, { enabled: next })
  } catch (err) {
    data.patchAccount(account.id, { enabled: !next })
    toast(err.message || t('common.failed'), 'error')
  } finally {
    setBusy(account.id, false)
  }
}

async function deleteAccount(account) {
  const ok = await confirm({
    message: t('accounts.confirmDelete'),
    confirmKey: 'accounts.delete',
    danger: true,
  })
  if (!ok) return
  setBusy(account.id, true)
  try {
    await api.deleteAccount(account.id)
    data.removeAccount(account.id)
    const next = new Set(selected.value)
    next.delete(account.id)
    selected.value = next
    toast(t('accounts.deleteSuccess'), 'success')
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    setBusy(account.id, false)
  }
}

async function copyAccountJson(account) {
  // The full record (with secrets) is fetched on demand; copyText keeps the user
  // gesture alive by handing the pending promise to the clipboard.
  const pending = api
    .accountFull(account.id)
    .then((full) =>
      JSON.stringify(
        {
          clientId: full?.clientId || '',
          clientSecret: full?.clientSecret || '',
          accessToken: full?.accessToken || '',
          refreshToken: full?.refreshToken || '',
        },
        null,
        2,
      ),
    )
  const ok = await copyText(pending)
  toast(ok ? t('accounts.copyJSONSuccess') : t('common.failed'), ok ? 'success' : 'error')
}

async function refreshAllModels() {
  const ok = await confirm({ message: t('models.confirmRefreshAll') })
  if (!ok) return
  const dismiss = toast(t('batch.processing'), 'info', { duration: 0 })
  try {
    const res = await api.refreshAllModels()
    toast(t('models.refreshAllDone', res?.refreshed ?? 0), 'success')
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    dismiss()
  }
}

/** Server-side batch for enable/disable/refresh. */
async function runServerBatch(action, confirmKey, resultFn) {
  const ids = selectedIds()
  if (!ids.length) return
  const ok = await confirm({ message: t(confirmKey, ids.length), danger: action === 'disable' })
  if (!ok) return

  batchRunning.value = true
  const dismiss = toast(t('batch.processing'), 'info', { duration: 0 })
  try {
    const res = await api.batchAccounts(ids, action)
    await data.loadAccounts()
    toast(resultFn(res, ids.length), 'success')
    selected.value = new Set()
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
  } finally {
    dismiss()
    batchRunning.value = false
  }
}

/**
 * Client-side fan-out for operations the server has no batch endpoint for.
 * Runs sequentially: a burst of parallel refreshes trips upstream rate limits.
 */
async function runClientBatch(ids, fn) {
  let done = 0
  let failed = 0
  for (const id of ids) {
    try {
      await fn(id)
      done += 1
    } catch {
      failed += 1
    }
  }
  return { done, failed }
}

const batchEnable = () =>
  runServerBatch('enable', 'batch.confirmEnable', (res, n) =>
    t('batch.enableResult', res?.count ?? n),
  )
const batchDisable = () =>
  runServerBatch('disable', 'batch.confirmDisable', (res, n) =>
    t('batch.disableResult', res?.count ?? n),
  )
const batchRefresh = () =>
  runServerBatch('refresh', 'batch.confirmRefresh', (res, n) =>
    t('batch.refreshResult', res?.refreshed ?? n, res?.failed ?? 0),
  )

async function batchRefreshModels() {
  const ids = selectedIds()
  if (!ids.length) return
  if (!(await confirm({ message: t('batch.confirmRefreshModels', ids.length) }))) return

  batchRunning.value = true
  const dismiss = toast(t('batch.processing'), 'info', { duration: 0 })
  try {
    const { done, failed } = await runClientBatch(ids, (id) => api.refreshAccountModels(id))
    toast(t('batch.refreshModelsResult', done, failed), failed ? 'warning' : 'success')
  } finally {
    dismiss()
    batchRunning.value = false
  }
}

async function batchDelete() {
  const ids = selectedIds()
  if (!ids.length) return
  if (!(await confirm({ message: t('batch.confirmDelete', ids.length), danger: true }))) return

  batchRunning.value = true
  const dismiss = toast(t('batch.deleting'), 'info', { duration: 0 })
  try {
    const { done, failed } = await runClientBatch(ids, (id) => api.deleteAccount(id))
    await data.loadAccounts()
    selected.value = new Set()
    toast(t('batch.deleteResult', done, failed), failed ? 'warning' : 'success')
  } finally {
    dismiss()
    batchRunning.value = false
  }
}

/** Cache metrics power the detail modal's cache grid; failure is non-fatal. */
async function loadCacheStats() {
  try {
    cacheStats.value = await api.stats()
  } catch {
    cacheStats.value = null
  }
}

function openDetail(id) {
  detailId.value = id
  loadCacheStats()
}

onMounted(() => {
  if (!data.accounts.length) data.loadAccounts().catch(() => {})
  // 卡片上的 RPM/TPM 来自日志尾部；账号页可能是用户的第一个落地页，
  // 此时 store 里还没有日志。
  if (!data.logs.length) data.loadLogs().catch(() => {})
})
</script>

<template>
  <div class="space-y-sm">
    <!-- ═══ 操作栏 ═══
         原先是「标题行 + 筛选行 + 批量行」三层堆叠，占掉近 200px 垂直空间，
         而其中标题「账号」与页面顶栏标题重复。这里压成一条：
         搜索框自适应占据剩余宽度，筛选与操作按钮右对齐同排；
         批量操作行仅在有选中项时出现，没选中时不占位。 -->
    <div class="glass-thin flex flex-wrap items-center gap-2 rounded-[14px] p-2">
      <!-- 全选：紧贴搜索框左侧，与列表项的复选框同一视觉列 -->
      <BaseCheckbox
        :model-value="allFilteredSelected"
        :indeterminate="someSelected"
        :aria-label="t('batch.selectAll')"
        :title="t('batch.selectAll')"
        class="ml-1 shrink-0"
        @update:model-value="toggleSelectAll"
      />

      <label class="relative min-w-[180px] flex-1">
        <span class="sr-only">{{ t('filter.search') }}</span>
        <PhMagnifyingGlass
          :size="15"
          class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-txt-tertiary"
        />
        <input
          v-model="search"
          type="search"
          class="field h-9 pl-8 text-body-sm"
          :placeholder="t('filter.search')"
        />
      </label>

      <BaseSelect
        v-model="statusFilter"
        :options="statusOptions"
        :aria-label="t('filter.status')"
        size="sm"
        class="w-[116px] shrink-0"
      />
      <BaseSelect
        v-model="tagFilter"
        :options="tagOptions"
        :aria-label="t('filter.tag')"
        size="sm"
        class="w-[116px] shrink-0"
      />

      <span class="hidden h-6 w-px shrink-0 bg-line sm:block" aria-hidden="true" />

      <!-- 低频操作收成图标按钮：文字标签在这一行里挤占的宽度超过其价值，
           全部保留 title/aria-label，可访问性不受影响。 -->
      <div class="flex shrink-0 items-center gap-0.5">
        <BaseButton
          variant="ghost"
          size="sm"
          icon
          :class="prefs.privacyMode && 'text-accent'"
          :title="`${t('privacy.label')} · ${t('privacy.tooltip')}`"
          :aria-label="t('privacy.label')"
          :aria-pressed="prefs.privacyMode"
          @click="prefs.togglePrivacy()"
        >
          <template #icon><PhEyeSlash :size="16" /></template>
        </BaseButton>
        <BaseButton
          variant="ghost"
          size="sm"
          icon
          :title="t('accounts.export')"
          :aria-label="t('accounts.export')"
          @click="exportOpen = true"
        >
          <template #icon><PhDownloadSimple :size="16" /></template>
        </BaseButton>
        <BaseButton
          variant="ghost"
          size="sm"
          icon
          :title="t('models.refreshAll')"
          :aria-label="t('models.refreshAll')"
          @click="refreshAllModels"
        >
          <template #icon><PhArrowsClockwise :size="16" /></template>
        </BaseButton>
      </div>

      <!-- 新增是主操作，保留文字 -->
      <BaseButton variant="primary" size="sm" class="shrink-0" @click="addOpen = true">
        <template #icon><PhPlus :size="15" /></template>
        {{ t('accounts.add') }}
      </BaseButton>
    </div>

    <!-- 批量操作：仅在有选中项时占位 -->
    <div
      v-if="selectedCount"
      class="glass-thin flex flex-wrap items-center gap-1.5 rounded-[14px] p-2"
    >
      <span class="mr-1 ml-1.5 text-caption tnum text-accent">
        {{ t('batch.selected', selectedCount) }}
      </span>
      <BaseButton variant="primary" size="xs" :disabled="batchRunning" @click="batchEnable">
        {{ t('batch.enable') }}
      </BaseButton>
      <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchDisable">
        {{ t('batch.disable') }}
      </BaseButton>
      <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchRefresh">
        {{ t('batch.refresh') }}
      </BaseButton>
      <BaseButton variant="glass" size="xs" :disabled="batchRunning" @click="batchRefreshModels">
        {{ t('batch.refreshModels') }}
      </BaseButton>
      <BaseButton
        variant="danger"
        size="xs"
        class="ml-auto"
        :disabled="batchRunning"
        @click="batchDelete"
      >
        {{ t('batch.delete') }}
      </BaseButton>
    </div>

    <!-- list -->
    <div
      v-if="data.loadingAccounts && !data.accounts.length"
      class="grid gap-sm sm:grid-cols-2 laptop:grid-cols-3 desktop:grid-cols-4"
    >
      <div v-for="i in 8" :key="i" class="skeleton h-[300px] rounded-2xl" />
    </div>

    <EmptyState v-else-if="!filtered.length" :message="t('accounts.empty')">
      <template #icon><PhUsers :size="32" /></template>
      <template #action>
        <BaseButton variant="primary" size="sm" @click="addOpen = true">
          <template #icon><PhPlus :size="15" /></template>
          {{ t('accounts.add') }}
        </BaseButton>
      </template>
    </EmptyState>

    <TransitionGroup
      v-else
      name="list"
      tag="div"
      class="grid gap-sm sm:grid-cols-2 laptop:grid-cols-3 desktop:grid-cols-4"
    >
      <AccountCard
        v-for="acc in filtered"
        :key="acc.id"
        :account="acc"
        :selected="selected.has(acc.id)"
        :privacy="prefs.privacyMode"
        :busy="isBusy(acc.id)"
        :throughput="throughputMap.get(acc.id) || null"
        @toggle-select="toggleSelect(acc.id)"
        @refresh="refreshAccount(acc.id)"
        @detail="openDetail(acc.id)"
        @copy-json="copyAccountJson(acc)"
        @toggle="toggleAccount(acc)"
        @test="testId = acc.id"
        @delete="deleteAccount(acc)"
      />
    </TransitionGroup>

    <!-- modals -->
    <AccountDetailModal
      :open="!!detailAccount"
      :account="detailAccount"
      :privacy="prefs.privacyMode"
      :cache="cacheStats"
      @close="detailId = null"
      @saved="data.loadAccounts()"
    />
    <AccountTestModal
      :open="!!testAccount"
      :account="testAccount"
      :privacy="prefs.privacyMode"
      @close="testId = null"
    />
    <AccountExportModal
      :open="exportOpen"
      :accounts="data.accounts"
      :privacy="prefs.privacyMode"
      @close="exportOpen = false"
    />
    <AddAccountModal
      :open="addOpen"
      @close="addOpen = false"
      @imported="data.loadAccounts()"
    />
  </div>
</template>

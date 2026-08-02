// Core data store: status/stats, accounts, logs, version.
//
// These four resources are shared across views (the dashboard, accounts list and
// logs table all read from them), so they live in one store with a single
// polling loop rather than being refetched per view.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/lib/api'

const STATS_POLL_MS = 10_000
const LOGS_POLL_MS = 5_000

export const useDataStore = defineStore('data', () => {
  const status = ref(null)
  const accounts = ref([])
  const logs = ref([])
  const version = ref('')

  const loadingAccounts = ref(false)
  const loadingLogs = ref(false)
  const accountsError = ref('')

  let statsTimer = null
  let logsTimer = null

  const totalAccounts = computed(() => accounts.value.length)
  const availableAccounts = computed(() => Number(status.value?.available ?? 0))

  /** Union of every tag across accounts, sorted — drives the tag filter. */
  const allTags = computed(() => {
    const set = new Set()
    for (const acc of accounts.value) {
      for (const tag of acc.tags || []) if (tag) set.add(tag)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  })

  async function loadStatus() {
    status.value = await api.status()
    if (status.value?.version) version.value = status.value.version
  }

  async function loadAccounts() {
    loadingAccounts.value = true
    accountsError.value = ''
    try {
      const res = await api.accounts()
      accounts.value = Array.isArray(res) ? res : []
    } catch (err) {
      accountsError.value = err.message || 'failed'
      throw err
    } finally {
      loadingAccounts.value = false
    }
  }

  async function loadLogs() {
    loadingLogs.value = true
    try {
      const res = await api.logs()
      logs.value = Array.isArray(res?.logs) ? res.logs : []
    } finally {
      loadingLogs.value = false
    }
  }

  async function loadVersion() {
    try {
      const res = await api.version()
      if (res?.version) version.value = res.version
    } catch {
      /* non-critical */
    }
  }

  /** Patches one account in place so a toggle does not require a full refetch. */
  function patchAccount(id, patch) {
    const idx = accounts.value.findIndex((a) => a.id === id)
    if (idx >= 0) accounts.value[idx] = { ...accounts.value[idx], ...patch }
  }

  function removeAccount(id) {
    accounts.value = accounts.value.filter((a) => a.id !== id)
  }

  function findAccount(id) {
    return accounts.value.find((a) => a.id === id) || null
  }

  /** Initial parallel load after login. */
  async function loadAll() {
    const results = await Promise.allSettled([
      loadStatus(),
      loadAccounts(),
      loadVersion(),
      loadLogs(),
    ])
    return results
  }

  function startStatsPolling() {
    if (statsTimer) return
    statsTimer = setInterval(() => {
      loadStatus().catch(() => {})
    }, STATS_POLL_MS)
  }

  function stopStatsPolling() {
    if (statsTimer) clearInterval(statsTimer)
    statsTimer = null
  }

  function startLogsPolling() {
    if (logsTimer) return
    logsTimer = setInterval(() => {
      loadLogs().catch(() => {})
    }, LOGS_POLL_MS)
  }

  function stopLogsPolling() {
    if (logsTimer) clearInterval(logsTimer)
    logsTimer = null
  }

  function reset() {
    stopStatsPolling()
    stopLogsPolling()
    status.value = null
    accounts.value = []
    logs.value = []
  }

  return {
    status,
    accounts,
    logs,
    version,
    loadingAccounts,
    loadingLogs,
    accountsError,
    totalAccounts,
    availableAccounts,
    allTags,
    loadStatus,
    loadAccounts,
    loadLogs,
    loadVersion,
    loadAll,
    patchAccount,
    removeAccount,
    findAccount,
    startStatsPolling,
    stopStatsPolling,
    startLogsPolling,
    stopLogsPolling,
    reset,
  }
})

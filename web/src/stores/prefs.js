// UI preferences that persist across sessions but are not server state.
import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

const K_PRIVACY = 'privacyMode'
const K_SIDEBAR = 'kiro_sidebar_collapsed'

function readBool(key, fallback) {
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return fallback
    return raw === 'true' || raw === '1'
  } catch {
    return fallback
  }
}

function writeBool(key, value) {
  try {
    localStorage.setItem(key, value ? 'true' : 'false')
  } catch {
    /* ignore */
  }
}

export const usePrefsStore = defineStore('prefs', () => {
  // Privacy mode masks account emails. Defaults to ON, matching the legacy panel.
  const privacyMode = ref(readBool(K_PRIVACY, true))
  const sidebarCollapsed = ref(readBool(K_SIDEBAR, false))

  watch(privacyMode, (v) => writeBool(K_PRIVACY, v))
  watch(sidebarCollapsed, (v) => writeBool(K_SIDEBAR, v))

  function togglePrivacy() {
    privacyMode.value = !privacyMode.value
  }

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  return { privacyMode, sidebarCollapsed, togglePrivacy, toggleSidebar }
})

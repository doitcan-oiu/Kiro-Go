<script setup>
// Root component: decides between the login screen and the authenticated shell,
// and hosts the global toast stack.
import { onMounted, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useSessionStore } from '@/stores/session'
import { useDataStore } from '@/stores/data'
import { applyDocumentLang, lang } from '@/lib/i18n'
import ToastHost from '@/components/ui/ToastHost.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import LoginView from '@/views/LoginView.vue'
import AppShell from '@/components/layout/AppShell.vue'
import BootSplash from '@/components/layout/BootSplash.vue'

const session = useSessionStore()
const data = useDataStore()
const { authenticated, booting } = storeToRefs(session)

onMounted(() => {
  session.tryAutoLogin()
})

// Document title / <html lang> follow the language switch.
watch(lang, applyDocumentLang)

// Entering the shell kicks off the initial load and the stats poll; leaving it
// tears both down so a logged-out tab stops hitting the API.
watch(
  authenticated,
  (isIn) => {
    if (isIn) {
      data.loadAll()
      data.startStatsPolling()
    } else {
      data.stopStatsPolling()
      data.stopLogsPolling()
      data.reset()
    }
  },
  { immediate: true },
)
</script>

<template>
  <BootSplash v-if="booting" />
  <AppShell v-else-if="authenticated" />
  <LoginView v-else />

  <ConfirmDialog />
  <ToastHost />
</template>

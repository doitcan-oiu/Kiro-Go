<script setup>
// Credential export. POST /export returns the selected accounts with their
// secrets; only the four credential fields are copied/downloaded.
import { computed, ref, watch } from 'vue'
import { PhCopy, PhDownloadSimple, PhEye } from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { copyText, downloadJson } from '@/lib/clipboard'
import { maskEmail, todayStamp } from '@/lib/format'
import { toast } from '@/lib/toast'
import BaseModal from '@/components/ui/BaseModal.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseCheckbox from '@/components/ui/BaseCheckbox.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  accounts: { type: Array, default: () => [] },
  privacy: { type: Boolean, default: true },
})

const emit = defineEmits(['close'])

const { t } = useI18n()

const selected = ref(new Set())
const preview = ref('')
const busy = ref(false)

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    // Default to everything selected — exporting one account is the rare case.
    selected.value = new Set(props.accounts.map((a) => a.id))
    preview.value = ''
  },
)

const ids = computed(() => [...selected.value])
const allSelected = computed(
  () => props.accounts.length > 0 && selected.value.size === props.accounts.length,
)

function toggle(id, on) {
  const next = new Set(selected.value)
  if (on) next.add(id)
  else next.delete(id)
  selected.value = next
}

function toggleAll() {
  selected.value = allSelected.value ? new Set() : new Set(props.accounts.map((a) => a.id))
}

/** Reduces the server payload to just the credential quartet. */
function slimCredentials(payload) {
  const list = Array.isArray(payload?.accounts) ? payload.accounts : []
  return {
    accounts: list.map((entry) => {
      const c = entry?.credentials || {}
      return {
        credentials: {
          clientId: c.clientId || '',
          clientSecret: c.clientSecret || '',
          accessToken: c.accessToken || '',
          refreshToken: c.refreshToken || '',
        },
      }
    }),
  }
}

async function fetchExport() {
  if (!ids.value.length) {
    toast(t('export.noSelection'), 'warning')
    return null
  }
  busy.value = true
  try {
    return await api.exportAccounts(ids.value)
  } catch (err) {
    toast(err.message || t('common.failed'), 'error')
    return null
  } finally {
    busy.value = false
  }
}

async function showJson() {
  const payload = await fetchExport()
  if (payload) preview.value = JSON.stringify(slimCredentials(payload), null, 2)
}

async function copyJson() {
  const payload = await fetchExport()
  if (!payload) return
  const ok = await copyText(JSON.stringify(slimCredentials(payload), null, 2))
  toast(ok ? t('export.copied') : t('common.failed'), ok ? 'success' : 'error')
}

async function download() {
  const payload = await fetchExport()
  if (!payload) return
  // The download keeps the server's full payload, unlike copy/preview: a backup
  // is only useful if it can be re-imported.
  downloadJson(`kiro-accounts-${todayStamp()}.json`, payload)
}
</script>

<template>
  <BaseModal :open="open" :title="t('export.title')" size="lg" @close="emit('close')">
    <div class="space-y-md">
      <div class="flex flex-wrap items-center justify-between gap-sm">
        <p class="text-body-sm text-txt-secondary">{{ t('export.selected', selected.size) }}</p>
        <BaseButton variant="glass" size="xs" @click="toggleAll">
          {{ allSelected ? t('export.deselectAll') : t('export.selectAll') }}
        </BaseButton>
      </div>

      <ul class="max-h-[280px] divide-y divide-divider overflow-y-auto rounded-[10px] bg-surface-input">
        <li v-for="acc in accounts" :key="acc.id" class="flex items-center gap-3 px-md py-2.5">
          <BaseCheckbox
            :model-value="selected.has(acc.id)"
            :aria-label="t('accounts.selectAccount', acc.email || acc.id)"
            @update:model-value="(v) => toggle(acc.id, v)"
          />
          <div class="min-w-0 flex-1">
            <p class="truncate text-body-sm text-txt">
              {{ privacy ? maskEmail(acc.email) : acc.email || acc.id }}
            </p>
            <p class="truncate text-caption-sm text-txt-tertiary">
              {{ [acc.authMethod, acc.subscriptionType].filter(Boolean).join(' · ') || '—' }}
            </p>
          </div>
        </li>
      </ul>

      <pre
        v-if="preview"
        class="max-h-[240px] overflow-auto rounded-[10px] bg-bg-sunken p-3 font-mono text-caption leading-relaxed text-txt-secondary"
      >{{ preview }}</pre>
    </div>

    <template #footer>
      <BaseButton variant="glass" size="sm" @click="emit('close')">
        {{ t('common.cancel') }}
      </BaseButton>
      <BaseButton variant="glass" size="sm" :loading="busy" @click="showJson">
        <template #icon><PhEye :size="15" /></template>
        {{ t('export.showJson') }}
      </BaseButton>
      <BaseButton variant="glass" size="sm" :loading="busy" @click="copyJson">
        <template #icon><PhCopy :size="15" /></template>
        {{ t('export.copyJson') }}
      </BaseButton>
      <BaseButton variant="primary" size="sm" :loading="busy" @click="download">
        <template #icon><PhDownloadSimple :size="15" /></template>
        {{ t('export.downloadJson') }}
      </BaseButton>
    </template>
  </BaseModal>
</template>

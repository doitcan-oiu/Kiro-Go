<script setup>
// Account connectivity test: picks a model, fires POST /accounts/{id}/test and
// streams the outcome into a small log pane.
import { computed, nextTick, ref, watch } from 'vue'
import { PhTrash } from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { maskEmail } from '@/lib/format'
import BaseModal from '@/components/ui/BaseModal.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  account: { type: Object, default: null },
  privacy: { type: Boolean, default: true },
})

const emit = defineEmits(['close'])

const { t } = useI18n()

const DEFAULT_MODEL = 'claude-sonnet-4'
const MAX_LINES = 100

const models = ref([])
const modelsState = ref('loading') // loading | ready | fallback
const model = ref(DEFAULT_MODEL)
const running = ref(false)
const lines = ref([])
const logEl = ref(null)

const acc = computed(() => props.account || {})

const displayEmail = computed(() =>
  props.privacy ? maskEmail(acc.value.email) : acc.value.email || '—',
)

const proxyLabel = computed(() => acc.value.proxyURL || t('accounts.testLog.globalProxy'))

const statusLabel = computed(() => {
  if (modelsState.value === 'loading') return t('accounts.testModelsLoading')
  if (modelsState.value === 'fallback') return t('accounts.testModelsFallback')
  return t('accounts.testModelsReady', models.value.length)
})

const statusTone = computed(() =>
  modelsState.value === 'ready' ? 'green' : modelsState.value === 'loading' ? 'gray' : 'yellow',
)

watch(
  () => [props.open, acc.value.id],
  ([isOpen]) => {
    if (!isOpen) return
    lines.value = []
    running.value = false
    model.value = DEFAULT_MODEL
    loadModels()
  },
  { immediate: true },
)

async function loadModels() {
  modelsState.value = 'loading'
  try {
    const res = await api.accountModelsCached(acc.value.id)
    const list = Array.isArray(res?.models) ? res.models.filter(Boolean).sort() : []
    if (list.length) {
      models.value = list
      // Prefer the conventional default when the account actually offers it.
      model.value = list.includes(DEFAULT_MODEL) ? DEFAULT_MODEL : list[0]
      modelsState.value = 'ready'
    } else {
      models.value = []
      modelsState.value = 'fallback'
    }
  } catch {
    models.value = []
    modelsState.value = 'fallback'
  }
}

async function pushLine(text, kind = 'info') {
  lines.value.push({ id: `${Date.now()}-${lines.value.length}`, text, kind })
  if (lines.value.length > MAX_LINES) lines.value.splice(0, lines.value.length - MAX_LINES)
  await nextTick()
  if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
}

async function run() {
  const chosen = (model.value || '').trim() || DEFAULT_MODEL
  running.value = true
  const startedAt = Date.now()
  pushLine(t('accounts.testLog.start', displayEmail.value, chosen, proxyLabel.value))
  try {
    const res = await api.testAccount(acc.value.id, chosen)
    const elapsed = Date.now() - startedAt
    if (res?.success) {
      pushLine(
        t('accounts.testLog.success', displayEmail.value, `${elapsed}ms`, res.reply || ''),
        'ok',
      )
    } else {
      pushLine(
        t('accounts.testLog.failed', displayEmail.value, `${elapsed}ms`, res?.error || ''),
        'err',
      )
    }
  } catch (err) {
    pushLine(t('accounts.testLog.error', displayEmail.value, err.message || ''), 'err')
  } finally {
    running.value = false
  }
}

const lineClass = {
  ok: 'text-accent',
  err: 'text-error',
  info: 'text-txt-secondary',
}
</script>

<template>
  <BaseModal :open="open" :title="t('accounts.testModalTitle')" size="lg" @close="emit('close')">
    <div v-if="account" class="space-y-md">
      <!-- target summary -->
      <div class="flex flex-wrap items-center gap-x-lg gap-y-2 rounded-[10px] bg-surface-input px-md py-3">
        <div class="min-w-0">
          <p class="text-caption-sm text-txt-tertiary">{{ t('detail.email') }}</p>
          <p class="truncate text-body-sm text-txt">{{ displayEmail }}</p>
        </div>
        <div class="min-w-0">
          <p class="text-caption-sm text-txt-tertiary">{{ t('detail.authMethod') }}</p>
          <p class="truncate text-body-sm text-txt">
            {{ account.provider || account.authMethod || '—' }}
          </p>
        </div>
        <div class="min-w-0">
          <p class="text-caption-sm text-txt-tertiary">{{ t('detail.proxyURL') }}</p>
          <p class="truncate text-body-sm text-txt">{{ proxyLabel }}</p>
        </div>
      </div>

      <!-- model choice -->
      <BaseField :label="t('accounts.selectModel')">
        <template #default="{ id: fid }">
          <div class="flex flex-wrap items-center gap-sm">
            <div class="min-w-[220px] flex-1">
              <BaseSelect
                v-if="modelsState === 'ready'"
                v-model="model"
                :options="models"
                :aria-label="t('accounts.selectModel')"
              />
              <input
                v-else
                :id="fid"
                v-model="model"
                type="text"
                class="field font-mono text-body-sm"
                :placeholder="DEFAULT_MODEL"
              />
            </div>
            <StatusBadge :label="statusLabel" :tone="statusTone" dot />
          </div>
        </template>
      </BaseField>

      <!-- log -->
      <div>
        <div class="mb-2 flex items-center justify-between gap-sm">
          <h3 class="text-caption font-semibold tracking-wide text-txt-tertiary uppercase">
            {{ t('accounts.testLog.title') }}
          </h3>
          <BaseButton
            variant="ghost"
            size="xs"
            :disabled="!lines.length"
            @click="lines = []"
          >
            <template #icon><PhTrash :size="14" /></template>
            {{ t('accounts.testLog.clear') }}
          </BaseButton>
        </div>
        <div
          ref="logEl"
          class="max-h-[240px] min-h-[120px] overflow-y-auto rounded-[10px] bg-bg-sunken p-3 font-mono text-caption leading-relaxed"
          role="log"
          aria-live="polite"
        >
          <p v-if="!lines.length" class="text-txt-tertiary">{{ t('accounts.testLog.empty') }}</p>
          <p
            v-for="line in lines"
            :key="line.id"
            class="break-words whitespace-pre-wrap"
            :class="lineClass[line.kind]"
          >
            {{ line.text }}
          </p>
        </div>
      </div>
    </div>

    <template #footer>
      <BaseButton variant="glass" size="sm" @click="emit('close')">
        {{ t('common.close') }}
      </BaseButton>
      <BaseButton variant="primary" size="sm" :loading="running" @click="run">
        {{ t('accounts.test') }}
      </BaseButton>
    </template>
  </BaseModal>
</template>

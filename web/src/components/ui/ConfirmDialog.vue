<script setup>
// Host for the global confirm service. Mounted once in App.vue.
import { computed } from 'vue'
import { PhWarning } from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { confirmState, settleConfirm } from '@/lib/confirm'
import BaseModal from '@/components/ui/BaseModal.vue'
import BaseButton from '@/components/ui/BaseButton.vue'

const { t } = useI18n()

const state = computed(() => confirmState.value)
</script>

<template>
  <BaseModal
    :open="state.open"
    :title="state.title || t('common.confirm')"
    size="sm"
    @close="settleConfirm(false)"
  >
    <div class="flex gap-3">
      <div
        class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full"
        :class="state.danger ? 'bg-error/12 text-error' : 'bg-accent-soft text-accent'"
        aria-hidden="true"
      >
        <PhWarning :size="20" />
      </div>
      <p class="text-body pt-1.5 leading-relaxed whitespace-pre-line text-txt">
        {{ state.message }}
      </p>
    </div>

    <template #footer>
      <BaseButton variant="glass" size="sm" @click="settleConfirm(false)">
        {{ t(state.cancelKey) }}
      </BaseButton>
      <BaseButton
        :variant="state.danger ? 'danger' : 'primary'"
        size="sm"
        @click="settleConfirm(true)"
      >
        {{ t(state.confirmKey) }}
      </BaseButton>
    </template>
  </BaseModal>
</template>

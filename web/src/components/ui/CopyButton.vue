<script setup>
// Copy-to-clipboard button with an inline success state.
//
// `value` may be a string, a Promise<string>, or a function returning either —
// the function form lets callers defer an expensive/authenticated fetch until
// the click actually happens (see copyText for why the Promise is not awaited
// before handing it to the clipboard API).
import { onBeforeUnmount, ref } from 'vue'
import { PhCheck, PhCopy } from '@phosphor-icons/vue'
import { copyText } from '@/lib/clipboard'
import { useI18n } from '@/lib/i18n'
import { toast } from '@/lib/toast'
import BaseButton from '@/components/ui/BaseButton.vue'

const props = defineProps({
  value: { type: [String, Object, Function, Promise], default: '' },
  /** Visible label; omit for an icon-only button. */
  label: { type: String, default: '' },
  size: { type: String, default: 'sm' },
  variant: { type: String, default: 'ghost' },
  ariaLabel: { type: String, default: '' },
  /** Toast shown on success; set to '' to stay silent. */
  successMessage: { type: String, default: null },
})

const { t } = useI18n()
const done = ref(false)
let timer = null

onBeforeUnmount(() => clearTimeout(timer))

async function onClick() {
  const source = typeof props.value === 'function' ? props.value() : props.value
  const ok = await copyText(source)
  if (!ok) {
    toast(t('common.failed'), 'error')
    return
  }
  const message = props.successMessage === null ? t('common.copied') : props.successMessage
  if (message) toast(message, 'success')

  done.value = true
  clearTimeout(timer)
  timer = setTimeout(() => {
    done.value = false
  }, 1400)
}

const iconSize = 15
</script>

<template>
  <BaseButton
    :variant="variant"
    :size="size"
    :title="ariaLabel || label || t('common.copy')"
    :aria-label="ariaLabel || label || t('common.copy')"
    @click="onClick"
  >
    <template #icon>
      <PhCheck v-if="done" :size="iconSize" class="text-accent" />
      <PhCopy v-else :size="iconSize" />
    </template>
    <span v-if="label">{{ label }}</span>
  </BaseButton>
</template>

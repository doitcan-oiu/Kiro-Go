<script setup>
// zh/en segmented control. Two options only, so a radiogroup is clearer to
// assistive tech than a menu.
import { LANGS, useI18n } from '@/lib/i18n'

const { t, lang, setLang } = useI18n()
</script>

<template>
  <div
    class="glass-clear inline-flex items-center gap-0.5 rounded-full p-0.5"
    role="radiogroup"
    :aria-label="t('lang.label')"
  >
    <button
      v-for="code in LANGS"
      :key="code"
      type="button"
      role="radio"
      :aria-checked="lang === code"
      class="rounded-full px-2.5 py-1 text-caption font-medium transition-colors duration-[var(--dur-fast)]"
      :class="
        lang === code
          ? 'bg-accent text-txt-inverse'
          : 'text-txt-tertiary hover:text-txt-secondary'
      "
      @click="setLang(code)"
    >
      {{ t(`lang.${code}`) }}
    </button>
  </div>
</template>

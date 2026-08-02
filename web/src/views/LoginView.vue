<script setup>
// Login. There is no login endpoint — the entered password is probed against
// GET /status and kept if the server accepts it (see stores/session.js).
import { onMounted, ref, useTemplateRef } from 'vue'
import { PhEye, PhEyeSlash, PhLockKey, PhSignIn } from '@phosphor-icons/vue'
import { useSessionStore } from '@/stores/session'
import { useI18n } from '@/lib/i18n'
import BaseButton from '@/components/ui/BaseButton.vue'
import LangSwitch from '@/components/layout/LangSwitch.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'

const { t } = useI18n()
const session = useSessionStore()

const revealed = ref(false)
const passwordInput = useTemplateRef('passwordInput')

onMounted(() => passwordInput.value?.focus())

function submit() {
  if (!session.canSubmit) return
  session.login()
}
</script>

<template>
  <div class="relative flex min-h-dvh flex-col bg-bg">
    <!-- Decorative accent wash; purely visual so it is hidden from AT. -->
    <div
      class="pointer-events-none absolute inset-0 overflow-hidden"
      aria-hidden="true"
    >
      <div
        class="absolute -top-40 left-1/2 h-[520px] w-[820px] -translate-x-1/2 rounded-full opacity-[0.14] blur-[120px]"
        style="background: radial-gradient(circle, var(--accent-primary), transparent 70%)"
      />
      <div
        class="absolute -bottom-52 -left-32 h-[420px] w-[560px] rounded-full opacity-[0.08] blur-[120px]"
        style="background: radial-gradient(circle, var(--accent-secondary), transparent 70%)"
      />
    </div>

    <header class="relative flex h-[var(--topbar-h)] items-center justify-between px-lg">
      <span class="font-brand text-[18px] font-bold tracking-tight text-txt">
        Kiro<span class="text-accent">-Go</span>
      </span>
      <div class="flex items-center gap-sm">
        <LangSwitch />
        <ThemeToggle />
      </div>
    </header>

    <main class="relative flex flex-1 items-center justify-center px-md pb-2xl">
      <div class="glass-regular w-full max-w-[420px] rounded-[20px] p-xl shadow-[var(--sh-lg)]">
        <div class="mb-xl flex flex-col items-center gap-sm text-center">
          <span
            class="flex size-12 items-center justify-center rounded-2xl bg-accent-soft text-accent"
          >
            <PhLockKey :size="24" aria-hidden="true" />
          </span>
          <h1 class="font-brand text-title-lg text-txt">{{ t('login.title') }}</h1>
          <p class="text-body-sm text-txt-tertiary">{{ t('login.subtitle') }}</p>
        </div>

        <form class="flex flex-col gap-md" @submit.prevent="submit">
          <div>
            <label for="login-password" class="field-label">{{ t('login.password') }}</label>
            <div class="relative">
              <input
                id="login-password"
                ref="passwordInput"
                v-model="session.passwordDraft"
                :type="revealed ? 'text' : 'password'"
                class="field pr-11"
                :class="session.error && 'field-error'"
                :placeholder="t('login.passwordPlaceholder')"
                autocomplete="current-password"
                :aria-invalid="Boolean(session.error) || undefined"
                aria-describedby="login-error"
              />
              <button
                type="button"
                class="absolute inset-y-0 right-0 grid w-11 place-items-center rounded-r-[6px] text-txt-tertiary transition-colors hover:text-txt"
                :aria-label="revealed ? t('login.hidePassword') : t('login.showPassword')"
                :title="revealed ? t('login.hidePassword') : t('login.showPassword')"
                @click="revealed = !revealed"
              >
                <component :is="revealed ? PhEyeSlash : PhEye" :size="18" />
              </button>
            </div>
          </div>

          <label class="flex cursor-pointer items-center gap-sm text-body-sm text-txt-secondary">
            <input
              type="checkbox"
              class="size-4 accent-[var(--accent-primary)]"
              :checked="session.rememberMe"
              @change="session.setRemember($event.target.checked)"
            />
            {{ t('login.remember') }}
          </label>

          <!-- aria-live so a screen reader announces a failed attempt (§12). -->
          <p
            id="login-error"
            class="min-h-[18px] text-body-sm text-error"
            role="status"
            aria-live="polite"
          >
            {{ session.error }}
          </p>

          <BaseButton
            type="submit"
            variant="primary"
            :loading="session.loggingIn"
            :disabled="!session.canSubmit"
            class="w-full"
          >
            <template #icon><PhSignIn :size="18" /></template>
            {{ t('login.submit') }}
          </BaseButton>
        </form>
      </div>
    </main>
  </div>
</template>

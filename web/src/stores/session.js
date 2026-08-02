// Session store: login / auto-login / logout.
//
// There is no login endpoint on the Go side. Authentication is a single
// `X-Admin-Password` header validated on every request, so "logging in" means
// probing `GET /status` with a candidate password and keeping it if it works.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, isAuthError } from '@/lib/api'
import { credentials } from '@/lib/credentials'
import { t } from '@/lib/i18n'

export const useSessionStore = defineStore('session', () => {
  const authenticated = ref(false)
  /** True until the initial auto-login probe settles, so we can hold the paint. */
  const booting = ref(true)
  const loggingIn = ref(false)
  const error = ref('')

  const rememberMe = ref(credentials.rememberMe.value)
  const passwordDraft = ref(credentials.rememberedPassword.value)

  const canSubmit = computed(() => passwordDraft.value.length > 0 && !loggingIn.value)

  async function login() {
    const password = passwordDraft.value
    if (!password) return false
    loggingIn.value = true
    error.value = ''

    // The header is read from `credentials.password`, so the candidate has to be
    // installed before probing. It is rolled back if the probe fails.
    const previous = credentials.password.value
    credentials.password.value = password
    try {
      await api.status()
      credentials.set(password, rememberMe.value)
      authenticated.value = true
      return true
    } catch (err) {
      credentials.password.value = previous
      // A wrong password is a 401; anything else is a connectivity problem.
      error.value = isAuthError(err) ? t('login.error') : t('login.connectError')
      return false
    } finally {
      loggingIn.value = false
    }
  }

  /**
   * Restores a stored session on boot. Silent by design: a stale password should
   * land the user on the login screen, not raise an error toast.
   */
  async function tryAutoLogin() {
    try {
      if (!credentials.isFresh()) {
        // Expired or absent. Drop the stale copy but keep the remembered value
        // so the login field stays prefilled.
        if (credentials.password.value) credentials.clear()
        return false
      }
      await api.status()
      authenticated.value = true
      return true
    } catch {
      credentials.clear()
      return false
    } finally {
      booting.value = false
      passwordDraft.value = credentials.rememberedPassword.value || passwordDraft.value
      rememberMe.value = credentials.rememberMe.value
    }
  }

  function logout() {
    const remembered = credentials.rememberedPassword.value
    credentials.clear()
    authenticated.value = false
    // Keep the field prefilled if the user had asked to be remembered.
    passwordDraft.value = remembered
  }

  /** Called after a password change so the active credential stays valid. */
  function adoptPassword(password) {
    credentials.set(password, rememberMe.value)
    passwordDraft.value = credentials.rememberedPassword.value
  }

  function setRemember(value) {
    rememberMe.value = Boolean(value)
  }

  return {
    authenticated,
    booting,
    loggingIn,
    error,
    rememberMe,
    passwordDraft,
    canSubmit,
    login,
    tryAutoLogin,
    logout,
    adoptPassword,
    setRemember,
  }
})

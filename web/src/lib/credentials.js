// Admin credential storage.
//
// Kept deliberately separate from the Pinia auth store so `api.js` can read the
// active password without importing a store (which would create a cycle:
// store -> api -> store).
//
// Storage contract carried over from the legacy panel so an existing session
// keeps working across the rewrite:
//   admin_password       session + local - the active password
//   admin_login_time     session + local - ms epoch, drives the 72h window
//   kiro_remember        local - '1' when "remember me" is on
//   kiro_remembered_pwd  local - value prefilled into the login field
import { ref } from 'vue'

const K_PWD = 'admin_password'
const K_TIME = 'admin_login_time'
const K_REMEMBER = 'kiro_remember'
const K_REMEMBERED_PWD = 'kiro_remembered_pwd'

/** Session validity window; matches the legacy panel's 72 hours. */
export const SESSION_MAX_AGE_MS = 72 * 60 * 60 * 1000

// localStorage throws in private-mode Safari and when storage is full. None of
// this is load-bearing, so every access degrades to a no-op.
function readLocal(key) {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}
function writeLocal(key, value) {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* ignore */
  }
}
function dropLocal(key) {
  try {
    localStorage.removeItem(key)
  } catch {
    /* ignore */
  }
}
function readSession(key) {
  try {
    return sessionStorage.getItem(key)
  } catch {
    return null
  }
}
function writeSession(key, value) {
  try {
    sessionStorage.setItem(key, value)
  } catch {
    /* ignore */
  }
}
function dropSession(key) {
  try {
    sessionStorage.removeItem(key)
  } catch {
    /* ignore */
  }
}

// Boot purge: without "remember me" the persisted copy must not survive a
// browser restart. Runs before the initial read below.
if (readLocal(K_REMEMBER) !== '1') {
  dropLocal(K_PWD)
  dropLocal(K_TIME)
}

const password = ref(readSession(K_PWD) || readLocal(K_PWD) || '')
const rememberMe = ref(readLocal(K_REMEMBER) === '1')
const rememberedPassword = ref(readLocal(K_REMEMBERED_PWD) || '')

/** Persist a password as the active credential. */
function set(pwd, remember) {
  password.value = pwd
  rememberMe.value = !!remember
  const now = String(Date.now())

  writeSession(K_PWD, pwd)
  writeSession(K_TIME, now)

  if (remember) {
    writeLocal(K_PWD, pwd)
    writeLocal(K_TIME, now)
    writeLocal(K_REMEMBER, '1')
    writeLocal(K_REMEMBERED_PWD, pwd)
    rememberedPassword.value = pwd
  } else {
    dropLocal(K_PWD)
    dropLocal(K_TIME)
    dropLocal(K_REMEMBER)
    dropLocal(K_REMEMBERED_PWD)
    rememberedPassword.value = ''
  }
}

/**
 * Drop the active credential.
 *
 * Unlike the legacy panel this also clears the remembered copy, which otherwise
 * left the password readable in localStorage after an explicit logout.
 */
function clear() {
  password.value = ''
  dropSession(K_PWD)
  dropSession(K_TIME)
  dropLocal(K_PWD)
  dropLocal(K_TIME)
  dropLocal(K_REMEMBER)
  dropLocal(K_REMEMBERED_PWD)
  rememberMe.value = false
  rememberedPassword.value = ''
}

/** True when a stored credential exists and is still inside the 72h window. */
function isFresh() {
  if (!password.value) return false
  const raw = readSession(K_TIME) || readLocal(K_TIME)
  const at = Number(raw)
  if (!raw || !Number.isFinite(at)) return false
  return Date.now() - at <= SESSION_MAX_AGE_MS
}

export const credentials = {
  password,
  rememberMe,
  rememberedPassword,
  set,
  clear,
  isFresh,
}

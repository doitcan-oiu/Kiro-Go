<script setup>
// Add-account modal: a method picker followed by the chosen flow's form.
//
// Nine import paths, grouped by how much work they need from the user:
//   device-code / browser flows  builderid, iam, enterprisesso
//   paste a secret               sso, cookie, apikey
//   paste a file or blob         local, credentials, apikeybatch
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import {
  PhArrowLeft,
  PhArrowSquareOut,
  PhCode,
  PhCookie,
  PhFolderOpen,
  PhIdentificationCard,
  PhKey,
  PhLayout,
  PhShieldCheck,
  PhStack,
  PhWindowsLogo,
} from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { copyText } from '@/lib/clipboard'
import { parseApiKeysInput, parseCredentialsInput } from '@/lib/import-parse'
import BaseModal from '@/components/ui/BaseModal.vue'
import BaseButton from '@/components/ui/BaseButton.vue'
import BaseField from '@/components/ui/BaseField.vue'
import BaseSelect from '@/components/ui/BaseSelect.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
})
const emit = defineEmits(['close', 'imported'])

const { t } = useI18n()

const DEFAULT_REGION = 'us-east-1'

// ── method registry ───────────────────────────────────────────────────────
const METHODS = [
  { id: 'builderid', icon: PhIdentificationCard, titleKey: 'modal.builderIdTitle', descKey: 'modal.builderIdDesc' },
  { id: 'iam', icon: PhKey, titleKey: 'modal.iamTitle', descKey: 'modal.iamDesc' },
  { id: 'enterprisesso', icon: PhWindowsLogo, titleKey: 'modal.enterpriseSsoTitle', descKey: 'modal.enterpriseSsoDesc' },
  { id: 'sso', icon: PhShieldCheck, titleKey: 'modal.ssoTitle', descKey: 'modal.ssoDesc' },
  { id: 'local', icon: PhFolderOpen, titleKey: 'modal.localTitle', descKey: 'modal.localDesc' },
  { id: 'credentials', icon: PhCode, titleKey: 'modal.credentialsTitle', descKey: 'modal.credentialsDesc' },
  { id: 'cookie', icon: PhCookie, titleKey: 'modal.cookieTitle', descKey: 'modal.cookieDesc' },
  { id: 'apikey', icon: PhKey, titleKey: 'modal.apikeyTitle', descKey: 'modal.apikeyDesc' },
  { id: 'apikeybatch', icon: PhStack, titleKey: 'modal.apikeyBatchTitle', descKey: 'modal.apikeyBatchDesc' },
]

const method = ref('')
const busy = ref(false)
const activeMethod = computed(() => METHODS.find((m) => m.id === method.value) || null)

const modalTitle = computed(() =>
  activeMethod.value ? t(activeMethod.value.titleKey) : t('modal.addAccount'),
)

// ── form state ────────────────────────────────────────────────────────────
const form = reactive({
  region: DEFAULT_REGION,
  // iam
  iamStartUrl: '',
  iamCallback: '',
  // sso
  ssoToken: '',
  // local
  localProvider: 'BuilderId',
  localTokenJson: '',
  localClientJson: '',
  // credentials
  credJson: '',
  // cookie
  cookieProvider: 'Google',
  cookieRefreshToken: '',
  // api key
  apiKeyValue: '',
  apiKeyBatchValue: '',
})

// Device-code / browser-flow session state.
const session = reactive({
  id: '',
  step: 1,
  userCode: '',
  verificationUri: '',
  authorizeUrl: '',
  signInUrl: '',
  waiting: false,
  hint: '',
})

let pollTimer = null

function stopPolling() {
  clearTimeout(pollTimer)
  pollTimer = null
}

onBeforeUnmount(stopPolling)

function resetAll() {
  stopPolling()
  method.value = ''
  busy.value = false
  Object.assign(form, {
    region: DEFAULT_REGION,
    iamStartUrl: '',
    iamCallback: '',
    ssoToken: '',
    localProvider: 'BuilderId',
    localTokenJson: '',
    localClientJson: '',
    credJson: '',
    cookieProvider: 'Google',
    cookieRefreshToken: '',
    apiKeyValue: '',
    apiKeyBatchValue: '',
  })
  Object.assign(session, {
    id: '',
    step: 1,
    userCode: '',
    verificationUri: '',
    authorizeUrl: '',
    signInUrl: '',
    waiting: false,
    hint: '',
  })
}

// Closing mid-flow must cancel the server-side Kiro SSO session, otherwise it
// lingers until it expires and blocks a retry.
watch(
  () => props.open,
  (open) => {
    if (open) return
    if (method.value === 'enterprisesso' && session.id) {
      api.kiroSsoCancel(session.id).catch(() => {})
    }
    resetAll()
  },
)

function close() {
  emit('close')
}

/** Reports a successful import and refreshes the parent list. */
function finish(message) {
  toast(message, 'success')
  emit('imported')
  close()
}

/** Newly imported accounts have no quota data until the first token refresh. */
function autoRefresh(id) {
  if (!id) return
  api.refreshAccount(id).catch(() => {})
}

function errorMessage(err) {
  return err?.message || t('common.unknownError')
}

// ── BuilderID device code ─────────────────────────────────────────────────
async function startBuilderId() {
  busy.value = true
  try {
    const res = await api.builderIdStart(form.region)
    if (res?.error) throw new Error(res.error)
    session.id = res.sessionId
    session.userCode = res.userCode || ''
    session.verificationUri = res.verificationUri || ''
    session.step = 2
    session.waiting = true
    pollBuilderId(Number(res.interval) || 5)
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

function pollBuilderId(intervalSeconds) {
  stopPolling()
  pollTimer = setTimeout(async () => {
    if (!session.id) return
    try {
      const res = await api.builderIdPoll(session.id)
      if (res?.completed || res?.success) {
        session.waiting = false
        autoRefresh(res?.account?.id)
        finish(t('builderid.success'))
        return
      }
      if (res?.error) throw new Error(res.error)
      pollBuilderId(Number(res?.interval) || intervalSeconds)
    } catch (err) {
      session.waiting = false
      toast(errorMessage(err), 'error')
    }
  }, Math.max(1, intervalSeconds) * 1000)
}

// ── IAM Identity Center ───────────────────────────────────────────────────
async function startIam() {
  if (!form.iamStartUrl.trim()) {
    toast(t('iam.startUrl'), 'warning')
    return
  }
  busy.value = true
  try {
    const res = await api.iamSsoStart(form.iamStartUrl.trim(), form.region)
    if (res?.error) throw new Error(res.error)
    session.id = res.sessionId
    session.authorizeUrl = res.authorizeUrl || ''
    session.step = 2
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

async function completeIam() {
  if (!form.iamCallback.trim()) {
    toast(t('iam.callbackUrl'), 'warning')
    return
  }
  busy.value = true
  try {
    const res = await api.iamSsoComplete(session.id, form.iamCallback.trim())
    if (!res?.success) throw new Error(res?.error || t('common.failed'))
    autoRefresh(res?.account?.id)
    finish(t('sso.importSuccess', 1))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

// ── Enterprise SSO (Microsoft 365) ────────────────────────────────────────
async function startEnterpriseSso() {
  busy.value = true
  try {
    const res = await api.kiroSsoStart()
    if (res?.error) throw new Error(res.error)
    session.id = res.sessionId
    session.signInUrl = res.signInUrl || ''
    session.step = 2
    session.hint = t('kirosso.openInstruction')
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

async function submitEnterpriseCallback() {
  if (!form.iamCallback.trim()) {
    toast(t('iam.callbackUrl'), 'warning')
    return
  }
  busy.value = true
  try {
    const res = await api.kiroSsoCallback(session.id, form.iamCallback.trim())
    if (res?.error) throw new Error(res.error)
    // A `redirectUrl` means the IdP bounced us to a second leg: show the new URL
    // and wait for another callback paste.
    if (res?.redirectUrl) {
      session.signInUrl = res.redirectUrl
      form.iamCallback = ''
      session.hint = t('kirosso.hostNote')
      return
    }
    session.waiting = true
    pollEnterpriseSso()
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

function pollEnterpriseSso() {
  stopPolling()
  pollTimer = setTimeout(async () => {
    if (!session.id) return
    try {
      const res = await api.kiroSsoPoll(session.id)
      if (res?.completed || res?.success) {
        // Clear the id *before* closing so the close watcher does not cancel a
        // session that already completed.
        const accountId = res?.account?.id
        session.id = ''
        session.waiting = false
        autoRefresh(accountId)
        finish(t('sso.importSuccess', 1))
        return
      }
      if (res?.error) throw new Error(res.error)
      pollEnterpriseSso()
    } catch (err) {
      session.waiting = false
      toast(errorMessage(err), 'error')
    }
  }, 2000)
}

// ── SSO bearer token ──────────────────────────────────────────────────────
async function submitSsoToken() {
  const token = form.ssoToken.trim()
  if (!token) {
    toast(t('sso.tokenLabel'), 'warning')
    return
  }
  busy.value = true
  try {
    const res = await api.importSsoToken(token, form.region)
    if (!res?.success && !res?.accounts?.length) {
      throw new Error(res?.error || t('common.failed'))
    }
    const ids = (res.accounts || []).map((a) => a.id)
    ids.forEach(autoRefresh)
    const failed = res.errors?.length || 0
    finish(failed ? t('sso.importPartial', ids.length, failed) : t('sso.importSuccess', ids.length))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

// ── local credential files ────────────────────────────────────────────────
const localNeedsClient = computed(
  () => form.localProvider === 'BuilderId' || form.localProvider === 'Enterprise',
)

async function readFileInto(event, field) {
  const file = event.target.files?.[0]
  if (!file) return
  form[field] = await file.text()
  event.target.value = ''
}

async function submitLocal() {
  let tokenObj
  try {
    tokenObj = JSON.parse(form.localTokenJson)
  } catch {
    toast(t('local.tokenInvalid'), 'error')
    return
  }
  const refreshToken = tokenObj.refreshToken || tokenObj.refresh_token
  if (!refreshToken) {
    toast(t('local.refreshTokenMissing'), 'error')
    return
  }

  const payload = {
    refreshToken,
    accessToken: tokenObj.accessToken || tokenObj.access_token || '',
    region: tokenObj.region || form.region,
    authMethod: localNeedsClient.value ? 'idc' : 'social',
    provider: form.localProvider,
  }

  if (localNeedsClient.value) {
    if (!form.localClientJson.trim()) {
      toast(t('local.clientRequired'), 'error')
      return
    }
    let clientObj
    try {
      clientObj = JSON.parse(form.localClientJson)
    } catch {
      toast(t('local.clientInvalid'), 'error')
      return
    }
    payload.clientId = clientObj.clientId || clientObj.client_id || ''
    payload.clientSecret = clientObj.clientSecret || clientObj.client_secret || ''
    if (!payload.clientId) {
      toast(t('local.clientMissing'), 'error')
      return
    }
    if (!payload.clientSecret) {
      toast(t('local.clientSecretMissing'), 'error')
      return
    }
    if (form.localProvider === 'Enterprise') payload.authMethod = 'external_idp'
  }

  busy.value = true
  try {
    const res = await api.importCredentials(payload)
    if (!res?.success) throw new Error(res?.error || t('common.failed'))
    autoRefresh(res?.account?.id)
    finish(t('local.importSuccess'))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

// ── pasted credentials (batch) ────────────────────────────────────────────
async function submitCredentials() {
  const { entries, skipped, invalidJson } = parseCredentialsInput(form.credJson)
  if (invalidJson) {
    toast(t('credentials.jsonError'), 'error')
    return
  }
  if (!entries.length) {
    toast(skipped ? t('credentials.lineParseAllSkipped') : t('credentials.label'), 'error')
    return
  }

  busy.value = true
  const dismiss = toast(t('batch.processing'), 'info', { duration: 0 })
  let ok = 0
  const failures = []
  try {
    for (const entry of entries) {
      try {
        const res = await api.importCredentials(entry)
        if (res?.success) {
          ok += 1
          autoRefresh(res?.account?.id)
        } else {
          failures.push(res?.error || t('common.failed'))
        }
      } catch (err) {
        failures.push(errorMessage(err))
      }
    }
  } finally {
    dismiss()
    busy.value = false
  }

  if (!ok) {
    toast(failures[0] || t('common.failed'), 'error')
    return
  }
  if (failures.length || skipped) {
    toast(t('sso.importPartial', ok, failures.length + skipped), 'warning')
    emit('imported')
    close()
    return
  }
  finish(t('sso.importSuccess', ok))
}

// ── cookie / refresh token ────────────────────────────────────────────────
async function submitCookie() {
  const refreshToken = form.cookieRefreshToken.trim()
  if (!refreshToken) {
    toast(t('cookie.refreshTokenMissing'), 'error')
    return
  }
  busy.value = true
  try {
    const res = await api.importCredentials({
      refreshToken,
      authMethod: 'social',
      provider: form.cookieProvider,
    })
    if (!res?.success) throw new Error(res?.error || t('common.failed'))
    autoRefresh(res?.account?.id)
    finish(t('cookie.importSuccess'))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

// ── single API key ────────────────────────────────────────────────────────
async function submitApiKey() {
  const key = form.apiKeyValue.trim()
  if (!key) {
    toast(t('apikey.keyMissing'), 'error')
    return
  }
  busy.value = true
  try {
    const res = await api.importCredentials({
      kiroApiKey: key,
      authMethod: 'api_key',
      region: form.region,
    })
    if (!res?.success) throw new Error(res?.error || t('common.failed'))
    autoRefresh(res?.account?.id)
    finish(t('apikey.importSuccess'))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

// ── API keys (batch) ──────────────────────────────────────────────────────
async function submitApiKeyBatch() {
  const keys = parseApiKeysInput(form.apiKeyBatchValue)
  if (!keys.length) {
    toast(t('apikeyBatch.keysMissing'), 'error')
    return
  }
  busy.value = true
  try {
    const res = await api.importApiKeysBatch(keys, form.region)
    if (!res?.success && !res?.imported) throw new Error(res?.error || t('common.failed'))
    const imported = Number(res.imported || 0)
    const total = Number(res.total || keys.length)
    const skipped = Number(res.skipped || 0)
    if (res.infoFailed) toast(t('apikeyBatch.infoFailed'), 'warning')
    finish(t('apiKeys.importSuccess', imported, total, skipped))
  } catch (err) {
    toast(errorMessage(err), 'error')
  } finally {
    busy.value = false
  }
}

const regionOptions = ['us-east-1', 'eu-central-1', 'ap-southeast-1']
</script>

<template>
  <BaseModal :open="open" :title="modalTitle" size="lg" @close="close">
    <template v-if="activeMethod" #header>
      <div class="flex min-w-0 items-center gap-2">
        <button
          type="button"
          class="-m-1 rounded-md p-1.5 text-txt-tertiary transition-colors hover:bg-surface-hover hover:text-txt"
          :aria-label="t('common.back')"
          @click="resetAll()"
        >
          <PhArrowLeft :size="16" />
        </button>
        <component :is="activeMethod.icon" :size="18" class="shrink-0 text-accent" />
        <h2 class="truncate font-brand text-title-sm text-txt">{{ modalTitle }}</h2>
      </div>
    </template>

    <!-- method picker -->
    <div v-if="!activeMethod" class="grid gap-sm sm:grid-cols-2">
      <button
        v-for="m in METHODS"
        :key="m.id"
        type="button"
        class="glass-thin glass-hover flex items-start gap-3 rounded-[10px] p-md text-left"
        @click="method = m.id"
      >
        <span class="mt-0.5 shrink-0 text-accent"><component :is="m.icon" :size="20" /></span>
        <span class="min-w-0">
          <span class="block text-body font-medium text-txt">{{ t(m.titleKey) }}</span>
          <span class="mt-0.5 block text-caption leading-relaxed text-txt-tertiary">
            {{ t(m.descKey) }}
          </span>
        </span>
      </button>
    </div>

    <!-- BuilderID -->
    <div v-else-if="method === 'builderid'" class="space-y-md">
      <template v-if="session.step === 1">
        <BaseField :label="t('detail.region')">
          <BaseSelect v-model="form.region" :options="regionOptions" />
        </BaseField>
      </template>

      <template v-else>
        <div class="surface-card space-y-3 p-md">
          <div>
            <p class="text-caption text-txt-tertiary">{{ t('builderid.verifyCode') }}</p>
            <div class="mt-1 flex items-center gap-2">
              <code class="code-inline text-title-md tracking-[0.2em]">{{ session.userCode }}</code>
              <BaseButton variant="ghost" size="xs" @click="copyText(session.userCode)">
                {{ t('common.copy') }}
              </BaseButton>
            </div>
          </div>
          <div>
            <p class="text-caption text-txt-tertiary">{{ t('builderid.verifyUrl') }}</p>
            <a
              :href="session.verificationUri"
              target="_blank"
              rel="noopener noreferrer"
              class="mt-1 inline-flex items-center gap-1.5 text-body-sm text-accent-secondary hover:underline"
            >
              {{ session.verificationUri }}
              <PhArrowSquareOut :size="14" />
            </a>
          </div>
        </div>
        <p v-if="session.waiting" class="text-body-sm text-txt-secondary">
          {{ t('builderid.waiting') }}
        </p>
      </template>
    </div>

    <!-- IAM Identity Center -->
    <div v-else-if="method === 'iam'" class="space-y-md">
      <BaseField :label="t('iam.startUrl')">
        <template #default="{ id }">
          <input
            :id="id"
            v-model="form.iamStartUrl"
            class="field"
            placeholder="https://d-xxxx.awsapps.com/start"
            :disabled="session.step === 2"
          />
        </template>
      </BaseField>

      <BaseField :label="t('detail.region')">
        <BaseSelect v-model="form.region" :options="regionOptions" :disabled="session.step === 2" />
      </BaseField>

      <template v-if="session.step === 2">
        <div class="surface-card p-md">
          <p class="text-caption text-txt-tertiary">{{ t('iam.loginUrl') }}</p>
          <a
            :href="session.authorizeUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="mt-1 inline-flex items-center gap-1.5 break-all text-body-sm text-accent-secondary hover:underline"
          >
            {{ session.authorizeUrl }}
            <PhArrowSquareOut :size="14" class="shrink-0" />
          </a>
        </div>

        <BaseField :label="t('iam.callbackUrl')">
          <template #default="{ id }">
            <input :id="id" v-model="form.iamCallback" class="field" placeholder="http://127.0.0.1:.../?code=..." />
          </template>
        </BaseField>
      </template>
    </div>

    <!-- Enterprise SSO -->
    <div v-else-if="method === 'enterprisesso'" class="space-y-md">
      <template v-if="session.step === 2">
        <div class="surface-card p-md">
          <p class="text-caption text-txt-tertiary">{{ t('iam.loginUrl') }}</p>
          <a
            :href="session.signInUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="mt-1 inline-flex items-center gap-1.5 break-all text-body-sm text-accent-secondary hover:underline"
          >
            {{ session.signInUrl }}
            <PhArrowSquareOut :size="14" class="shrink-0" />
          </a>
          <p v-if="session.hint" class="mt-2 text-caption leading-relaxed text-txt-tertiary">
            {{ session.hint }}
          </p>
        </div>

        <BaseField :label="t('iam.callbackUrl')">
          <template #default="{ id }">
            <input :id="id" v-model="form.iamCallback" class="field" placeholder="http://127.0.0.1:3128/?code=..." />
          </template>
        </BaseField>

        <p v-if="session.waiting" class="text-body-sm text-txt-secondary">
          {{ t('builderid.waiting') }}
        </p>
      </template>
      <p v-else class="text-body-sm leading-relaxed text-txt-secondary">
        {{ t('modal.enterpriseSsoDesc') }}
      </p>
    </div>

    <!-- SSO bearer token -->
    <div v-else-if="method === 'sso'" class="space-y-md">
      <BaseField :label="t('sso.tokenLabel')" :hint="t('sso.tokenHint')">
        <template #default="{ id, describedBy }">
          <textarea
            :id="id"
            v-model="form.ssoToken"
            class="field"
            rows="4"
            :aria-describedby="describedBy"
            :placeholder="t('sso.tokenPlaceholder')"
          />
        </template>
      </BaseField>

      <BaseField :label="t('detail.region')">
        <BaseSelect v-model="form.region" :options="regionOptions" />
      </BaseField>

      <div class="surface-card space-y-1 p-md text-caption leading-relaxed text-txt-tertiary">
        <p class="font-medium text-txt-secondary">{{ t('sso.howToGet') }}</p>
        <p>{{ t('sso.step1') }}</p>
        <p>{{ t('sso.step2') }}</p>
        <p>{{ t('sso.step3') }}</p>
      </div>
    </div>

    <!-- local credential files -->
    <div v-else-if="method === 'local'" class="space-y-md">
      <BaseField :label="t('local.loginChannel')">
        <BaseSelect
          v-model="form.localProvider"
          :options="[
            { value: 'BuilderId', label: t('local.providerBuilderId') },
            { value: 'Enterprise', label: t('local.providerEnterprise') },
            { value: 'Google', label: t('local.providerGoogle') },
            { value: 'Github', label: t('local.providerGithub') },
          ]"
        />
      </BaseField>

      <div class="surface-card space-y-1 p-md text-caption leading-relaxed text-txt-tertiary">
        <p class="font-medium text-txt-secondary">{{ t('local.fileLocation') }}</p>
        <p>{{ t('local.windows') }}</p>
        <p>{{ t('local.macosLinux') }}</p>
      </div>

      <BaseField :label="t('local.tokenFile')" :hint="t('local.pasteOrUpload')">
        <template #default="{ id, describedBy }">
          <textarea
            :id="id"
            v-model="form.localTokenJson"
            class="field"
            rows="4"
            :aria-describedby="describedBy"
            placeholder='{"refreshToken":"...","accessToken":"..."}'
          />
        </template>
      </BaseField>
      <label class="btn btn-glass btn-xs cursor-pointer">
        {{ t('local.upload') }}
        <input type="file" accept=".json,application/json" class="hidden" @change="readFileInto($event, 'localTokenJson')" />
      </label>

      <template v-if="localNeedsClient">
        <BaseField :label="t('local.clientFile')" :hint="t('local.pasteOrUpload')">
          <template #default="{ id, describedBy }">
            <textarea
              :id="id"
              v-model="form.localClientJson"
              class="field"
              rows="4"
              :aria-describedby="describedBy"
              placeholder='{"clientId":"...","clientSecret":"..."}'
            />
          </template>
        </BaseField>
        <label class="btn btn-glass btn-xs cursor-pointer">
          {{ t('local.upload') }}
          <input type="file" accept=".json,application/json" class="hidden" @change="readFileInto($event, 'localClientJson')" />
        </label>
      </template>
    </div>

    <!-- pasted credentials -->
    <div v-else-if="method === 'credentials'" class="space-y-md">
      <BaseField :label="t('credentials.label')" :hint="t('credentials.batchHint')">
        <template #default="{ id, describedBy }">
          <textarea
            :id="id"
            v-model="form.credJson"
            class="field"
            rows="10"
            :aria-describedby="describedBy"
            placeholder='[{"refreshToken":"...","clientId":"...","clientSecret":"..."}]'
          />
        </template>
      </BaseField>
      <p class="text-caption leading-relaxed text-txt-tertiary">{{ t('credentials.authHint') }}</p>
    </div>

    <!-- cookie / refresh token -->
    <div v-else-if="method === 'cookie'" class="space-y-md">
      <BaseField :label="t('cookie.provider')">
        <BaseSelect
          v-model="form.cookieProvider"
          :options="[
            { value: 'Google', label: t('cookie.google') },
            { value: 'Github', label: t('cookie.github') },
          ]"
        />
      </BaseField>

      <BaseField :label="t('cookie.refreshToken')">
        <template #default="{ id }">
          <textarea
            :id="id"
            v-model="form.cookieRefreshToken"
            class="field"
            rows="3"
            :placeholder="t('cookie.refreshTokenPlaceholder')"
          />
        </template>
      </BaseField>

      <div class="surface-card space-y-1 p-md text-caption leading-relaxed text-txt-tertiary">
        <p class="font-medium text-txt-secondary">{{ t('cookie.howToGet') }}</p>
        <p>{{ t('cookie.step1') }}</p>
        <p>{{ t('cookie.step2') }}</p>
        <p>{{ t('cookie.step3') }}</p>
      </div>
    </div>

    <!-- single API key -->
    <div v-else-if="method === 'apikey'" class="space-y-md">
      <BaseField :label="t('apikey.key')" :hint="t('apikey.hint')">
        <template #default="{ id, describedBy }">
          <input
            :id="id"
            v-model="form.apiKeyValue"
            class="field font-mono"
            :aria-describedby="describedBy"
            :placeholder="t('apikey.keyPlaceholder')"
          />
        </template>
      </BaseField>

      <BaseField :label="t('detail.region')" :hint="t('apikey.regionHint')">
        <BaseSelect v-model="form.region" :options="regionOptions" />
      </BaseField>
    </div>

    <!-- API keys batch -->
    <div v-else-if="method === 'apikeybatch'" class="space-y-md">
      <BaseField :label="t('apikeyBatch.keys')" :hint="t('apikeyBatch.onePerLine')">
        <template #default="{ id, describedBy }">
          <textarea
            :id="id"
            v-model="form.apiKeyBatchValue"
            class="field"
            rows="8"
            :aria-describedby="describedBy"
            :placeholder="t('apikeyBatch.keysPlaceholder')"
          />
        </template>
      </BaseField>
      <p class="text-caption leading-relaxed text-txt-tertiary">{{ t('apikeyBatch.hint') }}</p>

      <BaseField :label="t('detail.region')">
        <BaseSelect v-model="form.region" :options="regionOptions" />
      </BaseField>
    </div>

    <template v-if="activeMethod" #footer>
      <BaseButton variant="glass" size="sm" @click="close">{{ t('common.cancel') }}</BaseButton>

      <BaseButton
        v-if="method === 'builderid' && session.step === 1"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="startBuilderId"
      >
        {{ t('builderid.startLogin') }}
      </BaseButton>
      <BaseButton
        v-else-if="method === 'builderid'"
        variant="primary"
        size="sm"
        :href="session.verificationUri"
      >
        {{ t('builderid.open') }}
      </BaseButton>

      <BaseButton
        v-else-if="method === 'iam' && session.step === 1"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="startIam"
      >
        {{ t('iam.completeLogin') }}
      </BaseButton>
      <BaseButton
        v-else-if="method === 'iam'"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="completeIam"
      >
        {{ t('iam.complete') }}
      </BaseButton>

      <BaseButton
        v-else-if="method === 'enterprisesso' && session.step === 1"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="startEnterpriseSso"
      >
        {{ t('builderid.startLogin') }}
      </BaseButton>
      <BaseButton
        v-else-if="method === 'enterprisesso'"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="submitEnterpriseCallback"
      >
        {{ t('iam.complete') }}
      </BaseButton>

      <BaseButton v-else-if="method === 'sso'" variant="primary" size="sm" :loading="busy" @click="submitSsoToken">
        {{ t('common.add') }}
      </BaseButton>
      <BaseButton v-else-if="method === 'local'" variant="primary" size="sm" :loading="busy" @click="submitLocal">
        {{ t('common.add') }}
      </BaseButton>
      <BaseButton v-else-if="method === 'credentials'" variant="primary" size="sm" :loading="busy" @click="submitCredentials">
        {{ t('common.add') }}
      </BaseButton>
      <BaseButton v-else-if="method === 'cookie'" variant="primary" size="sm" :loading="busy" @click="submitCookie">
        {{ t('common.add') }}
      </BaseButton>
      <BaseButton v-else-if="method === 'apikey'" variant="primary" size="sm" :loading="busy" @click="submitApiKey">
        {{ t('common.add') }}
      </BaseButton>
      <BaseButton v-else-if="method === 'apikeybatch'" variant="primary" size="sm" :loading="busy" @click="submitApiKeyBatch">
        {{ t('common.add') }}
      </BaseButton>
    </template>
  </BaseModal>
</template>

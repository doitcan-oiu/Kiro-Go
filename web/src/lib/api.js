// Admin API client.
//
// Contract preserved from the legacy panel (the Go server has not changed):
//   - every admin call carries the `X-Admin-Password` header
//   - all admin paths are relative to `/admin/api`
//   - there is no login endpoint; credentials are probed with `GET /status`
import { credentials } from '@/lib/credentials'

const BASE = '/admin/api'

/** Thrown for non-2xx admin responses; carries status + parsed body. */
export class ApiError extends Error {
  constructor(message, { status = 0, body = null } = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

/** True when the failure means "your password is wrong / expired". */
export function isAuthError(err) {
  return err instanceof ApiError && (err.status === 401 || err.status === 403)
}

async function parseBody(res) {
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

/**
 * Low-level admin fetch. `body` is JSON-encoded automatically.
 * Rejects with ApiError on non-2xx so callers can use try/catch uniformly.
 */
export async function request(path, { method = 'GET', body, signal, headers } = {}) {
  const init = {
    method,
    headers: {
      'X-Admin-Password': credentials.password.value || '',
      ...(headers || {}),
    },
    signal,
  }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = typeof body === 'string' ? body : JSON.stringify(body)
  }

  const res = await fetch(BASE + path, init)
  const parsed = await parseBody(res)
  if (!res.ok) {
    const detail =
      (parsed && typeof parsed === 'object' && (parsed.error || parsed.message)) ||
      (typeof parsed === 'string' && parsed) ||
      `HTTP ${res.status}`
    throw new ApiError(detail, { status: res.status, body: parsed })
  }
  return parsed
}

const get = (path, opts) => request(path, { ...opts, method: 'GET' })
const post = (path, body, opts) => request(path, { ...opts, method: 'POST', body })
const put = (path, body, opts) => request(path, { ...opts, method: 'PUT', body })
const del = (path, opts) => request(path, { ...opts, method: 'DELETE' })

/**
 * Many admin handlers answer 200 with `{success:false, error:"…"}` instead of a
 * non-2xx status. `expectSuccess` normalises that into a thrown ApiError so
 * callers only need one failure path.
 */
export function expectSuccess(res, fallbackMessage = 'Request failed') {
  if (res && typeof res === 'object' && res.success === false) {
    throw new ApiError(res.error || fallbackMessage, { status: 200, body: res })
  }
  return res
}

async function postChecked(path, body, fallback) {
  return expectSuccess(await post(path, body), fallback)
}

const encId = (id) => encodeURIComponent(id)

export const api = {
  request,
  get,
  post,
  put,
  delete: del,

  // ── status / stats ──────────────────────────────────────────────────────
  status: () => get('/status'),
  stats: () => get('/stats'),
  resetStats: () => post('/stats/reset'),
  version: () => get('/version'),

  // ── accounts ────────────────────────────────────────────────────────────
  accounts: () => get('/accounts'),
  accountFull: (id) => get(`/accounts/${encId(id)}/full`),
  updateAccount: (id, patch) => postCheckedPut(`/accounts/${encId(id)}`, patch),
  deleteAccount: (id) => del(`/accounts/${encId(id)}`),
  refreshAccount: (id) => postChecked(`/accounts/${encId(id)}/refresh`),
  testAccount: (id, model) => post(`/accounts/${encId(id)}/test`, { model }),
  batchAccounts: (ids, action) => postChecked('/accounts/batch', { ids, action }),
  accountModels: (id) => get(`/accounts/${encId(id)}/models`),
  accountModelsCached: (id) => get(`/accounts/${encId(id)}/models/cached`),
  refreshAccountModels: (id) => post(`/accounts/${encId(id)}/models/refresh`),
  refreshAllModels: () => post('/accounts/models/refresh'),
  overage: (id) => get(`/accounts/${encId(id)}/overage`),
  setOverage: (id, enabled) => post(`/accounts/${encId(id)}/overage`, { enabled }),
  generateMachineId: () => get('/generate-machine-id'),
  exportAccounts: (ids) => post('/export', { ids }),

  // ── auth / import flows ─────────────────────────────────────────────────
  importCredentials: (payload) => post('/auth/credentials', payload),
  importApiKeysBatch: (keys, region) =>
    post('/auth/apikeys-batch', region ? { keys, region } : { keys }),
  importSsoToken: (bearerToken, region) => post('/auth/sso-token', { bearerToken, region }),
  builderIdStart: (region) => post('/auth/builderid/start', { region }),
  builderIdPoll: (sessionId) => post('/auth/builderid/poll', { sessionId }),
  iamSsoStart: (startUrl, region) => post('/auth/iam-sso/start', { startUrl, region }),
  iamSsoComplete: (sessionId, callbackUrl) =>
    post('/auth/iam-sso/complete', { sessionId, callbackUrl }),
  kiroSsoStart: () => post('/auth/kiro-sso/start', {}),
  kiroSsoCallback: (sessionId, callbackUrl) =>
    post('/auth/kiro-sso/callback', { sessionId, callbackUrl }),
  kiroSsoPoll: (sessionId) => post('/auth/kiro-sso/poll', { sessionId }),
  kiroSsoCancel: (sessionId) => post('/auth/kiro-sso/cancel', { sessionId }),

  // ── api keys ────────────────────────────────────────────────────────────
  apiKeys: () => get('/api-keys'),
  createApiKey: (payload) => postChecked('/api-keys', payload),
  updateApiKey: (id, payload) => postCheckedPut(`/api-keys/${encId(id)}`, payload),
  deleteApiKey: (id) => del(`/api-keys/${encId(id)}`),
  resetApiKeyUsage: (id) => postChecked(`/api-keys/${encId(id)}/reset-usage`),

  // ── settings ────────────────────────────────────────────────────────────
  // POST /settings is a partial merge on the server: absent fields stay
  // unchanged. Never send the whole form — only the keys being saved.
  settings: () => get('/settings'),
  saveSettings: (patch) => postChecked('/settings', patch),
  thinking: () => get('/thinking'),
  saveThinking: (payload) => postChecked('/thinking', payload),
  endpoint: () => get('/endpoint'),
  saveEndpoint: (payload) => postChecked('/endpoint', payload),
  proxy: () => get('/proxy'),
  saveProxy: (proxyURL) => postChecked('/proxy', { proxyURL }),
  promptFilter: () => get('/prompt-filter'),
  savePromptFilter: (payload) => postChecked('/prompt-filter', payload),

  // ── replenish ───────────────────────────────────────────────────────────
  replenish: () => get('/replenish'),
  saveReplenish: (payload) => postChecked('/replenish', payload),
  testReplenish: () => post('/replenish/test'),
  runReplenish: (payload) => post('/replenish/run', payload || {}),
  registerReplenishWebhook: (provider) =>
    post('/replenish/register-webhook', provider ? { provider } : {}),
  resetReplenishSecret: (provider) => post('/replenish/reset-secret', { provider }),

  // ── logs ────────────────────────────────────────────────────────────────
  logs: () => get('/logs'),
  clearLogs: () => del('/logs'),
}

async function postCheckedPut(path, body) {
  return expectSuccess(await put(path, body))
}

/**
 * Public (unauthenticated) model list, served from the same origin as the
 * proxy itself. Intentionally sends no admin header — this is the exact
 * request an API consumer would make.
 */
export async function fetchPublicModels() {
  const res = await fetch(`${location.origin}/v1/models`)
  if (!res.ok) throw new ApiError(`HTTP ${res.status}`, { status: res.status })
  return res.json()
}

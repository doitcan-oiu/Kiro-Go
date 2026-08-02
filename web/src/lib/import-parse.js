// Parsers for the bulk "paste credentials" and "paste API keys" import flows.
//
// Kept as pure functions (no DOM, no network) because these are the fiddliest
// pieces of the whole panel: the legacy implementation accepted five different
// shapes and silently dropped entries that did not match. Isolating them here
// makes the accepted formats explicit and unit-testable.

/** Auth-method values that mean "enterprise IdP" (Azure AD / Entra / M365). */
const EXTERNAL_IDP_ALIASES = new Set([
  'external_idp',
  'azuread',
  'azure',
  'entra',
  'entra-id',
  'microsoft',
  'm365',
  'office365',
  'external',
])

/** Reads the first present key, accepting camelCase and snake_case spellings. */
function pick(obj, ...names) {
  for (const name of names) {
    const value = obj?.[name]
    if (value !== undefined && value !== null && value !== '') return value
  }
  return ''
}

/**
 * Infers `authMethod` when the source data does not state it.
 *
 * Order matters: an explicit enterprise alias wins, then the presence of a
 * client pair implies an IdC device-code credential, and everything else is a
 * social (Google/GitHub) refresh token.
 */
export function inferAuthMethod(raw) {
  const stated = String(pick(raw, 'authMethod', 'auth_method') || '').toLowerCase()
  if (EXTERNAL_IDP_ALIASES.has(stated)) return 'external_idp'
  if (pick(raw, 'tokenEndpoint', 'token_endpoint')) return 'external_idp'
  if (stated === 'idc' || stated === 'social' || stated === 'api_key') return stated
  const hasClientPair = pick(raw, 'clientId', 'client_id') && pick(raw, 'clientSecret', 'client_secret')
  return hasClientPair ? 'idc' : 'social'
}

/** Default provider label per auth method, used when none is supplied. */
function defaultProvider(authMethod) {
  if (authMethod === 'external_idp') return 'AzureAD'
  if (authMethod === 'social') return 'Google'
  return 'BuilderId'
}

/** Normalises one object into the `POST /auth/credentials` payload shape. */
function normalizeEntry(raw) {
  if (!raw || typeof raw !== 'object') return null

  const refreshToken = String(pick(raw, 'refreshToken', 'refresh_token') || '').trim()
  const apiKey = String(pick(raw, 'kiroApiKey', 'kiro_api_key', 'apiKey', 'api_key') || '').trim()

  // An entry must carry something usable; anything else is a stray object.
  if (!refreshToken && !apiKey) return null

  if (!refreshToken && apiKey) {
    return {
      kiroApiKey: apiKey,
      authMethod: 'api_key',
      region: String(pick(raw, 'region') || '').trim() || undefined,
    }
  }

  const authMethod = inferAuthMethod(raw)
  const payload = {
    refreshToken,
    authMethod,
    provider: String(pick(raw, 'provider') || defaultProvider(authMethod)),
  }

  const optional = {
    accessToken: pick(raw, 'accessToken', 'access_token'),
    clientId: pick(raw, 'clientId', 'client_id'),
    clientSecret: pick(raw, 'clientSecret', 'client_secret'),
    region: pick(raw, 'region'),
    tokenEndpoint: pick(raw, 'tokenEndpoint', 'token_endpoint'),
    issuerUrl: pick(raw, 'issuerUrl', 'issuer_url', 'startUrl', 'start_url'),
    profileArn: pick(raw, 'profileArn', 'profile_arn'),
    userId: pick(raw, 'userId', 'user_id'),
    email: pick(raw, 'email'),
    id: pick(raw, 'id'),
  }
  for (const [key, value] of Object.entries(optional)) {
    if (value) payload[key] = String(value).trim()
  }

  const scopes = raw.scopes ?? raw.scope
  if (Array.isArray(scopes) && scopes.length) payload.scopes = scopes.map(String)

  return payload
}

/**
 * Parses the delimited line format:
 *   email----password----refreshToken----clientId----clientSecret
 *
 * Tab- and whitespace-separated variants are accepted too. The token is always
 * field 3 (index 2); fields beyond the fifth are ignored.
 */
function parseLine(line) {
  const parts = line.includes('----')
    ? line.split('----')
    : line.split(/\t+|\s{2,}| +/).filter(Boolean)

  if (parts.length < 5) return null
  const [email, , refreshToken, clientId, clientSecret] = parts.map((p) => p.trim())
  if (!refreshToken) return null

  return normalizeEntry({ email, refreshToken, clientId, clientSecret })
}

/**
 * Parses pasted credential text.
 *
 * Accepts, in order: a `{accounts:[…]}` wrapper, a bare JSON array, a single
 * JSON object, or newline-delimited `----` records. Returns the payloads plus a
 * count of unparseable lines so the caller can report partial success.
 */
export function parseCredentialsInput(text) {
  const trimmed = String(text || '').trim()
  if (!trimmed) return { entries: [], skipped: 0, invalidJson: false }

  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    let parsed
    try {
      parsed = JSON.parse(trimmed)
    } catch {
      return { entries: [], skipped: 0, invalidJson: true }
    }
    const list = Array.isArray(parsed)
      ? parsed
      : Array.isArray(parsed?.accounts)
        ? parsed.accounts
        : [parsed]

    const entries = []
    let skipped = 0
    for (const item of list) {
      const entry = normalizeEntry(item)
      if (entry) entries.push(entry)
      else skipped += 1
    }
    return { entries, skipped, invalidJson: false }
  }

  const entries = []
  let skipped = 0
  for (const line of trimmed.split(/\r?\n/)) {
    const value = line.trim()
    if (!value || value.startsWith('#')) continue
    const entry = parseLine(value)
    if (entry) entries.push(entry)
    else skipped += 1
  }
  return { entries, skipped, invalidJson: false }
}

/**
 * Parses pasted API keys. Accepts a JSON array of strings or objects
 * (`key` / `api_key` / `kiroApiKey`), a single object, or one key per line.
 */
export function parseApiKeysInput(text) {
  const trimmed = String(text || '').trim()
  if (!trimmed) return []

  const fromItem = (item) => {
    if (typeof item === 'string') return item.trim()
    if (item && typeof item === 'object') {
      return String(pick(item, 'key', 'api_key', 'apiKey', 'kiroApiKey', 'value') || '').trim()
    }
    return ''
  }

  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      const parsed = JSON.parse(trimmed)
      const list = Array.isArray(parsed)
        ? parsed
        : Array.isArray(parsed?.keys)
          ? parsed.keys
          : Array.isArray(parsed?.apiKeys)
            ? parsed.apiKeys
            : [parsed]
      return list.map(fromItem).filter(Boolean)
    } catch {
      // Not valid JSON — fall through and treat it as plain lines.
    }
  }

  return trimmed
    .split(/[\r\n,]+/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#'))
}

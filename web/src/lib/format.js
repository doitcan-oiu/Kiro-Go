// Formatting helpers. Behaviour matches the legacy panel so numbers, dates and
// quota labels read identically after the rewrite.
import { lang, t } from '@/lib/i18n'

/** Compact token counts: 1.2M / 3.4K / 812. */
export function formatNum(n) {
  const value = Number(n) || 0
  if (Math.abs(value) >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (Math.abs(value) >= 1000) return `${(value / 1000).toFixed(1)}K`
  return String(Math.round(value))
}

/** Grouped decimal formatting for usage/credit/price figures. */
export function formatNumber(n) {
  if (n === null || n === undefined || Number.isNaN(Number(n))) return '0'
  const value = Number(n)
  return Number.isInteger(value)
    ? value.toLocaleString('en-US')
    : value.toLocaleString('en-US', { maximumFractionDigits: 4 })
}

/** Fixed-precision helper that tolerates null/NaN. */
export function toFixed(n, digits = 1) {
  const value = Number(n)
  return Number.isFinite(value) ? value.toFixed(digits) : (0).toFixed(digits)
}

const pad = (n) => String(n).padStart(2, '0')

/** `MM-DD HH:mm:ss` — the log table format. */
export function formatLogTime(unixSeconds) {
  if (!unixSeconds) return '-'
  const d = new Date(Number(unixSeconds) * 1000)
  if (Number.isNaN(d.getTime())) return '-'
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}:${pad(d.getSeconds())}`
}

/** `YYYY-MM-DD HH:mm` — the spec's canonical date format (§4.3). */
export function formatDateTime(unixSeconds) {
  if (!unixSeconds) return '-'
  const d = new Date(Number(unixSeconds) * 1000)
  if (Number.isNaN(d.getTime())) return '-'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}`
}

/** Locale-aware datetime, used where the legacy panel called toLocaleString(). */
export function formatLocale(unixSeconds) {
  if (!unixSeconds) return '-'
  const d = new Date(Number(unixSeconds) * 1000)
  if (Number.isNaN(d.getTime())) return '-'
  return d.toLocaleString(lang.value === 'zh' ? 'zh-CN' : 'en-US')
}

/** Uptime as `Nd Nh Nm Ns`, with localized unit suffixes. */
export function formatUptime(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0))
  const d = Math.floor(total / 86400)
  const h = Math.floor((total % 86400) / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const zh = lang.value === 'zh'
  const parts = []
  if (d) parts.push(zh ? `${d}天` : `${d}d`)
  if (h) parts.push(zh ? `${h}时` : `${h}h`)
  if (m) parts.push(zh ? `${m}分` : `${m}m`)
  parts.push(zh ? `${s}秒` : `${s}s`)
  return parts.join(' ')
}

/** Token expiry relative label: expired / Nh / Nm. */
export function formatTokenExpiry(unixSeconds) {
  if (!unixSeconds) return '-'
  const diffMs = Number(unixSeconds) * 1000 - Date.now()
  if (diffMs <= 0) return t('time.expired')
  const minutes = Math.floor(diffMs / 60000)
  if (minutes >= 60) return `${Math.floor(minutes / 60)}${t('time.hours')}`
  return `${minutes}${t('time.minutes')}`
}

/** Trial expiry label: only surfaced when it is within a week. */
export function formatTrialExpiry(unixSeconds) {
  if (!unixSeconds) return ''
  const diffMs = Number(unixSeconds) * 1000 - Date.now()
  if (diffMs <= 0) return t('accounts.trialExpired')
  const days = Math.floor(diffMs / 86400000)
  if (days === 0) return t('accounts.trialToday')
  if (days <= 7) return `${days}${t('accounts.trialDays')}`
  return ''
}

/** Masks the local part of an email for privacy mode. */
export function maskEmail(email) {
  if (!email) return ''
  const at = email.indexOf('@')
  if (at <= 0) {
    return email.length <= 2 ? `${email.charAt(0)}***` : `${email.slice(0, 2)}***`
  }
  const local = email.slice(0, at)
  const domain = email.slice(at)
  const head = local.slice(0, Math.min(2, local.length))
  return `${head}${'*'.repeat(Math.max(3, local.length - head.length))}${domain}`
}

/** Percentage of a usage pair, clamped to 0–100. */
export function usagePercent(used, limit) {
  const l = Number(limit) || 0
  if (l <= 0) return 0
  return Math.min(100, Math.max(0, (Number(used) / l) * 100))
}

/** Severity band for quota bars: 95%+ critical, 80%+ warn. */
export function usageLevel(pct) {
  if (pct >= 95) return 'critical'
  if (pct >= 80) return 'warn'
  return 'ok'
}

/** `errorType` → localized label, falling back to the raw value. */
export function errorTypeLabel(type) {
  if (!type) return ''
  const pascal = type.charAt(0).toUpperCase() + type.slice(1)
  const key = `errors.type${pascal}`
  const label = t(key)
  return label === key ? type : label
}

/** YYYY-MM-DD, for generated download filenames. */
export function todayStamp() {
  const d = new Date()
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/**
 * 紧凑时长：最多两个量级，用于账号卡片的「存活时长」。
 *
 * 与 formatUptime 的区别是刻意的：formatUptime 面向「服务运行了多久」，秒级有意义；
 * 而账号存活动辄数天，`3天 7时 12分 45秒` 在卡片里既占地方又没人关心后两位。
 * 这里只保留最高两个量级，不足一分钟才显示秒。
 *
 * 非数值（null / undefined / NaN / 对象）一律返回占位符，而不是折算成 0。
 * 这一点是刚性的：这里原来写的是 `Number(seconds) || 0`，于是把
 * `accountLifetime()` 返回的 `{seconds, running}` 对象整个传进来时，NaN 被静默
 * 吞成 0，卡片上每个账号都显示「0秒」。传参错误伪装成了一个看起来合理的数值，
 * 比直接显示「—」难查得多——后者一眼就能看出是「没算出来」。
 *
 * @param {number|null} seconds 秒数；非数值或负数返回占位符
 */
export function formatDurationCompact(seconds) {
  const n = Number(seconds)
  if (seconds === null || seconds === undefined || !Number.isFinite(n)) return '—'
  const total = Math.max(0, Math.floor(n))
  const zh = lang.value === 'zh'
  const d = Math.floor(total / 86400)
  const h = Math.floor((total % 86400) / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60

  const unit = zh
    ? { d: '天', h: '时', m: '分', s: '秒' }
    : { d: 'd', h: 'h', m: 'm', s: 's' }

  if (d > 0) return h > 0 ? `${d}${unit.d}${h}${unit.h}` : `${d}${unit.d}`
  if (h > 0) return m > 0 ? `${h}${unit.h}${m}${unit.m}` : `${h}${unit.h}`
  if (m > 0) return `${m}${unit.m}`
  return `${s}${unit.s}`
}

/**
 * 短日期：`MM-DD HH:mm`，用于「添加时间」。
 *
 * 不显示年份：账号池里的账号通常是近期导入的，年份是冗余信息；跨年的老账号可以
 * 通过 title 属性看到完整时间戳。
 */
export function formatShortDate(unixSeconds) {
  const ts = Number(unixSeconds)
  if (!Number.isFinite(ts) || ts <= 0) return '—'
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/**
 * 美元金额。成本与收入都以美元记账（供应商单价按美元口径录入，模型单价来自
 * LiteLLM 价格表本身就是美元）。
 *
 * 小额用 4 位小数：单次请求的收入常在 $0.0001 量级，按 2 位小数会全部显示成
 * $0.00，看起来像功能没生效。绝对值 >= 1 时收敛到 2 位，避免大额出现一长串
 * 无意义的尾数。
 */
export function formatUsd(n) {
  // null / undefined / '' 一律视为「无数据」，与 NaN 同样返回「—」。
  // 不能依赖 Number(null) === 0：那会把「没有数据」渲染成一个看起来精确的
  // $0.0000，而 undefined 又渲染成「—」，同一种缺失呈现出两种样子。
  if (n === null || n === undefined || n === '') return '—'
  const v = Number(n)
  if (!Number.isFinite(v)) return '—'
  const abs = Math.abs(v)
  const digits = abs >= 1 ? 2 : 4
  const sign = v < 0 ? '-' : ''
  return `${sign}$${abs.toLocaleString('en-US', {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })}`
}

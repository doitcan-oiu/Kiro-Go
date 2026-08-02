// Tests for src/lib/format.js — run via `npm test`.
//
// This file exists because of a bug it would have caught: `formatDurationCompact`
// used `Number(seconds) || 0`, so being handed the `{seconds, running}` object
// that `accountLifetime` returns produced NaN → 0 → "0秒" on every account card.
// A wrong *type* was rendered as a plausible *value*, which is far harder to spot
// than a "—" placeholder.
//
// format.js imports '@/lib/i18n', which plain Node cannot resolve; scripts/alias-hook.mjs
// maps the alias the same way vite.config.js does. That unresolvable import is
// precisely why this module had no tests while stats.js did.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import './alias-hook.mjs'

const { formatDurationCompact } = await import('../src/lib/format.js')
const { lang } = await import('../src/lib/i18n.js')

// Assertions below are language-dependent; pin it so a different host locale
// cannot flip the expected suffixes.
lang.value = 'zh'

test('formatDurationCompact: keeps at most the two largest units', () => {
  assert.equal(formatDurationCompact(90 * 60), '1时30分')
  assert.equal(formatDurationCompact(3 * 86400 + 7 * 3600 + 12 * 60 + 45), '3天7时')
  assert.equal(formatDurationCompact(45 * 60), '45分')
})

test('formatDurationCompact: seconds only below one minute', () => {
  assert.equal(formatDurationCompact(0), '0秒')
  assert.equal(formatDurationCompact(59), '59秒')
})

test('formatDurationCompact: drops a zero middle unit instead of padding it', () => {
  // Exactly 2 days: showing "2天0时" wastes width on a unit that carries nothing.
  assert.equal(formatDurationCompact(2 * 86400), '2天')
  assert.equal(formatDurationCompact(2 * 3600), '2时')
})

test('formatDurationCompact: non-numeric input renders the placeholder, not 0', () => {
  // The regression guard. Every one of these used to collapse to "0秒".
  assert.equal(formatDurationCompact({ seconds: 5400, running: true }), '—')
  assert.equal(formatDurationCompact(NaN), '—')
  assert.equal(formatDurationCompact('abc'), '—')
  assert.equal(formatDurationCompact(null), '—')
  assert.equal(formatDurationCompact(undefined), '—')
  assert.equal(formatDurationCompact(Infinity), '—')
})

test('formatDurationCompact: negative input clamps to zero', () => {
  // Clock skew between the server's createdAt and the browser's Date.now().
  assert.equal(formatDurationCompact(-10), '0秒')
})

test('formatDurationCompact: numeric strings are still accepted', () => {
  // The API delivers JSON numbers, but a string is unambiguous here — unlike an
  // object, it has a single sensible interpretation.
  assert.equal(formatDurationCompact('90'), '1分')
})

test('formatDurationCompact: unit labels follow the active language', () => {
  lang.value = 'en'
  assert.equal(formatDurationCompact(90 * 60), '1h30m')
  lang.value = 'zh'
})

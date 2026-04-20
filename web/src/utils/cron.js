// Cron expression helpers for the visual builder.
// - parseCron: string → field-mode state, or { ok: false } when too complex for the simple builder
// - buildCron: field-mode state → string
// - PRESETS: quick-pick expressions
// - describe / nextRuns: wrappers around cronstrue / cron-parser

import { CronExpressionParser } from 'cron-parser'
import cronstrue from 'cronstrue'

export const PRESETS = [
  { label: 'Every minute',    cron: '* * * * *' },
  { label: 'Every 5 min',     cron: '*/5 * * * *' },
  { label: 'Hourly',          cron: '0 * * * *' },
  { label: 'Daily 9am',       cron: '0 9 * * *' },
  { label: 'Weekdays 9am',    cron: '0 9 * * 1-5' },
  { label: 'Weekly Mon 9am',  cron: '0 9 * * 1' },
  { label: 'Monthly 1st 9am', cron: '0 9 1 * *' },
]

const FIELD_RANGES = {
  minute: [0, 59],
  hour:   [0, 23],
  dom:    [1, 31],
  month:  [1, 12],
  dow:    [0, 6],
}

const MONTH_NAMES = ['JAN','FEB','MAR','APR','MAY','JUN','JUL','AUG','SEP','OCT','NOV','DEC']
const DOW_NAMES   = ['SUN','MON','TUE','WED','THU','FRI','SAT']

// Create a default state. mode='every' means '*'.
export function emptyState() {
  return {
    minute: { mode: 'every', value: 0, step: 5, list: [], rangeFrom: 0, rangeTo: 0 },
    hour:   { mode: 'every', value: 0, step: 2, list: [], rangeFrom: 0, rangeTo: 0 },
    dom:    { mode: 'every', value: 1, step: 2, list: [], rangeFrom: 1, rangeTo: 1 },
    month:  { mode: 'every', value: 1, step: 2, list: [], rangeFrom: 1, rangeTo: 1 },
    dow:    { mode: 'every', value: 0, step: 1, list: [], rangeFrom: 0, rangeTo: 0 },
  }
}

// Parse a 5-field cron string into the field-mode representation.
// Returns { ok: true, state } on success, or { ok: false, reason } when a field
// uses syntax the simple builder can't round-trip (e.g. '1-10/2', '1,5-8').
export function parseCron(str) {
  if (!str || typeof str !== 'string') {
    return { ok: false, reason: 'empty' }
  }
  const parts = str.trim().split(/\s+/)
  if (parts.length !== 5) {
    return { ok: false, reason: 'not-5-fields' }
  }
  const names = ['minute', 'hour', 'dom', 'month', 'dow']
  const state = emptyState()
  for (let i = 0; i < 5; i++) {
    const field = parseField(parts[i], names[i])
    if (!field) return { ok: false, reason: `unsupported:${names[i]}` }
    state[names[i]] = field
  }
  return { ok: true, state }
}

function parseField(raw, name) {
  const [min, max] = FIELD_RANGES[name]
  const base = { mode: 'every', value: min, step: 5, list: [], rangeFrom: min, rangeTo: min }

  // Normalize names to numbers for month / dow so parsing handles 'MON' or '1'.
  const normalized = normalizeNames(raw, name)

  // '*'
  if (normalized === '*') {
    return { ...base, mode: 'every' }
  }
  // '*/N'
  const stepMatch = normalized.match(/^\*\/(\d+)$/)
  if (stepMatch) {
    const step = parseInt(stepMatch[1], 10)
    if (step < 1) return null
    return { ...base, mode: 'everyN', step }
  }
  // Single value 'N'
  if (/^\d+$/.test(normalized)) {
    const v = parseInt(normalized, 10)
    if (!inRange(v, min, max)) return null
    return { ...base, mode: 'at', value: v }
  }
  // Plain range 'A-B'
  const rangeMatch = normalized.match(/^(\d+)-(\d+)$/)
  if (rangeMatch) {
    const from = parseInt(rangeMatch[1], 10)
    const to   = parseInt(rangeMatch[2], 10)
    if (!inRange(from, min, max) || !inRange(to, min, max) || from > to) return null
    return { ...base, mode: 'range', rangeFrom: from, rangeTo: to }
  }
  // Plain list 'A,B,C' — disallow embedded ranges or steps for round-trip simplicity
  if (/^\d+(,\d+)+$/.test(normalized)) {
    const list = normalized.split(',').map(n => parseInt(n, 10))
    if (list.some(v => !inRange(v, min, max))) return null
    return { ...base, mode: 'list', list }
  }
  return null
}

function normalizeNames(raw, name) {
  if (name !== 'month' && name !== 'dow') return raw.toUpperCase()
  const table = name === 'month' ? MONTH_NAMES : DOW_NAMES
  return raw.toUpperCase().split(',').map(tok => {
    const dash = tok.split('-')
    const mapped = dash.map(piece => {
      const slashIdx = piece.indexOf('/')
      if (slashIdx >= 0) {
        const left = piece.slice(0, slashIdx)
        const right = piece.slice(slashIdx)
        const idx = table.indexOf(left)
        return (idx >= 0 ? (name === 'month' ? idx + 1 : idx) : left) + right
      }
      const idx = table.indexOf(piece)
      if (idx < 0) return piece
      return String(name === 'month' ? idx + 1 : idx)
    })
    return mapped.join('-')
  }).join(',')
}

function inRange(v, min, max) {
  return Number.isInteger(v) && v >= min && v <= max
}

// Render state back to a cron expression.
export function buildCron(state) {
  return [
    fieldToString(state.minute, 'minute'),
    fieldToString(state.hour,   'hour'),
    fieldToString(state.dom,    'dom'),
    fieldToString(state.month,  'month'),
    fieldToString(state.dow,    'dow'),
  ].join(' ')
}

function fieldToString(f, name) {
  switch (f.mode) {
    case 'every':  return '*'
    case 'everyN': return `*/${Math.max(1, f.step | 0)}`
    case 'at':     return String(clamp(f.value, name))
    case 'range': {
      const a = clamp(f.rangeFrom, name)
      const b = clamp(f.rangeTo, name)
      return a <= b ? `${a}-${b}` : `${b}-${a}`
    }
    case 'list': {
      const sorted = [...(f.list || [])].map(v => clamp(v, name)).sort((a, b) => a - b)
      const unique = [...new Set(sorted)]
      return unique.length ? unique.join(',') : '*'
    }
    default: return '*'
  }
}

function clamp(v, name) {
  const [min, max] = FIELD_RANGES[name]
  const n = Number.isFinite(v) ? Math.trunc(v) : min
  return Math.min(max, Math.max(min, n))
}

// Human-readable description. Returns '' on parse failure.
export function describe(str) {
  if (!str || typeof str !== 'string') return ''
  try {
    return cronstrue.toString(str.trim(), { throwExceptionOnParseError: true })
  } catch {
    return ''
  }
}

// Return the next n scheduled Date instances. Empty array on parse failure.
export function nextRuns(str, n = 3, tz = '') {
  if (!str || typeof str !== 'string') return []
  try {
    const opts = tz ? { tz } : {}
    const iter = CronExpressionParser.parse(str.trim(), opts)
    const out = []
    for (let i = 0; i < n; i++) out.push(iter.next().toDate())
    return out
  } catch {
    return []
  }
}

// True when the expression is structurally valid per cron-parser.
export function isValid(str) {
  if (!str || typeof str !== 'string') return false
  try {
    CronExpressionParser.parse(str.trim())
    return true
  } catch {
    return false
  }
}

// Month/day-of-week label helpers for the chip UI.
export const MONTHS = [
  { n: 1,  short: 'Jan' }, { n: 2,  short: 'Feb' }, { n: 3,  short: 'Mar' },
  { n: 4,  short: 'Apr' }, { n: 5,  short: 'May' }, { n: 6,  short: 'Jun' },
  { n: 7,  short: 'Jul' }, { n: 8,  short: 'Aug' }, { n: 9,  short: 'Sep' },
  { n: 10, short: 'Oct' }, { n: 11, short: 'Nov' }, { n: 12, short: 'Dec' },
]

export const WEEKDAYS = [
  { n: 0, short: 'Sun' }, { n: 1, short: 'Mon' }, { n: 2, short: 'Tue' },
  { n: 3, short: 'Wed' }, { n: 4, short: 'Thu' }, { n: 5, short: 'Fri' },
  { n: 6, short: 'Sat' },
]

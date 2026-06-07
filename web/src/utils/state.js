// Factory functions for creating clean rule state objects.
// These mirror the Go types in internal/rule/types.go.

let _nextId = 1
export function uid() { return _nextId++ }

export function createRule(file = '') {
  return {
    id: uid(),
    file,  // Filename grouping — rules with same file go into one YAML output
    trigger: {
      type: 'nats',
      nats: { subject: '', reply: false, queue: '', debounce: null },
      http: { path: '', method: '', debounce: null },
      schedule: { cron: '', timezone: '' },
    },
    conditions: null,
    action: {
      type: 'nats',
      nats: createNATSAction(),
      http: createHTTPAction(),
      respond: createRespondAction(),
    },
  }
}

export function createNATSAction() {
  return {
    subject: '',
    payload: '',
    passthrough: false,
    merge: false,
    headers: {},
    request: false,
    timeout: '',
    forEach: '',
    filter: null,
    debounce: null,
  }
}

export function createRespondAction() {
  return {
    statusCode: 200,
    payload: '',
    passthrough: false,
    merge: false,
    headers: {},
  }
}

export function createHTTPAction() {
  return {
    url: '',
    method: 'POST',
    payload: '',
    passthrough: false,
    merge: false,
    headers: {},
    retry: null,
    publishResponse: null,
    forEach: '',
    filter: null,
    debounce: null,
  }
}

export function createPublishResponse() {
  return { subject: '' }
}

export function createConditions() {
  return { id: uid(), operator: 'and', items: [], groups: [] }
}

export function createCondition() {
  return { id: uid(), field: '', operator: 'eq', value: '', conditions: null }
}

export function createDebounce() {
  return { window: '', key: '' }
}

export function createRetry() {
  return { maxAttempts: 3, initialDelay: '1s', maxDelay: '30s' }
}

export const DEFAULT_FILENAME = 'untitled'

// Groups rules by filename. Returns array of { file, rules }.
// Rules with empty file are grouped under the default name.
export function groupRulesByFile(rules) {
  const map = new Map()
  for (const rule of rules) {
    const key = rule.file || DEFAULT_FILENAME
    if (!map.has(key)) map.set(key, [])
    map.get(key).push(rule)
  }
  return Array.from(map.entries()).map(([file, rules]) => ({ file, rules }))
}

// Returns the request/reply "mode" chips for a rule, for at-a-glance display in
// the sidebar. Mirrors the trigger/action combinations from the rule engine.
export function ruleModes(rule) {
  const modes = []
  const t = rule.trigger
  const a = rule.action
  if (t.type === 'nats' && t.nats.reply) {
    modes.push('reply')
  } else if (a.type === 'respond') {
    modes.push('respond')
  } else if (a.type === 'nats' && a.nats.request) {
    modes.push('bridge')
  }
  const activeAction = a.type === 'nats' ? a.nats : a.type === 'http' ? a.http : null
  if (activeAction?.forEach) modes.push('forEach')
  return modes
}

// One-line "trigger → action" summary for a rule. Handles every action type
// (including respond, which the old card summary dropped).
export function ruleSummary(rule) {
  const t = rule.trigger
  const a = rule.action
  let trigger = ''
  if (t.type === 'nats') trigger = `NATS ${t.nats.subject || '…'}`
  else if (t.type === 'http') trigger = `HTTP ${t.http.method || '*'} ${t.http.path || '…'}`
  else if (t.type === 'schedule') trigger = `Cron ${t.schedule.cron || '…'}`

  let action = ''
  if (a.type === 'nats') action = `NATS ${a.nats.subject || '…'}`
  else if (a.type === 'http') action = `HTTP ${a.http.method || ''} ${a.http.url || '…'}`.trim()
  else if (a.type === 'respond') action = 'Respond'

  return { trigger, action }
}

// Generates a filename not already used by any rule, for the "New file" action.
export function uniqueFileName(rules, base = 'rules') {
  const used = new Set(rules.map(r => r.file || DEFAULT_FILENAME))
  if (!used.has(base)) return base
  let n = 2
  while (used.has(`${base}-${n}`)) n++
  return `${base}-${n}`
}

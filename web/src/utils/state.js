// Factory functions for creating clean rule state objects.
// These mirror the Go types in internal/rule/types.go.

export function createRule(file = '') {
  return {
    file,  // Filename grouping — rules with same file go into one YAML output
    trigger: {
      type: 'nats',
      nats: { subject: '', debounce: null },
      http: { path: '', method: '', debounce: null },
      schedule: { cron: '', timezone: '' },
    },
    conditions: null,
    action: {
      type: 'nats',
      nats: createNATSAction(),
      http: createHTTPAction(),
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
    forEach: '',
    filter: null,
    debounce: null,
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
    forEach: '',
    filter: null,
    debounce: null,
  }
}

export function createConditions() {
  return { operator: 'and', items: [], groups: [] }
}

export function createCondition() {
  return { field: '', operator: 'eq', value: '', conditions: null }
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

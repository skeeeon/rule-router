// Client-side validation mirroring internal/rule/loader.go

export const VALID_OPERATORS = [
  'eq', 'neq', 'gt', 'lt', 'gte', 'lte',
  'contains', 'not_contains',
  'in', 'not_in',
  'exists', 'recent',
  'any', 'all', 'none',
]

export const ARRAY_OPERATORS = ['any', 'all', 'none']

export const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

export function validateRule(rule) {
  const errors = []
  validateTrigger(rule.trigger, errors)
  if (rule.conditions) {
    validateConditions(rule.conditions, 'conditions', errors)
  }
  validateAction(rule.action, errors)
  return errors
}

function validateTrigger(trigger, errors) {
  if (trigger.type === 'nats') {
    if (!trigger.nats.subject) {
      errors.push({ path: 'trigger.nats.subject', message: 'Subject is required' })
    } else {
      validateNATSSubject(trigger.nats.subject, 'trigger.nats.subject', errors)
    }
    if (trigger.nats.debounce) {
      validateDebounce(trigger.nats.debounce, 'trigger.nats.debounce', errors)
    }
  } else if (trigger.type === 'http') {
    if (!trigger.http.path) {
      errors.push({ path: 'trigger.http.path', message: 'Path is required' })
    } else if (!trigger.http.path.startsWith('/')) {
      errors.push({ path: 'trigger.http.path', message: 'Path must start with /' })
    }
    if (trigger.http.method && !HTTP_METHODS.includes(trigger.http.method.toUpperCase())) {
      errors.push({ path: 'trigger.http.method', message: 'Invalid HTTP method' })
    }
    if (trigger.http.debounce) {
      validateDebounce(trigger.http.debounce, 'trigger.http.debounce', errors)
    }
  } else if (trigger.type === 'schedule') {
    if (!trigger.schedule.cron) {
      errors.push({ path: 'trigger.schedule.cron', message: 'Cron expression is required' })
    } else if (!isValidCron(trigger.schedule.cron)) {
      errors.push({ path: 'trigger.schedule.cron', message: 'Must be a 5-field cron expression' })
    }
  }
}

function validateAction(action, errors) {
  if (action.type === 'nats') {
    const a = action.nats
    if (!a.subject) {
      errors.push({ path: 'action.nats.subject', message: 'Subject is required' })
    }
    if (a.passthrough && a.payload) {
      errors.push({ path: 'action.nats.payload', message: 'Passthrough is enabled — remove payload or disable passthrough' })
    }
    if (a.forEach) {
      validateForEach(a.forEach, 'action.nats.forEach', errors)
    }
    if (a.filter) {
      validateConditions(a.filter, 'action.nats.filter', errors)
    }
    if (a.debounce) {
      validateDebounce(a.debounce, 'action.nats.debounce', errors)
    }
  } else if (action.type === 'http') {
    const a = action.http
    if (!a.url) {
      errors.push({ path: 'action.http.url', message: 'URL is required' })
    } else if (!a.url.includes('{') && !a.url.startsWith('http://') && !a.url.startsWith('https://')) {
      errors.push({ path: 'action.http.url', message: 'URL must start with http:// or https://' })
    }
    if (!a.method) {
      errors.push({ path: 'action.http.method', message: 'Method is required' })
    } else if (!HTTP_METHODS.includes(a.method.toUpperCase())) {
      errors.push({ path: 'action.http.method', message: 'Invalid HTTP method' })
    }
    if (a.passthrough && a.payload) {
      errors.push({ path: 'action.http.payload', message: 'Passthrough is enabled — remove payload or disable passthrough' })
    }
    if (a.forEach) {
      validateForEach(a.forEach, 'action.http.forEach', errors)
    }
    if (a.filter) {
      validateConditions(a.filter, 'action.http.filter', errors)
    }
    if (a.retry) {
      if (a.retry.maxAttempts < 1) {
        errors.push({ path: 'action.http.retry.maxAttempts', message: 'Must be at least 1' })
      }
      if (a.retry.initialDelay && !isValidDuration(a.retry.initialDelay)) {
        errors.push({ path: 'action.http.retry.initialDelay', message: 'Invalid duration (e.g., "1s", "500ms")' })
      }
      if (a.retry.maxDelay && !isValidDuration(a.retry.maxDelay)) {
        errors.push({ path: 'action.http.retry.maxDelay', message: 'Invalid duration (e.g., "30s")' })
      }
    }
    if (a.debounce) {
      validateDebounce(a.debounce, 'action.http.debounce', errors)
    }
  }
}

function validateConditions(cond, prefix, errors) {
  if (!cond.operator || !['and', 'or'].includes(cond.operator)) {
    errors.push({ path: `${prefix}.operator`, message: 'Operator must be "and" or "or"' })
  }
  if (cond.items) {
    for (let i = 0; i < cond.items.length; i++) {
      validateConditionItem(cond.items[i], `${prefix}.items.${i}`, errors)
    }
  }
  if (cond.groups) {
    for (let i = 0; i < cond.groups.length; i++) {
      validateConditions(cond.groups[i], `${prefix}.groups.${i}`, errors)
    }
  }
}

function validateConditionItem(item, prefix, errors) {
  if (!item.field) {
    errors.push({ path: `${prefix}.field`, message: 'Field is required' })
  } else if (!isTemplateField(item.field)) {
    errors.push({ path: `${prefix}.field`, message: 'Field must use template syntax: {field}' })
  }
  if (!VALID_OPERATORS.includes(item.operator)) {
    errors.push({ path: `${prefix}.operator`, message: 'Invalid operator' })
  }
  if (ARRAY_OPERATORS.includes(item.operator)) {
    if (!item.conditions || (!item.conditions.items?.length && !item.conditions.groups?.length)) {
      errors.push({ path: `${prefix}.conditions`, message: `${item.operator} operator requires nested conditions` })
    } else {
      validateConditions(item.conditions, `${prefix}.conditions`, errors)
    }
  } else if (['in', 'not_in'].includes(item.operator)) {
    if (!Array.isArray(item.value) || item.value.length === 0) {
      errors.push({ path: `${prefix}.value`, message: `${item.operator} requires a comma-separated list of values` })
    }
  } else if (item.operator !== 'exists') {
    if (item.value === '' || item.value === null || item.value === undefined) {
      errors.push({ path: `${prefix}.value`, message: 'Value is required' })
    }
  }
}

function validateDebounce(debounce, prefix, errors) {
  if (!debounce.window) {
    errors.push({ path: `${prefix}.window`, message: 'Window is required' })
  } else if (!isValidDuration(debounce.window)) {
    errors.push({ path: `${prefix}.window`, message: 'Invalid duration (e.g., "5s", "1m")' })
  }
}

function validateForEach(forEach, path, errors) {
  if (!isTemplateField(forEach)) {
    errors.push({ path, message: 'forEach must use template syntax: {field}' })
  }
}

function validateNATSSubject(subject, path, errors) {
  const tokens = subject.split('.')
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i]
    if (t === '') {
      errors.push({ path, message: 'Subject cannot have empty tokens (..)' })
      return
    }
    if (t === '>' && i !== tokens.length - 1) {
      errors.push({ path, message: '> wildcard must be the last token' })
      return
    }
    if (t.includes('*') && t !== '*') {
      errors.push({ path, message: '* wildcard must be a full token' })
      return
    }
  }
}

// --- Helpers ---

function isTemplateField(s) {
  return s.includes('{') && s.includes('}')
}

function isValidCron(cron) {
  const parts = cron.trim().split(/\s+/)
  return parts.length === 5
}

function isValidDuration(s) {
  return /^\d+(\.\d+)?(ns|us|ms|s|m|h)$/.test(s)
}

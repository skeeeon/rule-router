// Hand-rolled YAML serializer for rule objects.
// The rule structure is well-defined, so we don't need a generic YAML library.

export function rulesToYaml(rules) {
  if (!rules.length) return ''
  return rules.map(ruleToYaml).join('\n')
}

function ruleToYaml(rule) {
  const lines = []
  lines.push('- trigger:')
  pushTrigger(lines, rule.trigger, 4)

  if (rule.conditions && hasConditions(rule.conditions)) {
    lines.push('  conditions:')
    pushConditions(lines, rule.conditions, 4)
  }

  lines.push('  action:')
  pushAction(lines, rule.action, 4)

  return lines.join('\n') + '\n'
}

function pushTrigger(lines, trigger, indent) {
  const pad = ' '.repeat(indent)
  if (trigger.type === 'nats') {
    lines.push(`${pad}nats:`)
    lines.push(`${pad}  subject: ${yamlString(trigger.nats.subject)}`)
    if (trigger.nats.debounce) {
      pushDebounce(lines, trigger.nats.debounce, indent + 2)
    }
  } else if (trigger.type === 'http') {
    lines.push(`${pad}http:`)
    lines.push(`${pad}  path: ${yamlString(trigger.http.path)}`)
    if (trigger.http.method) {
      lines.push(`${pad}  method: ${yamlString(trigger.http.method)}`)
    }
    if (trigger.http.debounce) {
      pushDebounce(lines, trigger.http.debounce, indent + 2)
    }
  } else if (trigger.type === 'schedule') {
    lines.push(`${pad}schedule:`)
    lines.push(`${pad}  cron: ${yamlString(trigger.schedule.cron)}`)
    if (trigger.schedule.timezone) {
      lines.push(`${pad}  timezone: ${yamlString(trigger.schedule.timezone)}`)
    }
  }
}

function pushConditions(lines, cond, indent) {
  const pad = ' '.repeat(indent)
  lines.push(`${pad}operator: ${cond.operator}`)

  if (cond.items.length > 0) {
    lines.push(`${pad}items:`)
    for (const item of cond.items) {
      pushConditionItem(lines, item, indent + 2)
    }
  }

  if (cond.groups && cond.groups.length > 0) {
    lines.push(`${pad}groups:`)
    for (const group of cond.groups) {
      lines.push(`${pad}  - operator: ${group.operator}`)
      if (group.items.length > 0) {
        lines.push(`${pad}    items:`)
        for (const item of group.items) {
          pushConditionItem(lines, item, indent + 6)
        }
      }
      if (group.groups && group.groups.length > 0) {
        lines.push(`${pad}    groups:`)
        for (const sub of group.groups) {
          lines.push(`${pad}      - operator: ${sub.operator}`)
          if (sub.items.length > 0) {
            lines.push(`${pad}        items:`)
            for (const item of sub.items) {
              pushConditionItem(lines, item, indent + 10)
            }
          }
        }
      }
    }
  }
}

function pushConditionItem(lines, item, indent) {
  const pad = ' '.repeat(indent)
  lines.push(`${pad}- field: ${yamlString(item.field)}`)
  lines.push(`${pad}  operator: ${yamlString(item.operator)}`)

  const isArrayOp = ['any', 'all', 'none'].includes(item.operator)
  if (isArrayOp && item.conditions && hasConditions(item.conditions)) {
    lines.push(`${pad}  conditions:`)
    pushConditions(lines, item.conditions, indent + 4)
  } else if (item.operator !== 'exists') {
    lines.push(`${pad}  value: ${yamlValue(item.value)}`)
  }
}

function pushAction(lines, action, indent) {
  const pad = ' '.repeat(indent)
  if (action.type === 'nats') {
    const a = action.nats
    lines.push(`${pad}nats:`)
    if (a.forEach) {
      lines.push(`${pad}  forEach: ${yamlString(a.forEach)}`)
    }
    if (a.filter && hasConditions(a.filter)) {
      lines.push(`${pad}  filter:`)
      pushConditions(lines, a.filter, indent + 4)
    }
    lines.push(`${pad}  subject: ${yamlString(a.subject)}`)
    pushPayloadFields(lines, a, indent + 2)
    pushHeaders(lines, a.headers, indent + 2)
    if (a.debounce) {
      pushDebounce(lines, a.debounce, indent + 2)
    }
  } else if (action.type === 'http') {
    const a = action.http
    lines.push(`${pad}http:`)
    if (a.forEach) {
      lines.push(`${pad}  forEach: ${yamlString(a.forEach)}`)
    }
    if (a.filter && hasConditions(a.filter)) {
      lines.push(`${pad}  filter:`)
      pushConditions(lines, a.filter, indent + 4)
    }
    lines.push(`${pad}  url: ${yamlString(a.url)}`)
    lines.push(`${pad}  method: ${a.method}`)
    pushPayloadFields(lines, a, indent + 2)
    pushHeaders(lines, a.headers, indent + 2)
    if (a.retry) {
      lines.push(`${pad}  retry:`)
      lines.push(`${pad}    maxAttempts: ${a.retry.maxAttempts}`)
      if (a.retry.initialDelay) {
        lines.push(`${pad}    initialDelay: ${yamlString(a.retry.initialDelay)}`)
      }
      if (a.retry.maxDelay) {
        lines.push(`${pad}    maxDelay: ${yamlString(a.retry.maxDelay)}`)
      }
    }
    if (a.debounce) {
      pushDebounce(lines, a.debounce, indent + 2)
    }
  }
}

function pushPayloadFields(lines, action, indent) {
  const pad = ' '.repeat(indent)
  if (action.passthrough) {
    lines.push(`${pad}passthrough: true`)
  } else if (action.merge && action.payload) {
    lines.push(`${pad}merge: true`)
    pushPayload(lines, action.payload, indent)
  } else if (action.payload && action.payload.trim()) {
    pushPayload(lines, action.payload, indent)
  }
}

function pushPayload(lines, payload, indent) {
  const pad = ' '.repeat(indent)
  if (payload.includes('\n') || (payload.startsWith('{') && payload.includes('"'))) {
    // Use block scalar for multi-line or JSON-like payloads
    lines.push(`${pad}payload: |`)
    for (const line of payload.split('\n')) {
      lines.push(`${pad}  ${line}`)
    }
  } else {
    lines.push(`${pad}payload: ${yamlString(payload)}`)
  }
}

function pushHeaders(lines, headers, indent) {
  if (!headers) return
  const entries = Object.entries(headers).filter(([k]) => k.trim())
  if (entries.length === 0) return
  const pad = ' '.repeat(indent)
  lines.push(`${pad}headers:`)
  for (const [key, value] of entries) {
    lines.push(`${pad}  ${key}: ${yamlString(value)}`)
  }
}

function pushDebounce(lines, debounce, indent) {
  const pad = ' '.repeat(indent)
  lines.push(`${pad}debounce:`)
  lines.push(`${pad}  window: ${yamlString(debounce.window)}`)
  if (debounce.key) {
    lines.push(`${pad}  key: ${yamlString(debounce.key)}`)
  }
}

// --- Helpers ---

function hasConditions(cond) {
  if (!cond) return false
  return (cond.items && cond.items.length > 0) || (cond.groups && cond.groups.length > 0)
}

function yamlValue(value) {
  if (value === null || value === undefined || value === '') return '""'
  if (typeof value === 'boolean') return value.toString()
  if (typeof value === 'number') return value.toString()
  if (Array.isArray(value)) {
    return '[' + value.map(v => yamlValue(v)).join(', ') + ']'
  }
  const str = String(value)
  // Try to preserve numeric values as unquoted
  if (/^-?\d+(\.\d+)?$/.test(str)) return str
  if (str === 'true' || str === 'false') return str
  return yamlString(str)
}

function yamlString(str) {
  if (str === null || str === undefined || str === '') return '""'
  const s = String(str)
  // Quote if contains special chars, templates, or could be ambiguous
  if (needsQuoting(s)) {
    return '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"'
  }
  return s
}

function needsQuoting(s) {
  if (s === '') return true
  if (s === 'true' || s === 'false' || s === 'null') return true
  if (/^-?\d+(\.\d+)?$/.test(s)) return true
  if (s.includes('{') || s.includes('}')) return true
  if (s.includes(':') || s.includes('#') || s.includes("'") || s.includes('"')) return true
  if (s.includes('\n')) return true
  if (s.startsWith(' ') || s.endsWith(' ')) return true
  if (s.includes('*') || s.includes('>') || s.includes('|') || s.includes('&')) return true
  return false
}

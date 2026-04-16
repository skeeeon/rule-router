// Parse YAML rule files back into form state objects.

import YAML from 'yaml'
import { createRule, createNATSAction, createHTTPAction, uid } from './state.js'

// Parse a YAML string into an array of form-state rule objects.
// Each rule gets the given filename assigned.
export function parseYamlToRules(yamlString, file = '') {
  const parsed = YAML.parse(yamlString)
  if (!Array.isArray(parsed)) return []
  return parsed.map(raw => rawToRule(raw, file))
}

function rawToRule(raw, file) {
  const rule = createRule(file)

  // Trigger
  if (raw.trigger?.nats) {
    rule.trigger.type = 'nats'
    rule.trigger.nats.subject = raw.trigger.nats.subject || ''
    if (raw.trigger.nats.debounce) {
      rule.trigger.nats.debounce = {
        window: raw.trigger.nats.debounce.window || '',
        key: raw.trigger.nats.debounce.key || '',
      }
    }
  } else if (raw.trigger?.http) {
    rule.trigger.type = 'http'
    rule.trigger.http.path = raw.trigger.http.path || ''
    rule.trigger.http.method = raw.trigger.http.method || ''
    if (raw.trigger.http.debounce) {
      rule.trigger.http.debounce = {
        window: raw.trigger.http.debounce.window || '',
        key: raw.trigger.http.debounce.key || '',
      }
    }
  } else if (raw.trigger?.schedule) {
    rule.trigger.type = 'schedule'
    rule.trigger.schedule.cron = raw.trigger.schedule.cron || ''
    rule.trigger.schedule.timezone = raw.trigger.schedule.timezone || ''
  }

  // Conditions
  if (raw.conditions) {
    rule.conditions = rawToConditions(raw.conditions)
  }

  // Action
  if (raw.action?.nats) {
    rule.action.type = 'nats'
    rule.action.nats = rawToNATSAction(raw.action.nats)
  } else if (raw.action?.http) {
    rule.action.type = 'http'
    rule.action.http = rawToHTTPAction(raw.action.http)
  }

  return rule
}

function rawToConditions(raw) {
  return {
    id: uid(),
    operator: raw.operator || 'and',
    items: (raw.items || []).map(rawToConditionItem),
    groups: (raw.groups || []).map(rawToConditions),
  }
}

function rawToConditionItem(raw) {
  const item = {
    id: uid(),
    field: raw.field || '',
    operator: raw.operator || 'eq',
    value: raw.value ?? '',
    conditions: null,
  }
  if (raw.conditions) {
    item.conditions = rawToConditions(raw.conditions)
  }
  return item
}

function rawToNATSAction(raw) {
  const action = createNATSAction()
  action.subject = raw.subject || ''
  action.payload = raw.payload || ''
  action.passthrough = raw.passthrough || false
  action.merge = raw.merge || false
  action.headers = raw.headers || {}
  action.forEach = raw.forEach || ''
  if (raw.filter) {
    action.filter = rawToConditions(raw.filter)
  }
  if (raw.debounce) {
    action.debounce = { window: raw.debounce.window || '', key: raw.debounce.key || '' }
  }
  return action
}

function rawToHTTPAction(raw) {
  const action = createHTTPAction()
  action.url = raw.url || ''
  action.method = raw.method || 'POST'
  action.payload = raw.payload || ''
  action.passthrough = raw.passthrough || false
  action.merge = raw.merge || false
  action.headers = raw.headers || {}
  action.forEach = raw.forEach || ''
  if (raw.retry) {
    action.retry = {
      maxAttempts: raw.retry.maxAttempts || 3,
      initialDelay: raw.retry.initialDelay || '1s',
      maxDelay: raw.retry.maxDelay || '30s',
    }
  }
  if (raw.filter) {
    action.filter = rawToConditions(raw.filter)
  }
  if (raw.debounce) {
    action.debounce = { window: raw.debounce.window || '', key: raw.debounce.key || '' }
  }
  return action
}

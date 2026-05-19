// Context-variable catalog used by FieldSuggestInput for autocomplete.
// Mirrors the runtime variables documented in HelpModal.vue and resolved by
// internal/rule (subject_context, http_context, condition_resolver).
//
// Each entry has the same shape as inspectedFields ({ path, type, sample }),
// plus a `category` label so the dropdown can show where the variable comes
// from.

// Variables available in every rule, regardless of trigger.
const ALWAYS_AVAILABLE = [
  // Time
  { path: '@time.hour',       type: 'number',  category: 'Time', sample: 'Hour 0-23' },
  { path: '@time.minute',     type: 'number',  category: 'Time', sample: 'Minute 0-59' },
  { path: '@day.name',        type: 'string',  category: 'Time', sample: 'monday-sunday' },
  { path: '@day.number',      type: 'number',  category: 'Time', sample: '1-7 (Mon=1)' },
  { path: '@date.year',       type: 'number',  category: 'Date', sample: 'Full year' },
  { path: '@date.month',      type: 'number',  category: 'Date', sample: 'Month 1-12' },
  { path: '@date.day',        type: 'number',  category: 'Date', sample: 'Day 1-31' },
  { path: '@date.iso',        type: 'string',  category: 'Date', sample: '2006-01-02' },
  { path: '@timestamp.unix',  type: 'number',  category: 'Time', sample: 'Unix seconds' },
  { path: '@timestamp.iso',   type: 'string',  category: 'Time', sample: 'RFC3339' },

  // Functions
  { path: '@timestamp()',     type: 'func',    category: 'Func', sample: 'Current RFC3339 timestamp' },
  { path: '@uuid4()',         type: 'func',    category: 'Func', sample: 'Random UUID v4' },
  { path: '@uuid7()',         type: 'func',    category: 'Func', sample: 'Time-based UUID v7' },
]

// Variables tied to a NATS-triggered rule.
const NATS_VARS = [
  { path: '@subject',         type: 'string',  category: 'NATS', sample: 'Full subject' },
  { path: '@subject.count',   type: 'number',  category: 'NATS', sample: 'Token count' },
  { path: '@signature.valid', type: 'boolean', category: 'Sig',  sample: 'NKey signature valid' },
  { path: '@signature.pubkey',type: 'string',  category: 'Sig',  sample: 'NKey public key' },
]

// Variables tied to an HTTP-triggered rule.
const HTTP_VARS = [
  { path: '@path',            type: 'string',  category: 'HTTP', sample: 'Full path' },
  { path: '@path.count',      type: 'number',  category: 'HTTP', sample: 'Path segment count' },
  { path: '@method',          type: 'string',  category: 'HTTP', sample: 'HTTP method' },
  { path: '@header.',         type: 'string',  category: 'HTTP', sample: 'Header by name (e.g. @header.X-Name)' },
]

// Variables available inside forEach iteration. Always-on alongside message
// fields, but only really meaningful when an action has forEach configured.
// We include them unconditionally — the cost is one extra dropdown entry.
const FOREACH_VARS = [
  { path: '@msg.', type: 'string', category: 'forEach', sample: 'Field from original root message (e.g. @msg.user.id)' },
]

// Split a NATS subject into tokens for @subject.N suggestions.
// "sensors.*.room1.>" -> ["sensors", "*", "room1", ">"]
function natsSubjectTokens(subject) {
  if (!subject) return []
  return subject.split('.').filter(t => t !== '')
}

// Split an HTTP path into segments for @path.N suggestions.
// "/webhooks/github/pr" -> ["webhooks", "github", "pr"]
function httpPathTokens(path) {
  if (!path) return []
  return path.replace(/^\/+|\/+$/g, '').split('/').filter(t => t !== '')
}

// Build per-token variables, annotating wildcards/literals where useful.
function natsTokenVars(subject) {
  const tokens = natsSubjectTokens(subject)
  return tokens.map((tok, i) => ({
    path: `@subject.${i}`,
    type: 'string',
    category: 'NATS',
    sample: tok === '*' || tok === '>' ? `wildcard (${tok})` : `"${tok}"`,
  }))
}

function httpTokenVars(path) {
  const tokens = httpPathTokens(path)
  return tokens.map((tok, i) => ({
    path: `@path.${i}`,
    type: 'string',
    category: 'HTTP',
    sample: `"${tok}"`,
  }))
}

// buildContextVars returns the suggestion list relevant to the given trigger.
// The trigger object follows the shape produced by createRule() in state.js.
export function buildContextVars(trigger) {
  const out = [...ALWAYS_AVAILABLE]

  const type = trigger?.type
  if (type === 'nats') {
    out.push(...natsTokenVars(trigger.nats?.subject))
    out.push(...NATS_VARS)
    out.push(...FOREACH_VARS)
  } else if (type === 'http') {
    out.push(...httpTokenVars(trigger.http?.path))
    out.push(...HTTP_VARS)
    out.push(...FOREACH_VARS)
  }
  // Schedule rules: only ALWAYS_AVAILABLE — no message, no subject, no path.

  return out
}

// Exposed for tests / future reuse.
export { ALWAYS_AVAILABLE, NATS_VARS, HTTP_VARS, FOREACH_VARS, natsSubjectTokens, httpPathTokens }

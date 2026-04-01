// Extracts all dot-notation field paths from a JSON object.
// Returns an array of { path, type, sample } for autocomplete suggestions.

export function extractFields(obj, prefix = '', depth = 0) {
  const fields = []
  if (depth > 10 || obj === null || obj === undefined) return fields

  if (Array.isArray(obj)) {
    // For arrays, describe the array itself and recurse into the first element
    fields.push({ path: prefix || '@items', type: 'array', sample: `[${obj.length} items]` })
    if (obj.length > 0 && typeof obj[0] === 'object' && obj[0] !== null) {
      fields.push(...extractFields(obj[0], prefix ? `${prefix}.0` : '0', depth + 1))
    }
    return fields
  }

  if (typeof obj !== 'object') return fields

  for (const [key, value] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${key}` : key

    if (value === null || value === undefined) {
      fields.push({ path, type: 'null', sample: 'null' })
    } else if (Array.isArray(value)) {
      fields.push({ path, type: 'array', sample: `[${value.length} items]` })
      // Recurse into first element to show nested array fields
      if (value.length > 0 && typeof value[0] === 'object' && value[0] !== null) {
        fields.push(...extractFields(value[0], `${path}.0`, depth + 1))
      }
    } else if (typeof value === 'object') {
      fields.push({ path, type: 'object', sample: '{...}' })
      fields.push(...extractFields(value, path, depth + 1))
    } else {
      const type = typeof value
      const sample = String(value).length > 40 ? String(value).slice(0, 40) + '...' : String(value)
      fields.push({ path, type, sample })
    }
  }

  return fields
}

export function parseAndExtract(jsonString) {
  const trimmed = jsonString.trim()
  if (!trimmed) return { fields: [], error: null }
  try {
    const obj = JSON.parse(trimmed)
    return { fields: extractFields(obj), error: null }
  } catch (e) {
    return { fields: [], error: e.message }
  }
}


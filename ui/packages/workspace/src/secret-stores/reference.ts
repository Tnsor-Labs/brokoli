import type { SecretShape } from '@brokoli/api'

/** The parts of a secret://<store>/<path>[?version=<v>][#<field>] reference (ADR-041). */
export type SecretRefParts = { store: string; path: string; version: string; field: string }

export const SECRET_SCHEME = 'secret://'

const STORE_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** True for a value that is a secret-store reference: only when it starts with the scheme. */
export function isSecretRef(value: string | undefined): value is string {
  return typeof value === 'string' && value.startsWith(SECRET_SCHEME)
}

export function validStoreName(name: string) {
  return STORE_NAME.test(name)
}

/**
 * Parses a reference the way the server does: the field after '#', then
 * ?version=, then the store up to the first '/'. The path is kept exactly,
 * including a leading '/' (an SSM parameter name). Returns null for anything
 * that is not a well-formed reference.
 */
export function parseSecretRef(ref: string | undefined): SecretRefParts | null {
  if (!isSecretRef(ref)) return null
  let body = ref.slice(SECRET_SCHEME.length)
  let field = ''
  const hash = body.indexOf('#')
  if (hash >= 0) {
    field = body.slice(hash + 1)
    body = body.slice(0, hash)
    if (!field) return null
  }
  let version = ''
  const q = body.indexOf('?')
  if (q >= 0) {
    const params = new URLSearchParams(body.slice(q + 1))
    for (const key of params.keys()) if (key !== 'version') return null
    version = params.get('version') ?? ''
    body = body.slice(0, q)
  }
  const slash = body.indexOf('/')
  if (slash < 0) return null
  const store = body.slice(0, slash)
  const path = body.slice(slash + 1)
  if (!validStoreName(store) || !path.replace(/\//g, '') || path.includes('..')) return null
  return { store, path, version, field }
}

/** Composes a reference from its parts; the inverse of parseSecretRef. */
export function composeSecretRef(parts: SecretRefParts): string {
  let ref = `${SECRET_SCHEME}${parts.store}/${parts.path}`
  if (parts.version) ref += `?version=${encodeURIComponent(parts.version)}`
  if (parts.field) ref += `#${parts.field}`
  return ref
}

/** What is wrong with a reference being composed, or '' when it is complete for this shape. */
export function secretRefProblem(parts: SecretRefParts, shape: SecretShape | undefined): string {
  if (!parts.store) return 'Choose a secret store'
  if (!parts.path.replace(/\//g, '')) return 'Enter the secret path'
  if (parts.path.includes('..')) return "The path may not contain '..'"
  if (shape === 'map' && !parts.field) return "This store's secrets are maps of fields: name one"
  if (shape === 'string' && parts.field)
    return "This store's secrets are single values: leave the field empty"
  return ''
}

/**
 * Sets one string value inside an extra-settings JSON object to a reference,
 * keeping every other key. An empty or invalid document starts a new object.
 */
export function setExtraReference(extra: string, key: string, ref: string): string {
  let doc: Record<string, unknown> = {}
  if (extra.trim()) {
    try {
      const parsed = JSON.parse(extra)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed))
        doc = parsed as Record<string, unknown>
    } catch {
      // A malformed document is replaced; the form already marks it invalid.
    }
  }
  doc[key] = ref
  return JSON.stringify(doc, null, 2)
}

import type { ConnectionTypeMeta } from '@brokoli/api'

/*
 * Facts about connection types that the server's /api/connection-types
 * catalog does not carry, taken from models/connection.go on core main.
 */

/** Port BuildURI substitutes when the stored port is 0. */
export const DEFAULT_PORT: Record<string, string> = {
  postgres: '5432',
  redshift: '5439',
  mysql: '3306',
  clickhouse: '9000',
  mssql: '1433',
  sftp: '22',
  http: '443 (80 means plain http)',
}

/** Types some node can actually use: database URIs, http for API nodes, sftp for file nodes (ADR-040). No node reads s3. */
export const USABLE_BY_NODES = new Set(['postgres', 'redshift', 'mysql', 'sqlite', 'mssql', 'clickhouse', 'http', 'sftp'])

/** Driver options BuildURI reads from `extra` (models/connection.go allowlist). */
export const DRIVER_OPTIONS: Record<string, string[]> = {
  postgres: ['sslmode', 'sslrootcert', 'sslcert', 'sslkey', 'application_name', 'connect_timeout', 'target_session_attrs'],
  redshift: ['sslmode', 'sslrootcert', 'sslcert', 'sslkey', 'application_name', 'connect_timeout'],
  mysql: ['tls', 'charset', 'collation', 'parseTime', 'loc', 'timeout', 'readTimeout', 'writeTimeout', 'interpolateParams', 'maxAllowedPacket', 'clientFoundRows'],
  mssql: ['encrypt', 'TrustServerCertificate', 'hostNameInCertificate', 'connection timeout', 'dial timeout', 'app name'],
  clickhouse: ['secure', 'dial_timeout', 'read_timeout', 'compress'],
  snowflake: ['warehouse', 'role', 'authenticator', 'loginTimeout', 'application'],
}

/** Stored ref schemes that the server returns verbatim and that win over a typed secret. */
export function externalSecret(ref: string | undefined) {
  if (!ref || ref.startsWith('encrypted://')) return null
  const scheme = ref.split('://')[0]
  return scheme ? { scheme, ref } : null
}

export const CATEGORY_LABEL: Record<string, string> = {
  database: 'Databases',
  storage: 'Storage',
  api: 'APIs and files',
  other: 'Other',
}

const ORDER = ['database', 'storage', 'api', 'other']

export function groupTypes(types: ConnectionTypeMeta[]) {
  const groups = new Map<string, ConnectionTypeMeta[]>()
  for (const t of types) {
    const cat = ORDER.includes(t.category) ? t.category : 'other'
    groups.set(cat, [...(groups.get(cat) ?? []), t])
  }
  return ORDER.filter((c) => groups.has(c)).map((c) => ({ category: c, types: groups.get(c)! }))
}

/** Fields to render for a type: the server's list, plus driver options where BuildURI reads them, minus a port Snowflake ignores. */
export function formFields(type: string, meta: ConnectionTypeMeta | undefined) {
  const base = meta?.fields?.length ? [...meta.fields] : ['host', 'port', 'schema', 'login', 'password', 'extra']
  if (DRIVER_OPTIONS[type] && !base.includes('extra')) base.push('extra')
  return type === 'snowflake' ? base.filter((f) => f !== 'port') : base
}

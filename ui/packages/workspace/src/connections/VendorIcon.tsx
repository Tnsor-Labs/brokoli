import {
  siClickhouse,
  siDatabricks,
  siGooglebigquery,
  siGooglecloud,
  siMysql,
  siPostgresql,
  siSnowflake,
  siSqlite,
  type SimpleIcon,
} from 'simple-icons'
import { CATEGORY_ICON } from './ConnectionForm'

/*
 * Vendor marks for the connection type picker.
 *
 * Real brand logos come from the simple-icons set (a maintained,
 * licensed brand-icon library) so they are recognisable and consistent,
 * not reproduced by hand. Several vendors (Oracle, Microsoft and some AWS
 * services) have had their logos removed from that set at their own
 * request, so we do not reproduce them: those show a short lettermark
 * instead, which is clearer than a repeated database glyph and is plainly
 * not their logo. Anything else falls back to its category icon.
 */
const BRAND: Record<string, SimpleIcon> = {
  postgres: siPostgresql,
  mysql: siMysql,
  clickhouse: siClickhouse,
  snowflake: siSnowflake,
  bigquery: siGooglebigquery,
  databricks: siDatabricks,
  sqlite: siSqlite,
  gcs: siGooglecloud,
}

const LETTER: Record<string, string> = {
  redshift: 'RS',
  oracle: 'OR',
  mssql: 'SQL',
  s3: 'S3',
  azure_blob: 'AZ',
}

export function VendorIcon({ type, category, size = 18 }: { type: string; category: string; size?: number }) {
  const brand = BRAND[type]
  if (brand) {
    return (
      <svg role="img" aria-hidden="true" viewBox="0 0 24 24" width={size} height={size} fill={`#${brand.hex}`}>
        <path d={brand.path} />
      </svg>
    )
  }
  const letter = LETTER[type]
  if (letter) return <span className="ws-vendor-letter">{letter}</span>
  const Icon = CATEGORY_ICON[category] ?? CATEGORY_ICON.other
  return <Icon size={size - 2} aria-hidden="true" />
}

import sprite from './assets/brand-sprite.svg?url'
import { cx } from './cx'

/*
 * Brand icon set from core (brand/icons/sprite.svg): product navigation
 * glyphs and the fifteen node-type glyphs. Every symbol draws in
 * currentColor, so the caller sets the color.
 */
export type BrandIconName =
  | 'bk-dashboard'
  | 'bk-pipelines'
  | 'bk-calendar'
  | 'bk-lineage'
  | 'bk-dependencies'
  | 'bk-variables'
  | 'bk-connections'
  | 'bk-plugins'
  | 'bk-settings'
  | 'bk-workspaces'
  | 'bk-workers'
  | 'bk-audit-log'
  | 'bk-git-sync'
  | 'bk-organization'
  | 'bk-api'
  | 'bk-support'
  | 'bk-account'
  | 'bk-sign-out'
  | 'bk-collapse'
  | 'bk-get-started'
  | 'bk-node-file-source'
  | 'bk-node-api-source'
  | 'bk-node-database-source'
  | 'bk-node-transform'
  | 'bk-node-python-code'
  | 'bk-node-join'
  | 'bk-node-quality-check'
  | 'bk-node-sql-generate'
  | 'bk-node-file-output'
  | 'bk-node-database-sink'
  | 'bk-node-api-sink'
  | 'bk-node-integration'
  | 'bk-node-notify'
  | 'bk-node-db-migration'
  | 'bk-node-if-else'

export function BrandIcon({ name, size = 18, className }: { name: BrandIconName; size?: number; className?: string }) {
  return (
    <svg className={cx('bk-brand-icon', className)} width={size} height={size} viewBox="0 0 20 20" aria-hidden="true" focusable="false">
      <use href={`${sprite}#${name}`} />
    </svg>
  )
}

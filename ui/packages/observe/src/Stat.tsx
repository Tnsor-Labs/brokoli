import type { ReactNode } from 'react'
import { cx } from '@brokoli/ui'

export type StatTone = 'neutral' | 'success' | 'running' | 'warning' | 'danger'

/** A figure with its label and an optional line of context under it. */
export function Stat({
  label,
  value,
  foot,
  tone = 'neutral',
  title,
  children,
}: {
  label: string
  value: ReactNode
  foot?: ReactNode
  tone?: StatTone
  title?: string
  children?: ReactNode
}) {
  return (
    <div className={cx('ob-stat', `is-${tone}`)} title={title}>
      <span className="ob-stat-label">{label}</span>
      <strong className="ob-stat-value">{value}</strong>
      {foot && <span className="ob-stat-foot">{foot}</span>}
      {children}
    </div>
  )
}

export const rateTone = (rate: number | null): StatTone => (rate === null ? 'neutral' : rate >= 95 ? 'success' : rate >= 80 ? 'warning' : 'danger')

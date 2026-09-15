import { useId, type ReactNode } from 'react'
import { Badge, cx, type Tone } from '@brokoli/ui'

export function Panel({
  title,
  subtitle,
  count,
  tone = 'neutral',
  action,
  children,
  className,
}: {
  title: string
  subtitle?: ReactNode
  count?: ReactNode
  tone?: Tone
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  const id = useId()
  return (
    <section className={cx('ob-panel', className)} aria-labelledby={id}>
      <header className="ob-panel-head">
        <div>
          <h2 id={id}>
            {title}
            {count !== undefined && <Badge tone={tone}>{count}</Badge>}
          </h2>
          {subtitle && <p>{subtitle}</p>}
        </div>
        {action}
      </header>
      <div className="ob-panel-body">{children}</div>
    </section>
  )
}

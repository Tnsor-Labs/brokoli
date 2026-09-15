import { useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { cx } from './cx'
import { Eyebrow, IconButton } from './primitives'

/*
 * Application frame: a sidebar and a scrolling content region. The sidebar
 * contents (brand, navigation, account) are supplied by each application,
 * which is what keeps Community and Enterprise navigation separate while the
 * frame itself is shared.
 */
export function AppShell({
  sidebar,
  children,
  collapsed = false,
}: {
  sidebar: ReactNode
  children: ReactNode
  collapsed?: boolean
}) {
  return (
    <div className={cx('bk-shell', collapsed && 'is-collapsed')}>
      <aside className="bk-sidebar">{sidebar}</aside>
      <main className="bk-shell-content" id="main">
        {children}
      </main>
    </div>
  )
}

export function NavSection({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <div className="bk-nav-section">
      {label && <span className="bk-nav-section-label">{label}</span>}
      <nav aria-label={label}>{children}</nav>
    </div>
  )
}

export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
  meta,
  className,
}: {
  eyebrow?: ReactNode
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  meta?: ReactNode
  className?: string
}) {
  return (
    <header className={cx('bk-page-header', className)}>
      <div className="bk-page-header-text">
        {eyebrow && <Eyebrow>{eyebrow}</Eyebrow>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
        {meta && <div className="bk-page-header-meta">{meta}</div>}
      </div>
      {actions && <div className="bk-page-header-actions">{actions}</div>}
    </header>
  )
}

export function Page({ children, className, wide = false }: { children: ReactNode; className?: string; wide?: boolean }) {
  return <div className={cx('bk-page', wide && 'is-wide', className)}>{children}</div>
}

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  className,
}: {
  items: { id: T; label: ReactNode; count?: number; disabled?: boolean }[]
  value: T
  onChange: (id: T) => void
  label: string
  className?: string
}) {
  const base = useId()
  const list = useRef<HTMLDivElement>(null)
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return
    const enabled = items.filter((i) => !i.disabled)
    const index = enabled.findIndex((i) => i.id === value)
    const next = enabled[(index + (e.key === 'ArrowRight' ? 1 : -1) + enabled.length) % enabled.length]
    if (!next) return
    onChange(next.id)
    list.current?.querySelector<HTMLElement>(`[data-tab="${next.id}"]`)?.focus()
  }
  return (
    <div ref={list} className={cx('bk-tabs', className)} role="tablist" aria-label={label} onKeyDown={onKeyDown}>
      {items.map((item) => (
        <button
          key={item.id}
          id={`${base}-${item.id}`}
          data-tab={item.id}
          type="button"
          role="tab"
          aria-selected={item.id === value}
          tabIndex={item.id === value ? 0 : -1}
          disabled={item.disabled}
          className={cx('bk-tab', item.id === value && 'is-active')}
          onClick={() => onChange(item.id)}
        >
          {item.label}
          {item.count !== undefined && <span className="bk-tab-count">{item.count}</span>}
        </button>
      ))}
    </div>
  )
}

export function Pagination({
  page,
  pageSize,
  total,
  onPage,
  label = 'items',
}: {
  /** 1-based. */
  page: number
  pageSize: number
  total: number
  onPage: (page: number) => void
  label?: string
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  if (total <= pageSize) return null
  const from = (page - 1) * pageSize + 1
  const to = Math.min(total, page * pageSize)
  return (
    <nav className="bk-pagination" aria-label="Pagination">
      <span>
        {from}-{to} of {total} {label}
      </span>
      <div>
        <IconButton label="Previous page" size="sm" variant="secondary" disabled={page <= 1} onClick={() => onPage(page - 1)}>
          <ChevronLeft size={15} aria-hidden="true" />
        </IconButton>
        <span className="bk-pagination-page">
          {page} / {pages}
        </span>
        <IconButton label="Next page" size="sm" variant="secondary" disabled={page >= pages} onClick={() => onPage(page + 1)}>
          <ChevronRight size={15} aria-hidden="true" />
        </IconButton>
      </div>
    </nav>
  )
}

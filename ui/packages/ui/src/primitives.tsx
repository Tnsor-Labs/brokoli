import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { cx } from './cx'

export type Tone =
  | 'neutral'
  | 'accent'
  | 'success'
  | 'running'
  | 'warning'
  | 'danger'
  | 'queued'
  | 'cancelled'

/*
 * The mark is the landing page's broccoli, desaturated the same way
 * (grayscale filter in CSS) so it reads as a signature, not a color accent.
 */
export function Brand({
  edition,
  compact = false,
  className,
}: {
  edition?: 'Community' | 'Enterprise'
  compact?: boolean
  className?: string
}) {
  return (
    <span className={cx('bk-brand', className)}>
      <svg className="bk-brand-mark" viewBox="0 0 32 32" fill="none" aria-hidden="true">
        <path d="M16 29V17M16 20l-5-5M16 22l6-6" stroke="#38e6a5" strokeWidth="2" strokeLinecap="round" />
        <circle cx="16" cy="9" r="5" fill="#1cae7a" />
        <circle cx="8" cy="12" r="4" fill="#31d89b" />
        <circle cx="24" cy="12" r="4" fill="#31d89b" />
        <circle cx="11" cy="6" r="4" fill="#24996f" />
        <circle cx="21" cy="6" r="4" fill="#24996f" />
        <circle cx="16" cy="4" r="3.5" fill="#46efad" />
      </svg>
      {!compact && (
        <span className="bk-brand-word">
          Brokoli
          {edition && <small>{edition}</small>}
        </span>
      )}
    </span>
  )
}

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'sm' | 'md' | 'lg'
  loading?: boolean
  icon?: ReactNode
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading = false, icon, className, children, disabled, type, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type ?? 'button'}
      className={cx('bk-button', `bk-button-${variant}`, `bk-button-${size}`, className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading ? <Spinner size="sm" /> : icon}
      {children}
    </button>
  )
})

export type IconButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'aria-label'> & {
  /** Required: an icon-only control has no other accessible name. */
  label: string
  size?: 'sm' | 'md'
  variant?: 'ghost' | 'secondary' | 'danger'
  active?: boolean
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, size = 'md', variant = 'ghost', active = false, className, type, title, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type ?? 'button'}
      aria-label={label}
      title={title ?? label}
      aria-pressed={active || undefined}
      className={cx('bk-icon-button', `bk-icon-button-${size}`, `bk-icon-button-${variant}`, active && 'is-active', className)}
      {...props}
    />
  )
})

export function Badge({
  children,
  tone = 'neutral',
  dot = false,
  className,
  title,
}: {
  children: ReactNode
  tone?: Tone
  dot?: boolean
  className?: string
  title?: string
}) {
  return (
    <span className={cx('bk-badge', `bk-tone-${tone}`, className)} title={title}>
      {dot && <i className="bk-badge-dot" aria-hidden="true" />}
      {children}
    </span>
  )
}

export function Spinner({ size = 'md', label = 'Loading' }: { size?: 'sm' | 'md' | 'lg'; label?: string }) {
  return <span className={cx('bk-spinner', `bk-spinner-${size}`)} role="status" aria-label={label} />
}

export function Skeleton({
  width,
  height = 14,
  className,
}: {
  width?: number | string
  height?: number | string
  className?: string
}) {
  return <span className={cx('bk-skeleton', className)} style={{ width, height }} aria-hidden="true" />
}

export function Eyebrow({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx('bk-eyebrow', className)}>{children}</span>
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="bk-kbd">{children}</kbd>
}

export function Dot({ tone = 'neutral', pulse = false }: { tone?: Tone; pulse?: boolean }) {
  return <i className={cx('bk-dot', `bk-tone-${tone}`, pulse && 'is-pulsing')} aria-hidden="true" />
}

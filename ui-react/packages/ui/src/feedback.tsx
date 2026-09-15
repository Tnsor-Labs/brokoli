import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { cx } from './cx'
import { Badge, Dot, type Tone } from './primitives'

/*
 * Run and node status vocabulary. Anything the server sends that is not in
 * this table still renders, as its raw text in the neutral tone, and is
 * reported in the console: an unknown status is a contract change, and
 * hiding it would make a new state look like nothing happened.
 */
const STATUS: Record<string, { label: string; tone: Tone; pulse?: boolean }> = {
  success: { label: 'Succeeded', tone: 'success' },
  succeeded: { label: 'Succeeded', tone: 'success' },
  completed: { label: 'Completed', tone: 'success' },
  running: { label: 'Running', tone: 'running', pulse: true },
  retrying: { label: 'Retrying', tone: 'warning', pulse: true },
  pending: { label: 'Pending', tone: 'queued' },
  queued: { label: 'Queued', tone: 'queued' },
  waiting: { label: 'Waiting', tone: 'queued' },
  blocked: { label: 'Blocked', tone: 'warning' },
  skipped: { label: 'Skipped', tone: 'queued' },
  failed: { label: 'Failed', tone: 'danger' },
  error: { label: 'Error', tone: 'danger' },
  timeout: { label: 'Timed out', tone: 'danger' },
  cancelled: { label: 'Cancelled', tone: 'cancelled' },
  canceled: { label: 'Cancelled', tone: 'cancelled' },
}

const reported = new Set<string>()

export function statusMeta(status: string | null | undefined) {
  const key = (status ?? '').toLowerCase()
  const known = STATUS[key]
  if (known) return known
  if (key && !reported.has(key)) {
    reported.add(key)
    console.warn(`[brokoli-ui] unknown status "${status}" rendered as neutral`)
  }
  return { label: status || 'Unknown', tone: 'neutral' as Tone, pulse: false }
}

export function StatusBadge({ status, className }: { status: string | null | undefined; className?: string }) {
  const meta = statusMeta(status)
  return (
    <Badge tone={meta.tone} className={cx('bk-status', className)}>
      <Dot tone={meta.tone} pulse={meta.pulse} />
      {meta.label}
    </Badge>
  )
}

export function EmptyState({
  icon,
  title,
  children,
  action,
  className,
}: {
  icon?: ReactNode
  title: ReactNode
  children?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cx('bk-empty', className)}>
      {icon && <span className="bk-empty-icon">{icon}</span>}
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {action && <div className="bk-empty-action">{action}</div>}
    </div>
  )
}

const CALLOUT_ICON = {
  info: Info,
  success: CheckCircle2,
  warning: AlertTriangle,
  danger: XCircle,
}

export function Callout({
  tone = 'info',
  title,
  children,
  action,
  onDismiss,
  className,
}: {
  tone?: 'info' | 'success' | 'warning' | 'danger'
  title?: ReactNode
  children?: ReactNode
  action?: ReactNode
  onDismiss?: () => void
  className?: string
}) {
  const Icon = CALLOUT_ICON[tone]
  return (
    <div className={cx('bk-callout', `bk-callout-${tone}`, className)} role={tone === 'danger' ? 'alert' : 'status'}>
      <Icon size={16} aria-hidden="true" />
      <div className="bk-callout-body">
        {title && <strong>{title}</strong>}
        {children && <div>{children}</div>}
      </div>
      {action}
      {onDismiss && (
        <button type="button" className="bk-callout-dismiss" aria-label="Dismiss" onClick={onDismiss}>
          <X size={14} aria-hidden="true" />
        </button>
      )}
    </div>
  )
}

type Toast = { id: number; tone: 'success' | 'danger' | 'info' | 'warning'; title: string; detail?: string }
type ToastApi = {
  success: (title: string, detail?: string) => void
  error: (title: string, detail?: unknown) => void
  info: (title: string, detail?: string) => void
  warning: (title: string, detail?: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function errorMessage(error: unknown): string {
  if (!error) return ''
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return JSON.stringify(error)
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const next = useRef(0)
  const dismiss = useCallback((id: number) => setToasts((all) => all.filter((t) => t.id !== id)), [])
  const push = useCallback(
    (tone: Toast['tone'], title: string, detail?: string) => {
      const id = ++next.current
      setToasts((all) => [...all.slice(-4), { id, tone, title, detail }])
      // Errors stay long enough to read the server's message.
      setTimeout(() => dismiss(id), tone === 'danger' ? 9000 : 4200)
    },
    [dismiss],
  )
  const api = useMemo<ToastApi>(
    () => ({
      success: (title, detail) => push('success', title, detail),
      info: (title, detail) => push('info', title, detail),
      warning: (title, detail) => push('warning', title, detail),
      error: (title, detail) => push('danger', title, detail === undefined ? undefined : errorMessage(detail)),
    }),
    [push],
  )
  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="bk-toasts" aria-live="polite" aria-relevant="additions">
        {toasts.map((t) => {
          const Icon = CALLOUT_ICON[t.tone === 'danger' ? 'danger' : t.tone]
          return (
            <div key={t.id} className={cx('bk-toast', `bk-tone-${t.tone === 'info' ? 'accent' : t.tone}`)} role={t.tone === 'danger' ? 'alert' : 'status'}>
              <Icon size={16} aria-hidden="true" />
              <div className="bk-callout-body">
                <strong>{t.title}</strong>
                {t.detail && <div>{t.detail}</div>}
              </div>
              <button type="button" className="bk-callout-dismiss" aria-label="Dismiss notification" onClick={() => dismiss(t.id)}>
                <X size={14} aria-hidden="true" />
              </button>
            </div>
          )
        })}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastApi {
  const api = useContext(ToastContext)
  if (!api) throw new Error('useToast must be used within ToastProvider')
  return api
}

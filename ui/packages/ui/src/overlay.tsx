import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { MoreHorizontal, X } from 'lucide-react'
import { cx } from './cx'
import { Button, IconButton } from './primitives'

const FOCUSABLE =
  'a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'

let openModals = 0

/*
 * Modal dialog. Escape and the backdrop close it, focus moves inside on
 * open, Tab cycles within it, and focus returns to the opener on close.
 */
export function Modal({
  title,
  description,
  onClose,
  size = 'md',
  footer,
  children,
  dismissible = true,
  className,
}: {
  title: ReactNode
  description?: ReactNode
  onClose: () => void
  size?: 'sm' | 'md' | 'lg' | 'xl'
  footer?: ReactNode
  children: ReactNode
  /** False while a destructive action is in flight, so it cannot be abandoned half-way. */
  dismissible?: boolean
  className?: string
}) {
  const dialog = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const descriptionId = useId()
  const close = useRef(onClose)
  close.current = onClose
  const canDismiss = useRef(dismissible)
  canDismiss.current = dismissible

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    openModals++
    document.body.style.overflow = 'hidden'
    const node = dialog.current
    const first = node?.querySelector<HTMLElement>('[data-autofocus]') ?? node?.querySelector<HTMLElement>(FOCUSABLE)
    ;(first ?? node)?.focus()
    return () => {
      openModals--
      if (!openModals) document.body.style.overflow = ''
      opener?.focus?.()
    }
  }, [])

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      if (canDismiss.current) close.current()
      return
    }
    if (e.key !== 'Tab' || !dialog.current) return
    const items = [...dialog.current.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.offsetParent !== null)
    if (!items.length) return
    const [first, last] = [items[0], items[items.length - 1]]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div
      className="bk-modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && canDismiss.current) close.current()
      }}
    >
      <div
        ref={dialog}
        className={cx('bk-modal', `bk-modal-${size}`, className)}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <header className="bk-modal-header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description && <p id={descriptionId}>{description}</p>}
          </div>
          <IconButton label="Close dialog" onClick={() => canDismiss.current && close.current()} disabled={!dismissible}>
            <X size={16} aria-hidden="true" />
          </IconButton>
        </header>
        <div className="bk-modal-body">{children}</div>
        {footer && <footer className="bk-modal-footer">{footer}</footer>}
      </div>
    </div>,
    document.body,
  )
}

/*
 * Confirmation for consequential actions. The confirm handler may be async;
 * while it runs the dialog cannot be dismissed, and a rejection is shown in
 * the dialog rather than swallowed.
 */
export function ConfirmDialog({
  title,
  children,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  tone = 'primary',
  confirmText,
  onConfirm,
  onCancel,
}: {
  title: ReactNode
  children?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  tone?: 'primary' | 'danger'
  /** When set, the user must type this exact text before confirming. */
  confirmText?: string
  onConfirm: () => unknown | Promise<unknown>
  onCancel: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [typed, setTyped] = useState('')
  const blocked = confirmText !== undefined && typed !== confirmText
  const run = async () => {
    setBusy(true)
    setError('')
    try {
      await onConfirm()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={title}
      size="sm"
      onClose={onCancel}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button variant={tone === 'danger' ? 'danger' : 'primary'} onClick={run} loading={busy} disabled={blocked} data-autofocus={confirmText ? undefined : true}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="bk-confirm">
        {children}
        {confirmText !== undefined && (
          <label className="bk-confirm-type">
            <span>
              Type <code>{confirmText}</code> to confirm
            </span>
            <input className="bk-input is-mono" value={typed} onChange={(e) => setTyped(e.target.value)} data-autofocus autoComplete="off" />
          </label>
        )}
        {error && (
          <p className="bk-confirm-error" role="alert">
            {error}
          </p>
        )}
      </div>
    </Modal>
  )
}

export type MenuItem =
  | {
      id: string
      label: ReactNode
      icon?: ReactNode
      onSelect: () => void
      tone?: 'danger'
      disabled?: boolean
      hint?: ReactNode
    }
  | 'separator'

/*
 * Action menu. Rendered in a portal with fixed positioning so a row inside
 * an overflow container can still open it fully. Closes on outside press,
 * Escape, Tab, scroll and resize; arrow keys move between items.
 */
export function Menu({
  items,
  label = 'More actions',
  trigger,
  align = 'end',
}: {
  items: MenuItem[]
  label?: string
  trigger?: (props: { onClick: () => void; 'aria-expanded': boolean; 'aria-haspopup': 'menu' }) => ReactNode
  align?: 'start' | 'end'
}) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ top: number; left: number; minWidth: number } | null>(null)
  const anchor = useRef<HTMLSpanElement>(null)
  const menu = useRef<HTMLDivElement>(null)

  const place = useCallback(() => {
    const rect = anchor.current?.getBoundingClientRect()
    if (!rect) return
    const width = menu.current?.offsetWidth ?? 200
    const height = menu.current?.offsetHeight ?? 0
    const left = align === 'end' ? rect.right - width : rect.left
    const below = rect.bottom + 6
    const top = below + height > window.innerHeight - 8 && rect.top - height - 6 > 8 ? rect.top - height - 6 : below
    setPos({ top, left: Math.max(8, Math.min(left, window.innerWidth - width - 8)), minWidth: Math.max(180, rect.width) })
  }, [align])

  useLayoutEffect(() => {
    if (!open) return
    place()
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus()
  }, [open, place])

  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      const t = e.target as Node
      if (!menu.current?.contains(t) && !anchor.current?.contains(t)) setOpen(false)
    }
    const dismiss = () => setOpen(false)
    document.addEventListener('pointerdown', onPointer, true)
    window.addEventListener('resize', dismiss)
    window.addEventListener('scroll', dismiss, true)
    return () => {
      document.removeEventListener('pointerdown', onPointer, true)
      window.removeEventListener('resize', dismiss)
      window.removeEventListener('scroll', dismiss, true)
    }
  }, [open])

  const closeAndReturn = () => {
    setOpen(false)
    anchor.current?.querySelector<HTMLElement>('button')?.focus()
  }

  const onKeyDown = (e: ReactKeyboardEvent) => {
    const list = [...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [])]
    const index = list.indexOf(document.activeElement as HTMLElement)
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const next = e.key === 'ArrowDown' ? (index + 1) % list.length : (index - 1 + list.length) % list.length
      list[next]?.focus()
    } else if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault()
      list[e.key === 'Home' ? 0 : list.length - 1]?.focus()
    } else if (e.key === 'Escape') {
      e.stopPropagation()
      closeAndReturn()
    } else if (e.key === 'Tab') setOpen(false)
  }

  const toggle = () => setOpen((v) => !v)
  return (
    <span ref={anchor} className="bk-menu-anchor" onClick={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
      {trigger ? (
        trigger({ onClick: toggle, 'aria-expanded': open, 'aria-haspopup': 'menu' })
      ) : (
        <IconButton label={label} onClick={toggle} aria-expanded={open} aria-haspopup="menu" active={open}>
          <MoreHorizontal size={16} aria-hidden="true" />
        </IconButton>
      )}
      {open &&
        createPortal(
          <div
            ref={menu}
            role="menu"
            aria-label={label}
            className="bk-menu"
            style={pos ? { top: pos.top, left: pos.left, minWidth: pos.minWidth } : { visibility: 'hidden' }}
            onKeyDown={onKeyDown}
            onClick={(e) => e.stopPropagation()}
          >
            {items.map((item, i) =>
              item === 'separator' ? (
                <hr key={`sep-${i}`} className="bk-menu-separator" />
              ) : (
                <button
                  key={item.id}
                  type="button"
                  role="menuitem"
                  disabled={item.disabled}
                  className={cx('bk-menu-item', item.tone === 'danger' && 'is-danger')}
                  onClick={() => {
                    setOpen(false)
                    item.onSelect()
                  }}
                >
                  {item.icon && <span className="bk-menu-icon">{item.icon}</span>}
                  <span className="bk-menu-label">{item.label}</span>
                  {item.hint && <span className="bk-menu-hint">{item.hint}</span>}
                </button>
              ),
            )}
          </div>,
          document.body,
        )}
    </span>
  )
}

/*
 * Side panel for secondary detail (run detail, settings). Same dismissal
 * rules as Modal, anchored to the right edge.
 */
export function Drawer({
  title,
  description,
  onClose,
  children,
  footer,
  width = 520,
}: {
  title: ReactNode
  description?: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  width?: number
}) {
  const titleId = useId()
  const panel = useRef<HTMLDivElement>(null)
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    panel.current?.focus()
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close.current()
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      opener?.focus?.()
    }
  }, [])
  return createPortal(
    <div className="bk-drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && close.current()}>
      <aside ref={panel} className="bk-drawer" style={{ width: `min(${width}px, 100vw)` }} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}>
        <header className="bk-modal-header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          <IconButton label="Close panel" onClick={() => close.current()}>
            <X size={16} aria-hidden="true" />
          </IconButton>
        </header>
        <div className="bk-drawer-body">{children}</div>
        {footer && <footer className="bk-modal-footer">{footer}</footer>}
      </aside>
    </div>,
    document.body,
  )
}

import {
  cloneElement,
  forwardRef,
  isValidElement,
  useId,
  type InputHTMLAttributes,
  type ReactElement,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react'
import { ChevronDown, Search, X } from 'lucide-react'
import { cx } from './cx'

type ControlA11y = { id?: string; 'aria-describedby'?: string; 'aria-invalid'?: boolean }

/*
 * Field owns the label, hint and error for exactly one control, and wires
 * them to it (id, aria-describedby, aria-invalid) so no caller has to.
 */
export function Field({
  label,
  hint,
  error,
  required,
  children,
  className,
  trailing,
}: {
  label: ReactNode
  hint?: ReactNode
  error?: ReactNode
  required?: boolean
  children: ReactElement<ControlA11y>
  className?: string
  trailing?: ReactNode
}) {
  const auto = useId()
  const id = (isValidElement(children) && children.props.id) || auto
  const hintId = hint ? `${id}-hint` : undefined
  const errorId = error ? `${id}-error` : undefined
  const describedBy = [children.props['aria-describedby'], hintId, errorId].filter(Boolean).join(' ') || undefined
  return (
    <div className={cx('bk-field', error ? 'is-invalid' : false, className)}>
      <div className="bk-field-label">
        <label htmlFor={id}>
          {label}
          {required && (
            <span className="bk-field-required" aria-hidden="true">
              *
            </span>
          )}
        </label>
        {trailing}
      </div>
      {cloneElement(children, { id, 'aria-describedby': describedBy, 'aria-invalid': error ? true : children.props['aria-invalid'] })}
      {hint && !error && (
        <small id={hintId} className="bk-field-hint">
          {hint}
        </small>
      )}
      {error && (
        <small id={errorId} className="bk-field-error" role="alert">
          {error}
        </small>
      )}
    </div>
  )
}

export type InputProps = InputHTMLAttributes<HTMLInputElement> & { mono?: boolean; invalid?: boolean }

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, mono, invalid, ...props },
  ref,
) {
  return (
    <input
      ref={ref}
      className={cx('bk-input', mono && 'is-mono', className)}
      aria-invalid={invalid || props['aria-invalid'] || undefined}
      {...props}
    />
  )
})

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean; invalid?: boolean }

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, mono, invalid, rows = 4, ...props },
  ref,
) {
  return (
    <textarea
      ref={ref}
      rows={rows}
      className={cx('bk-input', 'bk-textarea', mono && 'is-mono', className)}
      aria-invalid={invalid || props['aria-invalid'] || undefined}
      {...props}
    />
  )
})

export type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, invalid, children, ...props },
  ref,
) {
  return (
    <span className={cx('bk-select', className)}>
      <select ref={ref} className="bk-input" aria-invalid={invalid || props['aria-invalid'] || undefined} {...props}>
        {children}
      </select>
      <ChevronDown aria-hidden="true" size={14} />
    </span>
  )
})

export function Checkbox({
  label,
  description,
  className,
  ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & { label: ReactNode; description?: ReactNode }) {
  return (
    <label className={cx('bk-check', props.disabled && 'is-disabled', className)}>
      <input type="checkbox" {...props} />
      <span>
        <span className="bk-check-label">{label}</span>
        {description && <small>{description}</small>}
      </span>
    </label>
  )
}

export function Switch({
  checked,
  onChange,
  label,
  disabled,
  size = 'md',
  className,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  /** Accessible name; rendered visibly only when showLabel is used by the caller. */
  label: string
  disabled?: boolean
  size?: 'sm' | 'md'
  className?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      disabled={disabled}
      className={cx('bk-switch', `bk-switch-${size}`, checked && 'is-on', className)}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!checked)
      }}
    >
      <i aria-hidden="true" />
    </button>
  )
}

export const SearchInput = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value'> & {
    value: string
    onChange: (value: string) => void
    shortcut?: ReactNode
  }
>(function SearchInput({ value, onChange, className, shortcut, placeholder = 'Search', ...props }, ref) {
  return (
    <span className={cx('bk-search', className)}>
      <Search aria-hidden="true" size={15} />
      <input
        ref={ref}
        type="search"
        className="bk-input"
        value={value}
        placeholder={placeholder}
        aria-label={props['aria-label'] ?? placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && value) {
            e.stopPropagation()
            onChange('')
          }
        }}
        {...props}
      />
      {value ? (
        <button type="button" className="bk-search-clear" aria-label="Clear search" onClick={() => onChange('')}>
          <X size={14} aria-hidden="true" />
        </button>
      ) : (
        shortcut
      )}
    </span>
  )
})

export function SegmentedControl<T extends string>({
  options,
  value,
  onChange,
  label,
  size = 'md',
}: {
  options: { value: T; label: ReactNode; count?: number; title?: string }[]
  value: T
  onChange: (value: T) => void
  label: string
  size?: 'sm' | 'md'
}) {
  return (
    <div className={cx('bk-segmented', `bk-segmented-${size}`)} role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          title={o.title}
          className={cx(o.value === value && 'is-active')}
          onClick={() => onChange(o.value)}
        >
          {o.label}
          {o.count !== undefined && <span className="bk-segmented-count">{o.count}</span>}
        </button>
      ))}
    </div>
  )
}

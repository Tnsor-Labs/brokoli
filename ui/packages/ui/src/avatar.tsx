import { cx } from './cx'

/** One or two initials from a name or email, for an avatar with no photo. */
export function initials(name: string) {
  return (
    name
      .split(/[\s._@+-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]?.toUpperCase())
      .join('') || '?'
  )
}

/**
 * A person's avatar: their photo when there is one, otherwise their
 * initials in a circle. `name` drives the initials and the default hover
 * title; pass an email and it uses the part before the @.
 */
export function Avatar({
  name,
  src,
  size = 'md',
  title,
  className,
}: {
  name: string
  src?: string | null
  size?: 'sm' | 'md' | 'lg'
  title?: string
  className?: string
}) {
  return (
    <span className={cx('bk-avatar', `bk-avatar-${size}`, className)} title={title ?? name} aria-hidden="true">
      {src ? <img className="bk-avatar-img" src={src} alt="" /> : initials(name)}
    </span>
  )
}

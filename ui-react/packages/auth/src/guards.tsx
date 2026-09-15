import { useEffect, useState } from 'react'
import { Link, Navigate, Outlet, useLocation } from 'react-router-dom'
import { LogOut, Moon, Sun } from 'lucide-react'
import { onLiveConnection } from '@brokoli/api'
import { BootScreen, IconButton, cx, useTheme } from '@brokoli/ui'
import { LoginPage, ServerUnreachable } from './LoginPage'
import { useSession } from './session'

/** Wraps every route that needs a signed-in user. Remembers where the user was headed. */
export function RequireAuth() {
  const session = useSession()
  const location = useLocation()
  if (session.status === 'booting') return <BootScreen label="Establishing session" />
  if (session.status === 'unreachable') return <ServerUnreachable />
  if (session.status !== 'authenticated')
    return <Navigate to="/login" replace state={{ from: `${location.pathname}${location.search}` }} />
  return <Outlet />
}

export function LoginRoute({ defaultPath }: { defaultPath?: string }) {
  const session = useSession()
  if (session.status === 'booting') return <BootScreen label="Establishing session" />
  if (session.status === 'unreachable') return <ServerUnreachable />
  return <LoginPage defaultPath={defaultPath} />
}

function initials(name: string) {
  return (
    name
      .split(/[\s._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]?.toUpperCase())
      .join('') || '?'
  )
}

/** Sidebar footer: who is signed in, live-update state, theme and sign-out. */
export function SidebarAccount({ collapsed = false, profileHref }: { collapsed?: boolean; profileHref?: string }) {
  const session = useSession()
  const { theme, toggle } = useTheme()
  const [live, setLive] = useState(false)
  useEffect(() => onLiveConnection(setLive), [])
  const user = session.user
  if (!user) return null
  const name = user.display_name || user.username
  const inner = (
    <>
      <span className="bk-account-avatar" aria-hidden="true">
        {initials(name)}
      </span>
      {!collapsed && (
        <span className="bk-account-text">
          <strong>{name}</strong>
          <small>{user.role}</small>
        </span>
      )}
    </>
  )
  return (
    <div className={cx('bk-account', collapsed && 'is-collapsed')}>
      {/* When the application offers a profile page, the identity block links to it. */}
      {profileHref ? (
        <Link className="bk-account-user is-link" to={profileHref} title={`${name} (${user.role})`}>
          {inner}
        </Link>
      ) : (
        <div className="bk-account-user" title={`${name} (${user.role})`}>
          {inner}
        </div>
      )}
      <div className="bk-account-actions">
        <span
          className={cx('bk-account-live', live && 'is-live')}
          role="status"
          title={live ? 'Live updates connected' : 'Live updates reconnecting; pages still load normally'}
        >
          <i aria-hidden="true" />
          {!collapsed && (live ? 'Live' : 'Offline')}
        </span>
        <IconButton size="sm" label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={toggle}>
          {theme === 'dark' ? <Sun size={15} aria-hidden="true" /> : <Moon size={15} aria-hidden="true" />}
        </IconButton>
        <IconButton size="sm" label="Sign out" onClick={() => void session.logout()}>
          <LogOut size={15} aria-hidden="true" />
        </IconButton>
      </div>
    </div>
  )
}

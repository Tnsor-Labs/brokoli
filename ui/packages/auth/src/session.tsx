import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  ApiError,
  authApi,
  closeLiveClient,
  rememberWorkspaceHint,
  resolveWorkspace,
  setLiveSessionLostHandler,
  setToken,
  setUnauthorizedHandler,
  storedToken,
  type AuthUser,
  type Workspace,
} from '@brokoli/api'
import { takeReturn } from './returnTo'

/*
 * Session state machine.
 *
 *   booting -> unreachable   the server did not answer the setup probe
 *           -> setup         no users exist yet (first run)
 *           -> anonymous     server fine, nobody signed in
 *           -> authenticated
 *
 * The Svelte UI treated any failure of the setup probe as "open mode" and
 * then showed the login form anyway, so a database outage looked like a
 * password problem. Here an unreachable server is its own state with its own
 * screen and a retry.
 */
export type SessionStatus = 'booting' | 'unreachable' | 'setup' | 'anonymous' | 'authenticated'

type SessionState = {
  status: SessionStatus
  user: AuthUser | null
  /** null means "could not be loaded"; the server stays authoritative either way. */
  permissions: string[] | null
  workspaces: Workspace[]
  error: string
  /** Set when the last session ended because the server rejected it. */
  expired: boolean
}

type SessionApi = SessionState & {
  login: (username: string, password: string) => Promise<void>
  setup: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  retry: () => void
  /**
   * Whether the signed-in user holds a permission. When permissions could
   * not be loaded this returns true (the server still refuses on its side)
   * and a warning is logged, so a failed permissions call never hides the
   * whole product.
   */
  can: (permission: string) => boolean
}

const SessionContext = createContext<SessionApi | null>(null)

const initial: SessionState = {
  status: 'booting',
  user: null,
  permissions: null,
  workspaces: [],
  error: '',
  expired: false,
}

type Callback = { token: string; workspaceHint: string | null; isNewAccount: boolean }

/** Reads and strips an SSO callback token from the URL (hash form first, legacy query form second). */
function takeCallback(): Callback | null {
  const hash = window.location.hash
  const query = new URLSearchParams(window.location.search)
  let params: URLSearchParams | null = null
  if (hash.includes('/auth-callback')) params = new URLSearchParams(hash.split('?')[1] ?? '')
  else if (query.get('token')) params = query
  const token = params?.get('token')
  if (!params || !token) return null
  // Back to the route the person was opening before the identity provider, if any (device approval, an invite).
  history.replaceState(null, '', `${window.location.pathname}#${takeReturn() ?? '/'}`)
  return { token, workspaceHint: params.get('ws'), isNewAccount: params.get('new') === '1' }
}

function describe(error: unknown) {
  if (error instanceof ApiError) return error.message
  return error instanceof Error ? error.message : String(error)
}

async function loadPermissions(): Promise<string[] | null> {
  try {
    return await authApi.permissions()
  } catch (e) {
    console.warn(`[brokoli-ui] permissions unavailable, controls will not be hidden: ${describe(e)}`)
    return null
  }
}

export function SessionProvider({ children, onSignedOut }: { children: ReactNode; onSignedOut?: () => void }) {
  const [state, setState] = useState<SessionState>(initial)
  const [attempt, setAttempt] = useState(0)
  const signedOut = useRef(onSignedOut)
  signedOut.current = onSignedOut
  const warned = useRef(false)

  const endSession = useCallback((expired: boolean) => {
    closeLiveClient()
    signedOut.current?.()
    setState({ ...initial, status: 'anonymous', expired })
  }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => endSession(true))
    setLiveSessionLostHandler(() => endSession(true))
    return () => {
      setUnauthorizedHandler(() => {})
      setLiveSessionLostHandler(() => {})
    }
  }, [endSession])

  useEffect(() => {
    let active = true
    const settle = (next: Partial<SessionState>) => active && setState((s) => ({ ...s, ...next }))
    void (async () => {
      setState(initial)
      const callback = takeCallback()
      if (callback) {
        setToken(callback.token, true)
        if (callback.workspaceHint) rememberWorkspaceHint(callback.workspaceHint)
      }
      try {
        const { needs_setup } = await authApi.setup()
        if (needs_setup) return settle({ status: 'setup' })
      } catch (e) {
        return settle({ status: 'unreachable', error: describe(e) })
      }

      let user: AuthUser | null = null
      let usedBearer = Boolean(callback)
      try {
        user = await authApi.me(callback ? {} : { cookieOnly: true })
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 401) return settle({ status: 'unreachable', error: describe(e) })
        const legacy = !callback && storedToken()
        if (legacy) {
          setToken(legacy)
          usedBearer = true
          user = await authApi.me().catch(() => null)
        }
      }
      if (!user) {
        setToken(null)
        return settle({ status: 'anonymous' })
      }

      if (usedBearer) {
        // Convert the bearer into the httpOnly cookie the websocket needs; once that works the bearer is dropped.
        try {
          await authApi.session()
          setToken(null)
        } catch {
          /* Keep the in-memory bearer for this tab. */
        }
      } else if (storedToken()) setToken(null)

      let workspaces: Workspace[] = []
      try {
        workspaces = await resolveWorkspace(user.id, Boolean(callback?.workspaceHint))
      } catch (e) {
        return settle({ status: 'unreachable', error: `Could not load your workspaces: ${describe(e)}` })
      }
      const permissions = await loadPermissions()
      settle({ status: 'authenticated', user, permissions, workspaces, error: '', expired: false })
    })()
    return () => {
      active = false
    }
  }, [attempt])

  const establish = useCallback(async (user: AuthUser | null) => {
    if (!user) throw new Error('The server accepted the credentials but did not establish a session.')
    const workspaces = await resolveWorkspace(user.id, false)
    const permissions = await loadPermissions()
    setState({ status: 'authenticated', user, permissions, workspaces, error: '', expired: false })
  }, [])

  const api = useMemo<SessionApi>(
    () => ({
      ...state,
      login: async (username, password) => establish(await authApi.login(username, password)),
      setup: async (username, password) => establish(await authApi.createFirstUser(username, password)),
      logout: async () => {
        await authApi.logout()
        endSession(false)
      },
      retry: () => setAttempt((n) => n + 1),
      can: (permission) => {
        if (state.permissions === null) {
          if (!warned.current && state.status === 'authenticated') {
            warned.current = true
            console.warn('[brokoli-ui] permission checks are open because permissions failed to load')
          }
          return true
        }
        return state.permissions.includes(permission)
      },
    }),
    [state, establish, endSession],
  )

  return <SessionContext.Provider value={api}>{children}</SessionContext.Provider>
}

export function useSession(): SessionApi {
  const value = useContext(SessionContext)
  if (!value) throw new Error('useSession must be used within SessionProvider')
  return value
}

/** Password policy enforced by the server for new accounts (api/users.go). */
export function passwordProblems(password: string) {
  return {
    length: password.length >= 10,
    upper: /[A-Z]/.test(password),
    lower: /[a-z]/.test(password),
    digit: /\d/.test(password),
  }
}

import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Check, Eye, EyeOff, KeyRound, Moon, RefreshCw, Sun, X } from 'lucide-react'
import { authApi, type AuthMethods } from '@brokoli/api'
import { Brand, Button, Callout, Eyebrow, Field, IconButton, Input, Spinner, errorMessage, useTheme } from '@brokoli/ui'
import { rememberReturn } from './returnTo'
import { passwordProblems, useSession } from './session'
import './login.css'

export const PROVIDER_LABEL: Record<string, string> = {
  github: 'GitHub',
  google: 'Google',
  keycloak: 'single sign-on',
}

/** Human-friendly provider name for a button caption. */
export function providerLabel(id: string) {
  return PROVIDER_LABEL[id] ?? id.charAt(0).toUpperCase() + id.slice(1)
}

export function ProviderMark({ id }: { id: string }) {
  if (id === 'github')
    return (
      <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
        <path
          fill="currentColor"
          d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z"
        />
      </svg>
    )
  if (id === 'google')
    return (
      <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
        <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4" />
        <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853" />
        <path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05" />
        <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335" />
      </svg>
    )
  return <KeyRound size={17} aria-hidden="true" />
}

/*
 * The only error text the login page takes from the URL is the identity
 * provider's ?error= value. It is framed as coming from the provider and
 * truncated, so a crafted link cannot pose as a Brokoli message.
 */
function takeOAuthError() {
  const value = new URLSearchParams(window.location.search).get('error')
  if (!value) return ''
  history.replaceState(null, '', `${window.location.pathname}#/login`)
  return `Sign-in with your identity provider failed. It reported: "${value.slice(0, 200)}"`
}

export function LoginPage({ defaultPath = '/pipelines' }: { defaultPath?: string }) {
  const session = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const { theme, toggle } = useTheme()
  const isSetup = session.status === 'setup'
  const from = (location.state as { from?: string } | null)?.from
  const [methods, setMethods] = useState<AuthMethods | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [reveal, setReveal] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(takeOAuthError)
  const policy = useMemo(() => passwordProblems(password), [password])
  const policyMet = Object.values(policy).every(Boolean)

  useEffect(() => {
    if (isSetup) return
    authApi
      .methods()
      .then(setMethods)
      .catch((e) => {
        // Without methods we still offer the password form, the one method core always has.
        console.warn(`[brokoli-ui] could not load sign-in methods: ${errorMessage(e)}`)
        setMethods({ password: true, oauth: [] })
      })
  }, [isSetup])

  useEffect(() => {
    if (session.status === 'authenticated') navigate(from && from !== '/login' ? from : defaultPath, { replace: true })
  }, [session.status, from, defaultPath, navigate])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    if (!username.trim() || !password) return setError('Enter your username and password.')
    if (isSetup && !policyMet) return setError('The password does not meet the requirements below.')
    if (isSetup && password !== confirm) return setError('The two passwords do not match.')
    setBusy(true)
    setError('')
    try {
      await (isSetup ? session.setup(username.trim(), password) : session.login(username.trim(), password))
    } catch (err) {
      setError(errorMessage(err) || 'Sign-in failed.')
      setBusy(false)
    }
  }

  const providers = isSetup ? [] : (methods?.oauth ?? [])
  const passwordEnabled = isSetup || methods?.password !== false

  return (
    <main className="bk-login">
      <section className="bk-login-panel">
        <header className="bk-login-top">
          <Brand />
          <IconButton label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={toggle}>
            {theme === 'dark' ? <Sun size={16} aria-hidden="true" /> : <Moon size={16} aria-hidden="true" />}
          </IconButton>
        </header>

        <div className="bk-login-card">
          <div className="bk-login-heading">
            <Eyebrow>{isSetup ? 'First run' : 'Workspace access'}</Eyebrow>
            <h1>{isSetup ? 'Create the administrator account' : 'Sign in to Brokoli'}</h1>
            <p>
              {isSetup
                ? 'This account manages the workspace, its users and its connections. You can add more people afterwards.'
                : 'Build, schedule and observe your data pipelines.'}
            </p>
          </div>

          {session.expired && !error && (
            <Callout tone="warning" title="Your session ended">
              Sign in again to continue where you left off.
            </Callout>
          )}
          {error && (
            <Callout tone="danger" onDismiss={() => setError('')}>
              {error}
            </Callout>
          )}

          {!isSetup && !methods ? (
            <div className="bk-login-loading">
              <Spinner /> Checking sign-in options
            </div>
          ) : (
            <>
              {providers.length > 0 && (
                <div className="bk-login-providers">
                  {providers.map((id) => (
                    <a
                      key={id}
                      className="bk-login-provider"
                      onClick={() => rememberReturn(from)}
                      href={`/api/auth/oauth/${encodeURIComponent(id)}?redirect_uri=${encodeURIComponent(window.location.origin)}`}
                    >
                      <ProviderMark id={id} />
                      Continue with {providerLabel(id)}
                    </a>
                  ))}
                </div>
              )}
              {providers.length > 0 && passwordEnabled && (
                <div className="bk-login-divider">
                  <span>or use your Brokoli credentials</span>
                </div>
              )}
              {passwordEnabled ? (
                <form className="bk-login-form" onSubmit={submit} noValidate>
                  <Field label="Username">
                    <Input
                      name="username"
                      autoComplete="username"
                      autoFocus
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      placeholder={isSetup ? 'admin' : 'your.username'}
                      className="bk-login-input"
                    />
                  </Field>
                  <Field
                    label="Password"
                    trailing={
                      <button type="button" className="bk-login-reveal" onClick={() => setReveal((v) => !v)} aria-pressed={reveal}>
                        {reveal ? <EyeOff size={14} aria-hidden="true" /> : <Eye size={14} aria-hidden="true" />}
                        {reveal ? 'Hide' : 'Show'}
                      </button>
                    }
                  >
                    <Input
                      name="password"
                      type={reveal ? 'text' : 'password'}
                      autoComplete={isSetup ? 'new-password' : 'current-password'}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      className="bk-login-input"
                    />
                  </Field>
                  {isSetup && (
                    <>
                      <ul className="bk-login-policy" aria-label="Password requirements">
                        {(
                          [
                            ['length', 'At least 10 characters'],
                            ['upper', 'An uppercase letter'],
                            ['lower', 'A lowercase letter'],
                            ['digit', 'A number'],
                          ] as const
                        ).map(([key, label]) => (
                          <li key={key} className={policy[key] ? 'is-met' : ''}>
                            {policy[key] ? <Check size={13} aria-hidden="true" /> : <X size={13} aria-hidden="true" />}
                            {label}
                            <span className="bk-sr-only">{policy[key] ? '(met)' : '(not met)'}</span>
                          </li>
                        ))}
                      </ul>
                      <Field label="Confirm password" error={confirm && confirm !== password ? 'Passwords do not match' : undefined}>
                        <Input
                          name="confirm"
                          type={reveal ? 'text' : 'password'}
                          autoComplete="new-password"
                          value={confirm}
                          onChange={(e) => setConfirm(e.target.value)}
                          className="bk-login-input"
                        />
                      </Field>
                    </>
                  )}
                  <Button type="submit" variant="primary" size="lg" loading={busy} className="bk-login-submit">
                    {busy ? (isSetup ? 'Creating account' : 'Signing in') : isSetup ? 'Create account and continue' : 'Sign in'}
                  </Button>
                </form>
              ) : (
                <Callout tone="info" title="Password sign-in is turned off">
                  This workspace only accepts the identity providers above.
                </Callout>
              )}
            </>
          )}
        </div>

        <footer className="bk-login-foot">
          <i aria-hidden="true" />
          Connected to {window.location.host}
        </footer>
      </section>

      <aside className="bk-login-aside" aria-hidden="true">
        <div className="bk-login-grid" />
        <div className="bk-login-aside-inner">
          <p className="bk-login-display">
            Build data workflows.
            <span>Run them on your terms.</span>
          </p>
          <div className="bk-login-diagram">
            <svg className="bk-login-links" viewBox="0 0 520 220" preserveAspectRatio="none">
              <path d="M150 58 C 205 58, 205 110, 262 110" />
              <path d="M150 162 C 205 162, 205 110, 262 110" />
              <path d="M412 110 L 460 110" />
            </svg>
            <div className="bk-login-node" style={{ left: 0, top: 30 }}>
              <b>SRC</b>
              <span>
                orders_api
                <small>api source</small>
              </span>
            </div>
            <div className="bk-login-node" style={{ left: 0, top: 134 }}>
              <b>DB</b>
              <span>
                customers
                <small>database source</small>
              </span>
            </div>
            <div className="bk-login-node is-active" style={{ left: 262, top: 82 }}>
              <b>JN</b>
              <span>
                enrich_orders
                <small>join</small>
              </span>
            </div>
          </div>
          <p className="bk-login-proof">
            <span>Open-source core</span>
            <span>Self-hosted</span>
            <span>Your data stays yours</span>
          </p>
        </div>
      </aside>
    </main>
  )
}

/** Shown when the server did not answer the setup probe, instead of a login form that cannot work. */
export function ServerUnreachable() {
  const session = useSession()
  return (
    <main className="bk-login bk-login-single">
      <section className="bk-login-panel">
        <header className="bk-login-top">
          <Brand />
        </header>
        <div className="bk-login-card">
          <div className="bk-login-heading">
            <Eyebrow>Connection</Eyebrow>
            <h1>Brokoli is not responding</h1>
            <p>The interface loaded, but the server at {window.location.host} did not answer. Nothing in your workspace has changed.</p>
          </div>
          <Callout tone="danger" title="What the browser saw">
            {session.error || 'No response.'}
          </Callout>
          <Button variant="primary" size="lg" icon={<RefreshCw size={16} aria-hidden="true" />} onClick={session.retry}>
            Try again
          </Button>
        </div>
      </section>
    </main>
  )
}

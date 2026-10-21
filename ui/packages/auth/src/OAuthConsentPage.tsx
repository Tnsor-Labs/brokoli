import { useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { Moon, Sun } from 'lucide-react'
import { oauthApi } from '@brokoli/api'
import {
  Brand,
  Button,
  Callout,
  Eyebrow,
  IconButton,
  Skeleton,
  errorMessage,
  useTheme,
} from '@brokoli/ui'
import { useSession } from './session'
import './login.css'

/*
 * Connecting an MCP client with OAuth (ADR-046 phase 5): a client such as a
 * chat assistant's connector sent the person here to approve it. Approving
 * lets it act as them, with their permissions, until they revoke it; the
 * browser then goes back to the client with a one-time code.
 */
export function OAuthConsentPage() {
  const session = useSession()
  const { search } = useLocation()
  const { theme, toggle } = useTheme()
  const [info, setInfo] = useState<{ client_name: string; redirect_host: string } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState<null | 'approve' | 'deny'>(null)

  useEffect(() => {
    oauthApi
      .request(search)
      .then(setInfo)
      .catch((e) => setError(errorMessage(e)))
  }, [search])

  const decide = async (approve: boolean) => {
    setBusy(approve ? 'approve' : 'deny')
    try {
      const res = await oauthApi.decide(search, approve)
      window.location.assign(res.redirect)
    } catch (e) {
      setError(errorMessage(e))
      setBusy(null)
    }
  }

  const who = session.user?.display_name || session.user?.username || 'you'
  return (
    <main className="bk-login bk-login-single">
      <section className="bk-login-panel">
        <header className="bk-login-top">
          <Brand />
          <IconButton
            label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
            onClick={toggle}
          >
            {theme === 'dark' ? (
              <Sun size={16} aria-hidden="true" />
            ) : (
              <Moon size={16} aria-hidden="true" />
            )}
          </IconButton>
        </header>
        <div className="bk-login-card">
          <div className="bk-login-heading">
            <Eyebrow>Connect an application</Eyebrow>
            {info ? (
              <h1>Connect {info.client_name} to Brokoli?</h1>
            ) : (
              <h1>Connect an application to Brokoli?</h1>
            )}
          </div>
          {error ? (
            <Callout tone="danger" title="This connection cannot be approved">
              {error}
            </Callout>
          ) : !info ? (
            <Skeleton height={140} />
          ) : (
            <>
              <p>
                <strong>{info.client_name}</strong> will act as <strong>{who}</strong> in Brokoli,
                with your permissions: it can see the pipelines, runs and lineage you can, and do
                what your role allows, such as running pipelines and proposing changes. What it does
                is recorded under your name.
              </p>
              <p className="bk-muted">
                Approving sends you back to <span className="bk-mono">{info.redirect_host}</span>.
                The connection lasts until you revoke it; its access is renewed every hour while it
                is in use.
              </p>
              <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
                <Button
                  variant="primary"
                  loading={busy === 'approve'}
                  disabled={busy !== null}
                  onClick={() => void decide(true)}
                >
                  Approve
                </Button>
                <Button
                  loading={busy === 'deny'}
                  disabled={busy !== null}
                  onClick={() => void decide(false)}
                >
                  Deny
                </Button>
              </div>
            </>
          )}
        </div>
      </section>
    </main>
  )
}

import { useState, type FormEvent } from 'react'
import { userApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Button, Callout, Field, Input, errorMessage, useToast } from '@brokoli/ui'
import { PasswordField, passwordOk } from './PasswordField'

export function AccountTab() {
  const session = useSession()
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const mismatch = Boolean(confirm) && confirm !== next
  const ready = Boolean(current) && passwordOk(next) && confirm === next

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setError('')
    try {
      await userApi.changePassword(current, next)
      toast.success('Password changed')
      setCurrent('')
      setNext('')
      setConfirm('')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="ws-tab">
      <section className="ws-card">
        <h3>Your account</h3>
        <dl className="ws-facts">
          <dt>Username</dt>
          <dd>{session.user?.username}</dd>
          {session.user?.display_name && (
            <>
              <dt>Name</dt>
              <dd>{session.user.display_name}</dd>
            </>
          )}
          {session.user?.email && (
            <>
              <dt>Email</dt>
              <dd>{session.user.email}</dd>
            </>
          )}
          <dt>Role</dt>
          <dd>{session.user?.role}</dd>
        </dl>
      </section>
      <section className="ws-card">
        <h3>Change your password</h3>
        <form className="ws-form" onSubmit={submit}>
          <Field label="Current password">
            <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
          </Field>
          <PasswordField label="New password" value={next} onChange={setNext} />
          <Field label="Repeat the new password" error={mismatch ? 'The passwords do not match' : undefined}>
            <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          <p className="ws-muted ws-small">Other places where you are signed in stay signed in until their session expires, up to 24 hours.</p>
          {error && <Callout tone="danger" title="Password not changed">{error}</Callout>}
          <div>
            <Button type="submit" variant="primary" loading={busy} disabled={!ready}>
              Change password
            </Button>
          </div>
        </form>
      </section>
    </div>
  )
}

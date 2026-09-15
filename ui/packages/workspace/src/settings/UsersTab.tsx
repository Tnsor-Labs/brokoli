import { useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, UserPlus } from 'lucide-react'
import { userApi, type User } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, Field, Input, Modal, Select, Skeleton, errorMessage, formatDateTime, useToast } from '@brokoli/ui'
import { PasswordField, passwordOk } from './PasswordField'

const ROLES = [
  { value: 'viewer', label: 'Viewer', help: 'Reads everything, changes nothing' },
  { value: 'editor', label: 'Editor', help: 'Builds, edits and runs pipelines, connections and variables' },
  { value: 'admin', label: 'Administrator', help: 'Everything, including users, plugins and server settings' },
]

function ResetPassword({ user, onClose }: { user: User; onClose: () => void }) {
  const toast = useToast()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!passwordOk(password) || busy) return
    setBusy(true)
    try {
      await userApi.resetPassword(user.id, password)
      toast.success(`Password reset for ${user.username}`, 'Share it with them securely; they can change it after signing in.')
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Reset the password for ${user.username}`}
      description="Sessions they already have stay signed in until they expire, up to 24 hours."
      size="sm"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="ws-reset-form" loading={busy} disabled={!passwordOk(password)}>
            Reset password
          </Button>
        </>
      }
    >
      <form id="ws-reset-form" className="ws-form" onSubmit={submit}>
        <PasswordField label="New password" value={password} onChange={setPassword} autoFocus />
        {error && <Callout tone="danger">{error}</Callout>}
      </form>
    </Modal>
  )
}

export function UsersTab() {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const canManage = session.user?.role === 'admin' || session.user?.role === 'superadmin'
  const users = useQuery({ queryKey: ['users'], queryFn: userApi.list, enabled: canManage })
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('editor')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [resetting, setResetting] = useState<User | null>(null)
  const name = username.trim()

  if (!canManage)
    return (
      <div className="ws-tab">
        <Callout tone="info" title="Only administrators manage users">
          You are signed in as {session.user?.username} ({session.user?.role}). Ask an administrator to add people or reset passwords.
        </Callout>
      </div>
    )

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (!name || !passwordOk(password) || busy) return
    setBusy(true)
    setError('')
    try {
      const created = await userApi.create(name, password, role)
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      toast.success(`Added ${created.username}`, `Role: ${created.role}.`)
      setUsername('')
      setPassword('')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="ws-tab">
      <section className="ws-card">
        <h3>People</h3>
        {users.isPending ? (
          <div className="ws-loading">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} height={20} />
            ))}
          </div>
        ) : users.isError ? (
          <Callout tone="danger" title="Users could not be loaded">
            {errorMessage(users.error)}
          </Callout>
        ) : (
          <div className="bk-table-wrap">
            <table className="bk-table">
              <thead>
                <tr>
                  <th scope="col">User</th>
                  <th scope="col">Role</th>
                  <th scope="col">Added</th>
                  <th scope="col">
                    <span className="bk-sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {users.data.map((u) => {
                  const self = u.username === session.user?.username
                  return (
                    <tr key={u.id}>
                      <td>
                        <strong>{u.display_name || u.username}</strong>
                        {u.display_name && <span className="ws-muted bk-mono"> {u.username}</span>}
                        {u.email && <small className="ws-block ws-muted">{u.email}</small>}
                        {self && <Badge tone="accent">You</Badge>}
                      </td>
                      <td>
                        <Badge tone={u.role === 'admin' || u.role === 'superadmin' ? 'accent' : 'neutral'}>{u.role}</Badge>
                      </td>
                      <td>{formatDateTime(u.created_at)}</td>
                      <td>
                        <div className="ws-actions">
                          {!self && (
                            <Button size="sm" icon={<KeyRound size={14} aria-hidden="true" />} onClick={() => setResetting(u)}>
                              Reset password
                            </Button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        <p className="ws-muted ws-small">This server version cannot remove people or change a role after the account is created.</p>
      </section>

      <section className="ws-card">
        <h3>Add a person</h3>
        <form className="ws-form" onSubmit={create}>
          <Field label="Username">
            <Input value={username} autoComplete="off" onChange={(e) => setUsername(e.target.value)} />
          </Field>
          <PasswordField label="Initial password" value={password} onChange={setPassword} />
          <Field label="Role" hint={ROLES.find((r) => r.value === role)?.help}>
            <Select value={role} onChange={(e) => setRole(e.target.value)}>
              {ROLES.map((r) => (
                <option key={r.value} value={r.value}>
                  {r.label}
                </option>
              ))}
            </Select>
          </Field>
          {error && <Callout tone="danger" title="Not added">{error}</Callout>}
          <div>
            <Button type="submit" variant="primary" icon={<UserPlus size={15} aria-hidden="true" />} loading={busy} disabled={!name || !passwordOk(password)}>
              Add person
            </Button>
          </div>
        </form>
      </section>
      {resetting && <ResetPassword user={resetting} onClose={() => setResetting(null)} />}
    </div>
  )
}

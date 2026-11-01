import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bot, Plus } from 'lucide-react'
import { agentsApi, type Agent } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  AgentAvatar,
  Badge,
  Button,
  Callout,
  EmptyState,
  Field,
  Input,
  Modal,
  Page,
  PageHeader,
  Select,
  Skeleton,
  errorMessage,
  formatRelative,
} from '@brokoli/ui'
import { AGENT_NAME, AGENT_ROLES, roleLabel } from './model'
import '../workspace.css'
import './agents.css'

/*
 * Agents (ADR-046): accounts for AI agents, each with an owner who answers
 * for it, a role, and tokens of its own. An agent never has more than its
 * owner may do, and what it does is recorded as the agent. Each agent's
 * page holds its tokens and its triggers (ADR-047).
 */

export const AGENTS_KEY = ['agents'] as const

export function AgentsPage() {
  const session = useSession()
  const navigate = useNavigate()
  const agents = useQuery({ queryKey: AGENTS_KEY, queryFn: agentsApi.list })
  const [creating, setCreating] = useState(false)
  const canCreate = session.can('settings.manage_users') && agents.data !== null
  const list = agents.data ?? []

  return (
    <Page>
      <PageHeader
        title="Agents"
        description="Accounts for AI agents. Give an agent its own token instead of your sign-in: it never has more permissions than its owner, what it does is recorded as the agent, and triggers can tell it when a run fails."
        actions={
          canCreate && (
            <Button
              variant="primary"
              icon={<Plus size={15} aria-hidden="true" />}
              onClick={() => setCreating(true)}
            >
              New agent
            </Button>
          )
        }
      />
      {agents.isPending ? (
        <Skeleton height={160} />
      ) : agents.isError ? (
        <Callout
          tone="danger"
          title="Agents could not be loaded"
          action={
            <Button size="sm" onClick={() => void agents.refetch()}>
              Try again
            </Button>
          }
        >
          {errorMessage(agents.error)}
        </Callout>
      ) : agents.data === null ? (
        <Callout tone="info" title="Agents are not available here">
          This server does not offer agent identities.
        </Callout>
      ) : !list.length ? (
        <div className="ws-empty">
          <EmptyState
            icon={<Bot size={20} aria-hidden="true" />}
            title="No agents yet"
            action={
              canCreate && (
                <Button
                  variant="primary"
                  icon={<Plus size={15} aria-hidden="true" />}
                  onClick={() => setCreating(true)}
                >
                  Create an agent
                </Button>
              )
            }
          >
            {canCreate
              ? 'Create one for each agent you connect (Claude Code, an IDE, a hosted agent), then issue it a token.'
              : 'You own no agents. Someone who manages users can create one for you.'}
          </EmptyState>
        </div>
      ) : (
        <div className="bk-table-wrap">
          <table className="bk-table ag-table">
            <thead>
              <tr>
                <th scope="col">Agent</th>
                <th scope="col">Role</th>
                <th scope="col">Owner</th>
                <th scope="col">Last used</th>
              </tr>
            </thead>
            <tbody>
              {list.map((a) => (
                <tr
                  key={a.id}
                  className={a.disabled ? 'is-clickable is-disabled' : 'is-clickable'}
                  onClick={() => navigate(`/agents/${encodeURIComponent(a.id)}`)}
                >
                  <td>
                    <div className="ws-name">
                      <AgentAvatar avatar={a.avatar} size={32} />
                      <span>
                        <Link
                          to={`/agents/${encodeURIComponent(a.id)}`}
                          className="ag-name"
                          onClick={(e) => e.stopPropagation()}
                        >
                          {a.name}
                        </Link>
                        {a.description && <small>{a.description}</small>}
                      </span>
                    </div>
                  </td>
                  <td>{a.disabled ? <Badge tone="neutral">Disabled</Badge> : roleLabel(a.role)}</td>
                  <td>{a.owner_name ?? a.owner_id}</td>
                  <td>{a.last_used_at ? formatRelative(a.last_used_at) : 'never'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <CreateAgent
          onClose={() => setCreating(false)}
          onCreated={(a) => navigate(`/agents/${encodeURIComponent(a.id)}`)}
        />
      )}
    </Page>
  )
}

function CreateAgent({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (a: Agent) => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [role, setRole] = useState('agent')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = AGENT_NAME.test(name)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!valid || busy) return
    setBusy(true)
    setError('')
    try {
      const a = await agentsApi.create({ name, role, description: description.trim() })
      await queryClient.invalidateQueries({ queryKey: AGENTS_KEY })
      onClose()
      onCreated(a)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  return (
    <Modal
      title="New agent"
      description="You are its owner: it never has more permissions than you."
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="ag-create" loading={busy} disabled={!valid}>
            Create agent
          </Button>
        </>
      }
    >
      <form id="ag-create" className="ws-form" onSubmit={submit} noValidate>
        <Field
          label="Name"
          required
          hint="Lowercase letters, digits and dashes, such as triage-bot"
        >
          <Input
            autoFocus
            autoComplete="off"
            value={name}
            onChange={(e) => setName(e.target.value.toLowerCase())}
          />
        </Field>
        <Field label="What it is for">
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Investigates failed runs"
          />
        </Field>
        <Field label="Role" hint={AGENT_ROLES.find((r) => r.value === role)?.help}>
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            {AGENT_ROLES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </Field>
        {error && (
          <Callout tone="danger" title="Not created">
            {error}
          </Callout>
        )}
      </form>
    </Modal>
  )
}

import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Copy } from 'lucide-react'
import { agentsApi, currentWorkspace, messagesApi, type Agent } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  AgentAvatar,
  Badge,
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Field,
  IconButton,
  Input,
  Page,
  PageHeader,
  Select,
  Skeleton,
  errorMessage,
  formatDateTime,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import { AGENTS_KEY } from './AgentsPage'
import { AgentAvatarPicker } from './AgentAvatarPicker'
import { AgentTriggers } from './AgentTriggers'
import { Conversation } from './Conversation'
import { connectCommand, roleLabel, tokenState } from './model'
import '../workspace.css'
import './agents.css'

/** One agent: who answers for it, its tokens, and its triggers. */
export function AgentPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const agent = useQuery({ queryKey: [...AGENTS_KEY, id], queryFn: () => agentsApi.get(id) })
  // What the agent said this week, and how much of it people muted.
  const said = useQuery({
    queryKey: [...AGENTS_KEY, id, 'messages'],
    queryFn: () => messagesApi.summary(id),
    retry: false,
  })
  const session = useSession()
  const [disabling, setDisabling] = useState(false)

  const back = (
    <Link to="/agents" className="ag-back">
      <ArrowLeft size={14} aria-hidden="true" /> Agents
    </Link>
  )
  if (agent.isPending)
    return (
      <Page>
        {back}
        <Skeleton height={200} />
      </Page>
    )
  if (agent.isError)
    return (
      <Page>
        {back}
        <Callout tone="danger" title="This agent could not be loaded">
          {errorMessage(agent.error)}
        </Callout>
      </Page>
    )
  const a = agent.data
  // Its owner picks the agent's avatar, as may whoever manages users.
  const mayDress =
    !a.disabled && (session.user?.id === a.owner_id || session.can('settings.manage_users'))
  return (
    <Page>
      {back}
      <PageHeader
        title={
          <span className="ag-title">
            <AgentAvatar avatar={a.avatar} size={52} />
            <span className="bk-mono">{a.name}</span>
          </span>
        }
        description={a.description || undefined}
        meta={
          <div className="ag-meta">
            {a.disabled ? (
              <Badge tone="neutral">Disabled</Badge>
            ) : (
              <Badge tone="accent">{roleLabel(a.role)}</Badge>
            )}
            <span>Owned by {a.owner_name ?? a.owner_id}</span>
            <span title={formatDateTime(a.created_at)}>Created {formatRelative(a.created_at)}</span>
            <span>
              {a.last_used_at ? `Last used ${formatRelative(a.last_used_at)}` : 'Never used'}
            </span>
            {said.data && said.data.sent > 0 && (
              <span title="Messages it sent people in the last 7 days">
                {said.data.sent} {said.data.sent === 1 ? 'message' : 'messages'} this week
                {said.data.muted > 0 && `, ${said.data.muted} to people who muted it`}
              </span>
            )}
          </div>
        }
        actions={
          !a.disabled && (
            <Button variant="ghost" onClick={() => setDisabling(true)}>
              Disable
            </Button>
          )
        }
      />
      {a.disabled && (
        <Callout tone="info" title="This agent is disabled">
          Its tokens no longer work and its triggers deliver nothing. What it did stays recorded as
          it.
        </Callout>
      )}
      {mayDress && (
        <AgentAvatarPicker
          agent={a}
          onChanged={() => queryClient.invalidateQueries({ queryKey: AGENTS_KEY })}
        />
      )}
      {mayDress && <TalkTo agent={a} />}
      <AgentTriggers agent={a} />
      {!a.disabled && <Tokens agent={a} />}
      {disabling && (
        <ConfirmDialog
          title={`Disable ${a.name}?`}
          confirmLabel="Disable"
          tone="danger"
          onCancel={() => setDisabling(false)}
          onConfirm={async () => {
            try {
              await agentsApi.disable(a.id)
              await queryClient.invalidateQueries({ queryKey: AGENTS_KEY })
              toast.success(
                `${a.name} is disabled`,
                'Its tokens no longer work. What it did stays recorded as it.',
              )
              navigate('/agents')
            } catch (e) {
              toast.error('Not disabled', e)
            }
            setDisabling(false)
          }}
        >
          Every token it has stops working at once, and its triggers stop. It cannot be enabled
          again; create a new agent instead.
        </ConfirmDialog>
      )}
    </Page>
  )
}

function Tokens({ agent }: { agent: Agent }) {
  const session = useSession()
  const isOwner = session.user?.id === agent.owner_id
  const toast = useToast()
  const queryClient = useQueryClient()
  const key = [...AGENTS_KEY, agent.id, 'tokens']
  const tokens = useQuery({ queryKey: key, queryFn: () => agentsApi.tokens(agent.id) })
  const [name, setName] = useState('')
  const [days, setDays] = useState('90')
  const [onBehalf, setOnBehalf] = useState(false)
  const [busy, setBusy] = useState(false)
  const [issued, setIssued] = useState<string | null>(null)

  const issue = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim() || busy) return
    setBusy(true)
    try {
      const res = await agentsApi.createToken(agent.id, {
        name: name.trim(),
        expires_in_days: Number(days),
        on_behalf_of_owner: isOwner && onBehalf,
      })
      setIssued(res.token)
      setName('')
      await queryClient.invalidateQueries({ queryKey: key })
    } catch (err) {
      toast.error('No token issued', err)
    }
    setBusy(false)
  }
  const revoke = async (tokenId: string) => {
    try {
      await agentsApi.revokeToken(agent.id, tokenId)
      await queryClient.invalidateQueries({ queryKey: key })
      toast.success('Revoked', 'The agent can no longer use that token.')
    } catch (err) {
      toast.error('Not revoked', err)
    }
  }
  const command = issued
    ? connectCommand(window.location.origin, currentWorkspace() ?? '', issued)
    : ''
  const list = tokens.data ?? []

  return (
    <section className="ag-section" aria-labelledby="ag-tokens">
      <div className="ag-section-head">
        <div>
          <h2 id="ag-tokens">Tokens</h2>
          <p className="ws-muted">
            Each token is shown once. Name it after where it is used, so it can be revoked on its
            own.
          </p>
        </div>
      </div>
      {issued && (
        <div className="ag-once" role="status">
          <strong>Connect a client with it</strong>
          <div className="ag-once-row">
            <code>{command}</code>
            <IconButton
              label="Copy the command"
              onClick={() =>
                navigator.clipboard
                  .writeText(command)
                  .then(() => toast.success('Copied'))
                  .catch((e) => toast.error('Could not copy', e))
              }
            >
              <Copy size={15} aria-hidden="true" />
            </IconButton>
          </div>
          <p className="ws-muted">
            This is the only time the token is shown. Paste it where the agent runs before you
            leave.
          </p>
          <Button size="sm" variant="ghost" onClick={() => setIssued(null)}>
            I have saved it
          </Button>
        </div>
      )}
      <form className="ag-issue" onSubmit={issue}>
        <Field label="Where it is used" required>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Claude Code on my laptop"
          />
        </Field>
        <Field label="Expires after">
          <Select value={days} onChange={(e) => setDays(e.target.value)}>
            {['7', '30', '90', '365'].map((d) => (
              <option key={d} value={d}>
                {d} days
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" variant="primary" loading={busy} disabled={!name.trim()}>
          Issue token
        </Button>
        {isOwner && (
          <Checkbox
            label="It acts on my behalf"
            description="What it does is recorded as the agent, for you. Only its owner can issue such a token."
            checked={onBehalf}
            onChange={(e) => setOnBehalf(e.target.checked)}
          />
        )}
      </form>
      {tokens.isError ? (
        <Callout tone="danger" title="Tokens could not be loaded">
          {errorMessage(tokens.error)}
        </Callout>
      ) : (
        list.length > 0 && (
          <ul className="ag-tokens">
            {list.map((t) => {
              const state = tokenState(t)
              return (
                <li key={t.id}>
                  <code>{t.prefix}...</code>
                  <span>{t.name}</span>
                  <Badge tone={state === 'active' ? 'success' : 'neutral'}>{state}</Badge>
                  <small className="ws-muted">
                    {state === 'active' ? `expires ${formatRelative(t.expires_at)}, ` : ''}
                    {t.last_used_at ? `used ${formatRelative(t.last_used_at)}` : 'never used'}
                  </small>
                  {state === 'active' && (
                    <Button size="sm" variant="ghost" onClick={() => void revoke(t.id)}>
                      Revoke
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )
      )}
    </section>
  )
}

/** The owner's conversation with their agent: opened on request, not on sight. */
function TalkTo({ agent }: { agent: Agent }) {
  const toast = useToast()
  const [conversation, setConversation] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const open = async () => {
    setBusy(true)
    try {
      setConversation((await messagesApi.open(agent.id)).id)
    } catch (e) {
      toast.error('Could not open the conversation', e)
    }
    setBusy(false)
  }
  return (
    <section className="ag-section" aria-labelledby="ag-talk">
      <div className="ag-section-head">
        <div>
          <h2 id="ag-talk">Talk to {agent.name}</h2>
          <p className="ws-muted">
            Ask it something or tell it what to leave alone. It is told through a trigger for
            messages, or reads them when it next checks.
          </p>
        </div>
        {!conversation && (
          <Button variant="secondary" loading={busy} onClick={() => void open()}>
            Open the conversation
          </Button>
        )}
      </div>
      {conversation && <Conversation conversationId={conversation} mute={false} />}
    </section>
  )
}

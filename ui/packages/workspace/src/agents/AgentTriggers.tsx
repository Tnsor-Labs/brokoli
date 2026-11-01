import { Fragment, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Plus, RotateCw, Zap } from 'lucide-react'
import {
  pipelineApi,
  triggersApi,
  type Agent,
  type AgentTrigger,
  type TriggerEventType,
} from '@brokoli/api'
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Field,
  IconButton,
  Input,
  Menu,
  Modal,
  SegmentedControl,
  Select,
  Skeleton,
  Switch,
  errorMessage,
  formatDateTime,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import {
  COOLDOWNS,
  canRedeliver,
  cooldownLabel,
  deliveryRun,
  deliveryStatus,
  deliveryTarget,
  isTestDelivery,
  lastOutcome,
  triggerProblem,
} from './model'
import '../workspace.css'
import './agents.css'

/*
 * An agent's triggers (ADR-047): which run events it is told about, where
 * they go, and every delivery. Exported for a distribution's own agent
 * page as well as the one here.
 */

const triggersKey = (agentId: string) => ['agents', agentId, 'triggers'] as const

function usePipelineNames() {
  const q = useQuery({
    queryKey: ['agents', 'pipelines'],
    queryFn: pipelineApi.list,
    staleTime: 60_000,
  })
  const names = new Map((q.data ?? []).map((p) => [p.id, p.name]))
  return { pipelines: q.data ?? [], name: (id: string) => names.get(id) ?? id }
}

export function AgentTriggers({ agent }: { agent: Agent }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const key = triggersKey(agent.id)
  const triggers = useQuery({ queryKey: key, queryFn: () => triggersApi.list(agent.id) })
  const types = useQuery({
    queryKey: ['agents', 'trigger-events'],
    queryFn: triggersApi.eventTypes,
    staleTime: 300_000,
  })
  const { name: pipelineName } = usePipelineNames()
  const [editing, setEditing] = useState<AgentTrigger | 'new' | null>(null)
  const [logFor, setLogFor] = useState<AgentTrigger | null>(null)
  const [removing, setRemoving] = useState<AgentTrigger | null>(null)
  const [rotating, setRotating] = useState<AgentTrigger | null>(null)
  const [secret, setSecret] = useState<string | null>(null)

  const refresh = () => queryClient.invalidateQueries({ queryKey: key })
  const setEnabled = async (t: AgentTrigger, enabled: boolean) => {
    try {
      await triggersApi.update(agent.id, t.id, { enabled })
      await refresh()
    } catch (e) {
      toast.error(enabled ? 'Not enabled' : 'Not disabled', e)
    }
  }
  const sendTest = async (t: AgentTrigger) => {
    try {
      const res = await triggersApi.test(agent.id, t.id)
      toast.success(
        'Test event queued',
        `About ${pipelineName(res.pipeline_id)}. It carries "test": true.`,
      )
    } catch (e) {
      toast.error('No test event sent', e)
    }
  }

  if (types.data === null)
    return (
      <Callout tone="info" title="Triggers are not available here">
        This server does not deliver run events to agents.
      </Callout>
    )
  const list = triggers.data ?? []
  return (
    <section className="ag-section" aria-labelledby="ag-triggers">
      <div className="ag-section-head">
        <div>
          <h2 id="ag-triggers">Triggers</h2>
          <p className="ws-muted">
            Tell {agent.name} when runs fail, are blocked or recover, so it starts work on its own.
          </p>
        </div>
        {!agent.disabled && (
          <Button
            variant="secondary"
            icon={<Plus size={15} aria-hidden="true" />}
            onClick={() => setEditing('new')}
          >
            New trigger
          </Button>
        )}
      </div>
      {secret && <SecretOnce secret={secret} onDismiss={() => setSecret(null)} />}
      {triggers.isPending ? (
        <Skeleton height={96} />
      ) : triggers.isError ? (
        <Callout tone="danger" title="Triggers could not be loaded">
          {errorMessage(triggers.error)}
        </Callout>
      ) : !list.length ? (
        <div className="ws-empty">
          <EmptyState icon={<Zap size={20} aria-hidden="true" />} title="No triggers yet">
            Without one, {agent.name} only works when someone asks it to. A trigger posts each
            failed run to the agent's URL, or keeps it in a feed the agent reads.
          </EmptyState>
        </div>
      ) : (
        <div className="bk-table-wrap">
          <table className="bk-table ag-table">
            <thead>
              <tr>
                <th scope="col">Events</th>
                <th scope="col">Pipelines</th>
                <th scope="col">Delivered to</th>
                <th scope="col">Cooldown</th>
                <th scope="col">On</th>
                <th scope="col">
                  <span className="bk-sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((t) => (
                <tr key={t.id} className={t.enabled ? undefined : 'is-disabled'}>
                  <td>
                    <div className="ag-events">
                      {t.events.map((e) => (
                        <code key={e}>{e}</code>
                      ))}
                    </div>
                  </td>
                  <td>{t.pipeline_id ? pipelineName(t.pipeline_id) : 'Every pipeline'}</td>
                  <td>
                    <span
                      className={t.delivery === 'feed' ? undefined : 'ag-url'}
                      title={t.url || undefined}
                    >
                      {deliveryTarget(t)}
                    </span>
                  </td>
                  <td>{cooldownLabel(t.cooldown_seconds)}</td>
                  <td>
                    <Switch
                      checked={t.enabled}
                      label={t.enabled ? 'Disable this trigger' : 'Enable this trigger'}
                      disabled={agent.disabled}
                      onChange={(next) => void setEnabled(t, next)}
                    />
                  </td>
                  <td>
                    <div className="ws-actions">
                      <Button size="sm" variant="ghost" onClick={() => setLogFor(t)}>
                        Deliveries
                      </Button>
                      <Menu
                        label={`Actions for this trigger`}
                        align="end"
                        items={[
                          {
                            id: 'edit',
                            label: 'Edit',
                            onSelect: () => setEditing(t),
                            disabled: agent.disabled,
                          },
                          {
                            id: 'test',
                            label: 'Send a test event',
                            onSelect: () => void sendTest(t),
                            disabled: !t.enabled,
                          },
                          ...(t.delivery === 'webhook'
                            ? [
                                {
                                  id: 'rotate',
                                  label: 'New signing secret',
                                  onSelect: () => setRotating(t),
                                },
                              ]
                            : []),
                          'separator' as const,
                          {
                            id: 'remove',
                            label: 'Remove',
                            tone: 'danger' as const,
                            onSelect: () => setRemoving(t),
                          },
                        ]}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <TriggerForm
          agent={agent}
          existing={editing === 'new' ? undefined : editing}
          types={types.data ?? []}
          onClose={() => setEditing(null)}
          onSecret={setSecret}
        />
      )}
      {logFor && <Deliveries agent={agent} trigger={logFor} onClose={() => setLogFor(null)} />}
      {removing && (
        <ConfirmDialog
          title="Remove this trigger?"
          tone="danger"
          confirmLabel="Remove trigger"
          onCancel={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await triggersApi.remove(agent.id, removing.id)
              await refresh()
              toast.success('Trigger removed')
            } catch (e) {
              toast.error('Not removed', e)
            }
            setRemoving(null)
          }}
        >
          {agent.name} is no longer told about these events. Deliveries still waiting are not sent.
        </ConfirmDialog>
      )}
      {rotating && (
        <ConfirmDialog
          title="Make a new signing secret?"
          confirmLabel="Make a new secret"
          onCancel={() => setRotating(null)}
          onConfirm={async () => {
            try {
              const res = await triggersApi.rotateSecret(agent.id, rotating.id)
              setSecret(res.secret)
              await refresh()
            } catch (e) {
              toast.error('No new secret', e)
            }
            setRotating(null)
          }}
        >
          For 24 hours deliveries are signed with the new secret and the old one, so you have a day
          to give the receiver the new one.
        </ConfirmDialog>
      )}
    </section>
  )
}

function SecretOnce({ secret, onDismiss }: { secret: string; onDismiss: () => void }) {
  const toast = useToast()
  return (
    <div className="ag-once" role="status">
      <strong>The signing secret</strong>
      <div className="ag-once-row">
        <code>{secret}</code>
        <IconButton
          label="Copy the secret"
          onClick={() =>
            navigator.clipboard
              .writeText(secret)
              .then(() => toast.success('Copied'))
              .catch((e) => toast.error('Could not copy', e))
          }
        >
          <Copy size={15} aria-hidden="true" />
        </IconButton>
      </div>
      <p className="ws-muted">
        This is the only time it is shown. The receiver verifies each delivery with it (Standard
        Webhooks).
      </p>
      <Button size="sm" variant="ghost" onClick={onDismiss}>
        I have saved it
      </Button>
    </div>
  )
}

function TriggerForm({
  agent,
  existing,
  types,
  onClose,
  onSecret,
}: {
  agent: Agent
  existing?: AgentTrigger
  types: TriggerEventType[]
  onClose: () => void
  onSecret: (secret: string) => void
}) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const { pipelines } = usePipelineNames()
  const [delivery, setDelivery] = useState<'webhook' | 'feed'>(existing?.delivery ?? 'webhook')
  const [url, setUrl] = useState(existing?.url ?? '')
  const [events, setEvents] = useState<string[]>(existing?.events ?? ['run.failed'])
  const [pipeline, setPipeline] = useState(existing?.pipeline_id ?? '')
  const [cooldown, setCooldown] = useState(existing?.cooldown_seconds ?? 1800)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const problem = triggerProblem({ delivery, url, events })
  const cooldowns = COOLDOWNS.includes(cooldown)
    ? COOLDOWNS
    : [...COOLDOWNS, cooldown].sort((a, b) => a - b)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem || busy) return
    setBusy(true)
    setError('')
    const body = {
      events,
      pipeline_id: pipeline,
      cooldown_seconds: cooldown,
      ...(delivery === 'webhook' ? { url: url.trim() } : {}),
    }
    try {
      if (existing) {
        await triggersApi.update(agent.id, existing.id, body)
        toast.success('Trigger saved')
      } else {
        const res = await triggersApi.create(agent.id, { ...body, delivery })
        if (res.secret) onSecret(res.secret)
        toast.success(
          'Trigger made',
          delivery === 'feed' ? `${agent.name} reads its events from its feed.` : undefined,
        )
      }
      await queryClient.invalidateQueries({ queryKey: triggersKey(agent.id) })
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  const toggle = (name: string, on: boolean) =>
    setEvents((cur) => (on ? [...cur, name] : cur.filter((e) => e !== name)))

  return (
    <Modal
      title={existing ? 'Edit trigger' : 'New trigger'}
      description={`When one of these events happens, ${agent.name} is told, and starts from there with its own token.`}
      onClose={onClose}
      dismissible={!busy}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="ag-trigger-form"
            loading={busy}
            disabled={Boolean(problem)}
          >
            {existing ? 'Save trigger' : 'Make trigger'}
          </Button>
        </>
      }
    >
      <form id="ag-trigger-form" className="ws-form" onSubmit={submit} noValidate>
        <Field
          label="How the agent hears about it"
          hint={
            existing
              ? 'This cannot change; make a new trigger instead.'
              : delivery === 'webhook'
                ? 'Each event is posted, signed, to a URL the agent listens on.'
                : 'For an agent with no URL: it reads its events with its own token (the next_events MCP tool).'
          }
        >
          <SegmentedControl
            label="How the agent hears about it"
            value={delivery}
            onChange={(v) => !existing && setDelivery(v)}
            options={[
              { value: 'webhook', label: 'Webhook' },
              { value: 'feed', label: 'Feed' },
            ]}
          />
        </Field>
        {delivery === 'webhook' && (
          <Field label="URL" required>
            <Input
              autoFocus={!existing}
              value={url}
              inputMode="url"
              placeholder="https://agents.example.com/brokoli"
              onChange={(e) => setUrl(e.target.value)}
            />
          </Field>
        )}
        <fieldset className="ag-fieldset">
          <legend>Events</legend>
          {types.map((t) => (
            <Checkbox
              key={t.name}
              label={<code>{t.name}</code>}
              description={t.description}
              checked={events.includes(t.name)}
              onChange={(e) => toggle(t.name, e.target.checked)}
            />
          ))}
        </fieldset>
        <div className="ws-row-2 ag-row-2">
          <Field label="Pipelines" hint="Only pipelines the agent may see are ever delivered.">
            <Select value={pipeline} onChange={(e) => setPipeline(e.target.value)}>
              <option value="">Every pipeline in this workspace</option>
              {pipelines.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Cooldown"
            hint="After telling the agent, wait this long before telling it the same about the same pipeline."
          >
            <Select value={String(cooldown)} onChange={(e) => setCooldown(Number(e.target.value))}>
              {cooldowns.map((s) => (
                <option key={s} value={s}>
                  {cooldownLabel(s)}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {(error || (problem && events.length === 0)) && (
          <Callout tone="danger" title={existing ? 'Not saved' : 'Not made'}>
            {error || problem}
          </Callout>
        )}
      </form>
    </Modal>
  )
}

function Deliveries({
  agent,
  trigger,
  onClose,
}: {
  agent: Agent
  trigger: AgentTrigger
  onClose: () => void
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const key = ['agents', agent.id, 'triggers', trigger.id, 'deliveries']
  const deliveries = useQuery({
    queryKey: key,
    queryFn: () => triggersApi.deliveries(agent.id, trigger.id),
    refetchInterval: 5_000,
  })
  const { name: pipelineName } = usePipelineNames()
  const [open, setOpen] = useState<string | null>(null)

  const redeliver = async (id: string) => {
    try {
      await triggersApi.redeliver(agent.id, trigger.id, id)
      await queryClient.invalidateQueries({ queryKey: key })
      toast.success('Queued again', 'It is sent within a few seconds.')
    } catch (e) {
      toast.error('Not queued', e)
    }
  }
  const list = deliveries.data ?? []
  return (
    <Drawer
      title="Deliveries"
      description={`The newest 50 events this trigger sent to ${agent.name}, with every attempt.`}
      onClose={onClose}
      width={760}
    >
      {deliveries.isPending ? (
        <Skeleton height={120} />
      ) : deliveries.isError ? (
        <Callout tone="danger" title="Deliveries could not be loaded">
          {errorMessage(deliveries.error)}
        </Callout>
      ) : !list.length ? (
        <EmptyState icon={<Zap size={20} aria-hidden="true" />} title="Nothing delivered yet">
          Events appear here as they happen. Send a test event to try the receiver now.
        </EmptyState>
      ) : (
        <table className="bk-table ag-log">
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">Event</th>
              <th scope="col">About</th>
              <th scope="col">Status</th>
              <th scope="col">
                <span className="bk-sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.map((d) => {
              const status = deliveryStatus(d, trigger.delivery)
              const run = deliveryRun(d)
              const outcome = lastOutcome(d)
              const expanded = open === d.id
              return (
                <Fragment key={d.id}>
                  <tr
                    className="is-clickable"
                    onClick={() => setOpen(expanded ? null : d.id)}
                    aria-expanded={expanded}
                  >
                    <td title={formatDateTime(d.created_at)}>{formatRelative(d.created_at)}</td>
                    <td>
                      <code>{d.event}</code>
                      {isTestDelivery(d) && (
                        <Badge tone="neutral" className="ag-test">
                          test
                        </Badge>
                      )}
                    </td>
                    <td>
                      {run ? (
                        <Link
                          to={`/pipelines/${encodeURIComponent(d.pipeline_id)}/runs?run=${encodeURIComponent(run)}`}
                          onClick={(e) => e.stopPropagation()}
                        >
                          {pipelineName(d.pipeline_id)}
                        </Link>
                      ) : d.pipeline_id ? (
                        pipelineName(d.pipeline_id)
                      ) : (
                        'A message to it'
                      )}
                    </td>
                    <td>
                      <Badge tone={status.tone}>{status.label}</Badge>
                      {outcome && <small className="ag-outcome">{outcome}</small>}
                    </td>
                    <td>
                      {canRedeliver(d, trigger.delivery) && (
                        <IconButton
                          size="sm"
                          label="Send it again"
                          onClick={(e) => {
                            e.stopPropagation()
                            void redeliver(d.id)
                          }}
                        >
                          <RotateCw size={14} aria-hidden="true" />
                        </IconButton>
                      )}
                    </td>
                  </tr>
                  {expanded && (
                    <tr className="ag-attempts">
                      <td colSpan={5}>
                        {d.attempts_log?.length ? (
                          <ol>
                            {d.attempts_log.map((a, i) => (
                              <li key={i}>
                                <span>{formatDateTime(a.at)}</span>
                                <span>
                                  {a.status_code ? `HTTP ${a.status_code}` : a.error || 'no answer'}
                                </span>
                                <span className="ws-muted">{a.duration_ms} ms</span>
                                {a.response && <code>{a.response}</code>}
                              </li>
                            ))}
                          </ol>
                        ) : (
                          <p className="ws-muted">
                            {trigger.delivery === 'feed'
                              ? 'A feed delivery is not sent; the agent reads it.'
                              : d.next_attempt_at
                                ? `Not tried yet; next attempt ${formatRelative(d.next_attempt_at)}.`
                                : 'No attempts recorded.'}
                          </p>
                        )}
                      </td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      )}
    </Drawer>
  )
}

import { useMemo, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Database, Globe, HardDrive, Plug, PlugZap } from 'lucide-react'
import { connectionApi, type Connection, type ConnectionTestResult, type ConnectionTypeMeta } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, Field, Input, Modal, SearchInput, Textarea, cx, errorMessage, useToast } from '@brokoli/ui'
import { CATEGORY_LABEL, DEFAULT_PORT, DRIVER_OPTIONS, USABLE_BY_NODES, externalSecret, formFields, groupTypes } from './catalog'

import { VendorIcon } from './VendorIcon'

export const CATEGORY_ICON: Record<string, typeof Database> = { database: Database, storage: HardDrive, api: Globe, other: Plug }

type Draft = {
  conn_id: string
  type: string
  description: string
  host: string
  port: string
  schema: string
  login: string
  password: string
  extra: string
  max_concurrent: string
}

const CONN_ID = /^[a-z0-9_-]+$/

function draftFrom(c?: Connection, type = ''): Draft {
  return {
    conn_id: c?.conn_id ?? '',
    type: c?.type ?? type,
    description: c?.description ?? '',
    host: c?.host ?? '',
    port: c?.port ? String(c.port) : '',
    schema: c?.schema ?? '',
    login: c?.login ?? '',
    password: '',
    extra: '',
    max_concurrent: c?.max_concurrent ? String(c.max_concurrent) : '',
  }
}

export function TestResult({ result }: { result: ConnectionTestResult }) {
  return (
    <Callout tone={result.success ? 'success' : 'danger'} title={result.success ? 'Connection works' : 'Connection failed'}>
      {result.success ? result.message || 'The server reached it.' : result.error || result.message || 'The server reported a failure without details.'}
      {result.driver && <span className="ws-muted"> Driver: {result.driver}.</span>}
    </Callout>
  )
}

/*
 * Create or edit a connection.
 *
 * Every field the server stores is sent back, including the ones this type
 * does not display, because an update is a full replace and would otherwise
 * blank them. Secrets are write-only: an empty password or extra keeps the
 * stored value. When the stored secret is an external reference (env://,
 * vault://, k8s://) the server ignores a typed value, so the field says
 * where the secret lives instead of accepting input that would be dropped.
 */
export function ConnectionForm({
  existing,
  types,
  onClose,
}: {
  existing?: Connection
  types: ConnectionTypeMeta[]
  onClose: () => void
}) {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const [saved, setSaved] = useState<Connection | undefined>(existing)
  const [step, setStep] = useState<'catalog' | 'form'>(existing ? 'form' : 'catalog')
  const [draft, setDraft] = useState<Draft>(() => draftFrom(existing))
  const [pristine, setPristine] = useState<Draft>(() => draftFrom(existing))
  const [query, setQuery] = useState('')
  const [busy, setBusy] = useState<'save' | 'test' | null>(null)
  const [error, setError] = useState('')
  const [test, setTest] = useState<ConnectionTestResult | null>(null)
  const editing = Boolean(saved)
  const meta = types.find((t) => t.type === draft.type)
  const fields = formFields(draft.type, meta)
  const has = (f: string) => fields.includes(f)
  const hint = (f: string) => meta?.hints?.[f]
  const dirty = JSON.stringify(draft) !== JSON.stringify(pristine)
  const passwordSecret = externalSecret(saved?.password_ref)
  const extraSecret = externalSecret(saved?.extra_ref)
  const set = (patch: Partial<Draft>) => {
    setDraft((d) => ({ ...d, ...patch }))
    setTest(null)
  }

  const problems = useMemo(() => {
    const p: Partial<Record<keyof Draft, string>> = {}
    if (!draft.conn_id.trim()) p.conn_id = 'Required'
    else if (!CONN_ID.test(draft.conn_id)) p.conn_id = 'Lowercase letters, digits, hyphens and underscores only'
    if (draft.port && (!/^\d+$/.test(draft.port) || Number(draft.port) < 1 || Number(draft.port) > 65535)) p.port = 'A port between 1 and 65535'
    if (draft.max_concurrent && (!/^\d+$/.test(draft.max_concurrent))) p.max_concurrent = 'A whole number, 0 or more'
    if (draft.extra.trim()) {
      try {
        const v = JSON.parse(draft.extra)
        if (!v || typeof v !== 'object' || Array.isArray(v)) p.extra = 'Must be a JSON object'
      } catch {
        p.extra = 'Not valid JSON'
      }
    }
    return p
  }, [draft])
  const invalid = Object.keys(problems).length > 0

  const filteredTypes = useMemo(() => {
    const q = query.trim().toLowerCase()
    return types.filter((t) => !q || `${t.label} ${t.type} ${t.description ?? ''}`.toLowerCase().includes(q))
  }, [types, query])

  const save = async (): Promise<Connection | null> => {
    setBusy('save')
    setError('')
    const body: Partial<Connection> = {
      conn_id: draft.conn_id.trim(),
      type: draft.type,
      description: draft.description,
      host: draft.host,
      port: draft.port ? Number(draft.port) : 0,
      schema: draft.schema,
      login: draft.login,
      max_concurrent: draft.max_concurrent ? Number(draft.max_concurrent) : 0,
    }
    if (draft.password && !passwordSecret) body.password = draft.password
    if (draft.extra.trim() && !extraSecret) body.extra = draft.extra.trim()
    // Echo stored refs: the server keeps an encrypted secret when its masked ref comes back, and an external ref stays authoritative.
    if (saved?.password_ref) body.password_ref = saved.password_ref
    if (saved?.extra_ref) body.extra_ref = saved.extra_ref
    try {
      const result = saved ? await connectionApi.update(saved.conn_id, body) : await connectionApi.create(body)
      await queryClient.invalidateQueries({ queryKey: ['connections'] })
      toast.success(saved ? `Updated ${result.conn_id}` : `Created ${result.conn_id}`)
      setSaved(result)
      const next = { ...draftFrom(result), password: '', extra: '' }
      setDraft(next)
      setPristine(next)
      return result
    } catch (e) {
      setError(errorMessage(e))
      return null
    } finally {
      setBusy(null)
    }
  }

  const runTest = async (target: Connection) => {
    setBusy('test')
    setTest(null)
    try {
      setTest(await connectionApi.test(target.conn_id))
    } catch (e) {
      setTest({ success: false, error: errorMessage(e) })
    } finally {
      setBusy(null)
    }
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid || busy) return
    const result = await save()
    if (result && existing) onClose()
  }

  const canEdit = editing ? session.can('connections.edit') : session.can('connections.create')

  if (step === 'catalog')
    return (
      <Modal title="New connection" description="Pick the system to connect to." size="lg" onClose={onClose}>
        <div className="ws-catalog">
          <SearchInput value={query} onChange={setQuery} placeholder="Search types, for example postgres, bucket, api" autoFocus />
          {!types.length && <Callout tone="warning">The server returned no connection types.</Callout>}
          {groupTypes(filteredTypes).map((g) => {
            return (
              <section key={g.category}>
                <h3>{CATEGORY_LABEL[g.category]}</h3>
                <div className="ws-catalog-grid">
                  {g.types.map((t) => (
                    <button
                      key={t.type}
                      type="button"
                      className={cx('ws-type-card', `is-${g.category}`)}
                      onClick={() => {
                        setDraft({ ...draftFrom(undefined, t.type), conn_id: draft.conn_id, description: draft.description })
                        setStep('form')
                      }}
                    >
                      <span className="ws-type-icon is-brand">
                        <VendorIcon type={t.type} category={t.category} size={18} />
                      </span>
                      <span>
                        <strong>{t.label}</strong>
                        <small>{t.description}</small>
                      </span>
                      {!USABLE_BY_NODES.has(t.type) && <Badge tone="warning">Not usable by nodes yet</Badge>}
                    </button>
                  ))}
                </div>
              </section>
            )
          })}
          {types.length > 0 && !filteredTypes.length && <p className="ws-muted">No connection type matches "{query}".</p>}
        </div>
      </Modal>
    )

  return (
    <Modal
      title={editing ? `Connection ${saved!.conn_id}` : `New ${meta?.label ?? draft.type} connection`}
      description={meta?.description}
      size="md"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          {editing && session.can('connections.test') && (
            <Button
              icon={<PlugZap size={15} aria-hidden="true" />}
              loading={busy === 'test'}
              disabled={Boolean(busy) || (dirty && (invalid || !canEdit))}
              title={dirty ? 'Saves your changes, then tests the saved connection' : 'Tests the saved connection from the server'}
              onClick={async () => {
                const target = dirty ? await save() : saved
                if (target) await runTest(target)
              }}
            >
              {dirty ? 'Save and test' : 'Test connection'}
            </Button>
          )}
          <span className="ws-spacer" />
          <Button variant="ghost" onClick={onClose} disabled={Boolean(busy)}>
            {editing && !dirty ? 'Close' : 'Cancel'}
          </Button>
          {canEdit && (
            <Button variant="primary" type="submit" form="ws-connection-form" loading={busy === 'save'} disabled={invalid || (editing && !dirty)}>
              {editing ? 'Save changes' : 'Create connection'}
            </Button>
          )}
        </>
      }
    >
      <form id="ws-connection-form" className="ws-form" onSubmit={submit} noValidate>
        <fieldset disabled={!canEdit} className="ws-fieldset">
          {!editing && (
            <button type="button" className="ws-change-type" onClick={() => setStep('catalog')}>
              <ArrowLeft size={14} aria-hidden="true" /> Choose a different type
            </button>
          )}
          {!USABLE_BY_NODES.has(draft.type) && (
            <Callout tone="warning" title="Pipelines cannot use this type yet">
              The engine has no driver for {meta?.label ?? draft.type}, so database, file and API nodes will not accept it. You can still store it for reference.
            </Callout>
          )}
          <Field label="Connection ID" required error={problems.conn_id} hint={editing ? 'Pipelines refer to the connection by this ID, so it cannot change.' : 'Nodes refer to the connection by this ID.'}>
            <Input mono value={draft.conn_id} disabled={editing} placeholder={`my_${draft.type || 'connection'}`} onChange={(e) => set({ conn_id: e.target.value.toLowerCase() })} autoFocus={!editing} />
          </Field>
          <Field label="Description">
            <Input value={draft.description} placeholder="Production warehouse" onChange={(e) => set({ description: e.target.value })} />
          </Field>
          {has('host') && (
            <div className={cx(has('port') && 'ws-row')}>
              <Field label={draft.type === 'sqlite' ? 'Database file path' : 'Host'}>
                <Input mono value={draft.host} placeholder={draft.type === 'sqlite' ? '/data/app.db' : hint('host') ?? 'db.internal.example.com'} onChange={(e) => set({ host: e.target.value })} />
              </Field>
              {has('port') && (
                <Field label="Port" error={problems.port}>
                  <Input mono inputMode="numeric" value={draft.port} placeholder={DEFAULT_PORT[draft.type] ? `Default ${DEFAULT_PORT[draft.type]}` : hint('port') ?? ''} onChange={(e) => set({ port: e.target.value.trim() })} />
                </Field>
              )}
            </div>
          )}
          {draft.type === 'http' && draft.host.includes('://') && <p className="ws-warning">Enter the host without a scheme; the scheme comes from the port.</p>}
          {has('schema') && (
            <Field label={draft.type === 'bigquery' ? 'Project and dataset' : 'Database or schema'}>
              <Input mono value={draft.schema} placeholder={hint('schema') ?? 'analytics'} onChange={(e) => set({ schema: e.target.value })} />
            </Field>
          )}
          {has('login') && (
            <Field label="Username">
              <Input value={draft.login} autoComplete="off" placeholder={hint('login') ?? ''} onChange={(e) => set({ login: e.target.value })} />
            </Field>
          )}
          {has('password') &&
            (passwordSecret ? (
              <Callout tone="info" title="Password from a secret store">
                This connection reads its password from <code>{passwordSecret.ref}</code>. Change it there; a password typed here would be ignored.
              </Callout>
            ) : (
              <Field label="Password" hint={editing ? 'Leave empty to keep the stored password. It is encrypted and never shown again.' : 'Encrypted on the server and never shown again.'}>
                <Input type="password" autoComplete="new-password" value={draft.password} placeholder={editing ? 'Unchanged' : hint('password') ?? ''} onChange={(e) => set({ password: e.target.value })} />
              </Field>
            ))}
          {has('extra') &&
            (extraSecret ? (
              <Callout tone="info" title="Extra settings from a secret store">
                Read from <code>{extraSecret.ref}</code>. Change them there.
              </Callout>
            ) : (
              <Field
                label={DRIVER_OPTIONS[draft.type] && !meta?.fields?.includes('extra') ? 'Driver options (JSON)' : 'Extra settings (JSON)'}
                error={problems.extra}
                hint={
                  <>
                    {editing && 'Stored encrypted and never shown. Leave empty to keep the current value; anything you enter replaces all of it. '}
                    {DRIVER_OPTIONS[draft.type] && <>Driver options read by the engine: {DRIVER_OPTIONS[draft.type].join(', ')}.</>}
                  </>
                }
              >
                <Textarea mono rows={4} value={draft.extra} placeholder={hint('extra') ?? (DRIVER_OPTIONS[draft.type] ? `{"${DRIVER_OPTIONS[draft.type][0]}": "..."}` : '{}')} onChange={(e) => set({ extra: e.target.value })} />
              </Field>
            ))}
          <Field label="Maximum concurrent nodes" error={problems.max_concurrent} hint="How many nodes may use this connection at once, per server. Empty or 0 means no limit; nodes over the limit wait.">
            <Input inputMode="numeric" value={draft.max_concurrent} placeholder="No limit" onChange={(e) => set({ max_concurrent: e.target.value.trim() })} />
          </Field>
        </fieldset>
        {error && <Callout tone="danger" title="Not saved">{error}</Callout>}
        {editing && !existing && !test && <Callout tone="success" title="Connection created">Test it now to check the server can reach it.</Callout>}
        {test && <TestResult result={test} />}
      </form>
    </Modal>
  )
}

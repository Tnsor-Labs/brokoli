import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Database, Globe, HardDrive, Plug, PlugZap } from 'lucide-react'
import { connectionApi, driverApi, type Connection, type ConnectionTestResult, type ConnectionTypeMeta } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, Checkbox, Field, Input, Modal, SearchInput, SegmentedControl, Select, Textarea, cx, errorMessage, useToast } from '@brokoli/ui'
import { CATEGORY_LABEL, DEFAULT_PORT, DRIVER_OPTIONS, USABLE_BY_NODES, externalSecret, formFields, groupTypes } from './catalog'
import { EMPTY_REF, SecretRefPicker } from '../secret-stores/SecretRefPicker'
import { composeSecretRef, isSecretRef, parseSecretRef, secretRefProblem, setExtraReference, type SecretRefParts } from '../secret-stores/reference'
import { useSecretStores } from '../secret-stores/useSecretStores'
import { passwordFields } from './passwordSource'

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
  /** Where the password comes from: typed and stored by Brokoli, or a reference into a secret store. */
  password_source: 'stored' | 'store'
  password_ref: SecretRefParts
  extra: string
  s3_endpoint: string
  s3_use_path_style: boolean
  use_native_driver: boolean
  driver_identity: string
  max_concurrent: string
}

const CONN_ID = /^[a-z0-9_-]+$/

function driverIdentityKey(identity?: Connection['driver_identity']) {
  return identity ? `${identity.name}\u0000${identity.version}\u0000${identity.library_sha256}` : ''
}

function draftFrom(c?: Connection, type = ''): Draft {
  let s3Endpoint = ''
  let s3UsePathStyle = false
  if (c?.type === 's3' && c.extra) {
    try {
      const extra = JSON.parse(c.extra) as Record<string, unknown>
      if (typeof extra.endpoint === 'string') s3Endpoint = extra.endpoint
      if (extra.use_path_style === true) s3UsePathStyle = true
    } catch {
      // The server validates extra JSON; leave the structured fields empty if a legacy value cannot be parsed.
    }
  }
  return {
    conn_id: c?.conn_id ?? '',
    type: c?.type ?? type,
    description: c?.description ?? '',
    host: c?.host ?? '',
    port: c?.port ? String(c.port) : '',
    schema: c?.schema ?? '',
    login: c?.login ?? '',
    password: '',
    password_source: isSecretRef(c?.password_ref) ? 'store' : 'stored',
    password_ref: parseSecretRef(c?.password_ref) ?? EMPTY_REF,
    extra: '',
    s3_endpoint: s3Endpoint,
    s3_use_path_style: s3UsePathStyle,
    use_native_driver: c?.type === 'flightsql' || type === 'flightsql' || Boolean(c?.driver_identity),
    driver_identity: driverIdentityKey(c?.driver_identity),
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
  const secretStores = useSecretStores()
  // A secret-store reference is edited with the picker; an operator-level one (env://, vault://, k8s://) is shown read-only.
  const passwordSecret = isSecretRef(saved?.password_ref) ? null : externalSecret(saved?.password_ref)
  const extraSecret = externalSecret(saved?.extra_ref)
  const storedRefIsSecret = isSecretRef(saved?.password_ref)

  // Which installed drivers can serve this connection comes from the server
  // (usable_by), the same mapping it validates a pin against: a type offers
  // a native driver exactly when one installed here serves it. Flight SQL
  // has no other driver, so it always does.
  const installedDrivers = useQuery({
    queryKey: ['drivers', 'installed'],
    queryFn: driverApi.installed,
    enabled: Boolean(draft.type),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const nativeDrivers = (installedDrivers.data?.drivers ?? []).filter((driver) => (driver.usable_by ?? []).includes(draft.type))
  const nativeDriverType = draft.type === 'flightsql' || nativeDrivers.length > 0 || Boolean(saved?.driver_identity) ? draft.type : null
  const nativeDriverLabel = meta?.label ?? draft.type
  const selectedNativeDriver = nativeDrivers.find((driver) => driverIdentityKey(driver) === draft.driver_identity)
  const nativeDriverRequired = draft.type === 'flightsql' || draft.use_native_driver
  const nativeDriverSaveBlocked = nativeDriverRequired && (installedDrivers.isPending || !selectedNativeDriver)
  // The test runs through the native driver, so it needs a server that can
  // load one; the save does not, since another worker may run the connection.
  const nativeDriverTestBlocked = nativeDriverRequired && installedDrivers.data?.native_worker_enabled === false
  const [extraRefKey, setExtraRefKey] = useState('')
  const [extraRef, setExtraRef] = useState<SecretRefParts>(EMPTY_REF)
  const [extraRefOpen, setExtraRefOpen] = useState(false)
  const extraRefProblem = !extraRefKey.trim() ? 'Name the setting' : secretRefProblem(extraRef, secretStores.shapeOf(extraRef.store))
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
    if (draft.password_source === 'store') {
      const problem = secretRefProblem(draft.password_ref, secretStores.shapeOf(draft.password_ref.store))
      if (problem) p.password_ref = problem
    } else if (storedRefIsSecret && !draft.password) p.password = 'Enter the password to store it in Brokoli instead of the secret store'
    if (draft.extra.trim()) {
      try {
        const v = JSON.parse(draft.extra)
        if (!v || typeof v !== 'object' || Array.isArray(v)) p.extra = 'Must be a JSON object'
      } catch {
        p.extra = 'Not valid JSON'
      }
    }
    return p
    // shapeOf reads only the store and provider lists.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft, secretStores.stores, secretStores.providers, storedRefIsSecret])
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
    if (nativeDriverRequired && selectedNativeDriver) {
      body.driver_identity = {
        name: selectedNativeDriver.name,
        version: selectedNativeDriver.version,
        library_sha256: selectedNativeDriver.library_sha256,
      }
    } else if (saved?.driver_identity) {
      // Leaving the field out keeps the server's pin; unpinning is explicit.
      body.driver_identity = null
    }
    Object.assign(body, passwordFields(draft.password_source, draft.password_ref, draft.password, saved?.password_ref, Boolean(passwordSecret)))
    if (draft.type === 's3' && !extraSecret) {
      let extra: Record<string, unknown> = {}
      if (draft.extra.trim()) {
        try {
          extra = JSON.parse(draft.extra) as Record<string, unknown>
        } catch {
          // The form is already invalid for malformed JSON.
        }
      }
      if (draft.s3_endpoint.trim()) extra.endpoint = draft.s3_endpoint.trim()
      else delete extra.endpoint
      if (draft.s3_use_path_style) extra.use_path_style = true
      else delete extra.use_path_style
      body.extra = JSON.stringify(extra)
    } else if (draft.extra.trim() && !extraSecret) body.extra = draft.extra.trim()
    // Echo stored refs: the server keeps an encrypted secret when its masked ref comes back, and an external ref stays authoritative.
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
    if (invalid || busy || nativeDriverSaveBlocked) return
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
              disabled={Boolean(busy) || nativeDriverTestBlocked || (dirty && (invalid || !canEdit))}
              title={nativeDriverTestBlocked ? 'This server cannot load native drivers, so it cannot test this connection; a worker that can will run it' : dirty ? 'Saves your changes, then tests the saved connection' : 'Tests the saved connection from the server'}
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
            <Button variant="primary" type="submit" form="ws-connection-form" loading={busy === 'save'} disabled={invalid || nativeDriverSaveBlocked || (editing && !dirty)}>
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
          {nativeDriverType && draft.type !== 'flightsql' && (
            <Callout tone="info" title={`${nativeDriverLabel} driver`}>
              The standard {nativeDriverLabel} driver remains in use unless you explicitly select an installed native ADBC driver below.
            </Callout>
          )}
          {draft.type === 'clickhouse' && draft.use_native_driver && (
            <Callout tone="info" title="ClickHouse over HTTP">
              The native ClickHouse driver uses the HTTP interface: port 8123, or 8443 with <code>&quot;secure&quot;: true</code>. Port 9000 is the native protocol the standard driver uses.
            </Callout>
          )}
          {nativeDriverType && (
            <>
              {draft.type === 'flightsql' && (
                <Callout tone="info" title="Read through a native driver">
                  Flight SQL sources run the driver selected below in an isolated worker process and store their results as Arrow. Test runs <code>SELECT 1</code> through the same driver.
                </Callout>
              )}
              {draft.type !== 'flightsql' && (
                <Checkbox
                  label="Use an installed native ADBC driver"
                  description={`Pins this connection to one installed ${nativeDriverLabel} driver. Leave unchecked to use the standard ${nativeDriverLabel} driver.`}
                  checked={draft.use_native_driver}
                  onChange={(e) => set({ use_native_driver: e.target.checked, driver_identity: e.target.checked ? draft.driver_identity : '' })}
                />
              )}
              {nativeDriverRequired && (
                <>
                  {installedDrivers.isPending ? (
                    <Callout tone="info" title={`Checking installed ${nativeDriverLabel} drivers`}>
                      Loading the native drivers installed on this server.
                    </Callout>
                  ) : !nativeDrivers.length ? (
                    <Callout tone="danger" title="Native ADBC driver required">
                      No installed {nativeDriverLabel} driver was discovered on this server. <Link to="/drivers">Browse managed drivers</Link> before saving this connection.
                    </Callout>
                  ) : (
                    <Field label={`${nativeDriverLabel} driver`} required hint="The selected installed driver identity is saved with this connection.">
                      <Select value={draft.driver_identity} onChange={(e) => set({ driver_identity: e.target.value })}>
                        <option value="">Choose an installed {nativeDriverLabel} driver</option>
                        {nativeDrivers.map((driver) => (
                          <option key={driverIdentityKey(driver)} value={driverIdentityKey(driver)}>
                            {driver.name} {driver.version} ({driver.library_sha256})
                          </option>
                        ))}
                      </Select>
                    </Field>
                  )}
                </>
              )}
            </>
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
            <Field label={draft.type === 'bigquery' ? 'Project and dataset' : draft.type === 'sftp' ? 'Base directory' : 'Database or schema'}>
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
              <>
                {(secretStores.available && secretStores.stores.length > 0) || draft.password_source === 'store' ? (
                  <SegmentedControl
                    label="Where the password comes from"
                    size="sm"
                    value={draft.password_source}
                    onChange={(v) => set({ password_source: v })}
                    options={[
                      { value: 'stored', label: 'Stored in Brokoli' },
                      { value: 'store', label: 'From a secret store' },
                    ]}
                  />
                ) : null}
                {draft.password_source === 'store' ? (
                  <>
                    <SecretRefPicker
                      idPrefix="ws-password-ref"
                      stores={secretStores.stores}
                      shapeOf={secretStores.shapeOf}
                      value={draft.password_ref}
                      onChange={(password_ref) => set({ password_ref })}
                    />
                    <p className="ws-muted">Read from your secret manager each time a run needs it, on the machine that runs the node. Brokoli never stores the value.</p>
                  </>
                ) : (
                  <Field
                    label="Password"
                    error={problems.password}
                    hint={
                      storedRefIsSecret
                        ? 'The password currently comes from a secret store. Enter one to store it in Brokoli instead.'
                        : editing
                          ? 'Leave empty to keep the stored password. It is encrypted and never shown again.'
                          : 'Encrypted on the server and never shown again.'
                    }
                  >
                    <Input type="password" autoComplete="new-password" value={draft.password} placeholder={editing && !storedRefIsSecret ? 'Unchanged' : hint('password') ?? ''} onChange={(e) => set({ password: e.target.value })} />
                  </Field>
                )}
              </>
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
          {has('extra') && !extraSecret && secretStores.available && secretStores.stores.length > 0 && (
            <div className="ws-secret-ref-builder">
              {!extraRefOpen ? (
                <Button size="sm" variant="ghost" onClick={() => setExtraRefOpen(true)}>
                  Read one extra setting from a secret store
                </Button>
              ) : (
                <>
                  <Field label="Setting" required hint="The key in the extra settings whose value comes from the store, for example secret_key or private_key.">
                    <Input id="ws-extra-ref-key" mono value={extraRefKey} placeholder="secret_key" onChange={(e) => setExtraRefKey(e.target.value.trim())} />
                  </Field>
                  <SecretRefPicker idPrefix="ws-extra-ref" stores={secretStores.stores} shapeOf={secretStores.shapeOf} value={extraRef} onChange={setExtraRef} />
                  <div className="ws-inline">
                    <Button
                      size="sm"
                      disabled={Boolean(extraRefProblem)}
                      title={extraRefProblem || undefined}
                      onClick={() => {
                        set({ extra: setExtraReference(draft.extra, extraRefKey, composeSecretRef(extraRef)) })
                        setExtraRefKey('')
                        setExtraRef(EMPTY_REF)
                        setExtraRefOpen(false)
                      }}
                    >
                      Insert into extra settings
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setExtraRefOpen(false)}>
                      Cancel
                    </Button>
                  </div>
                  {editing && <p className="ws-muted">The extra settings you type replace all of the stored ones, so include every setting the connection needs.</p>}
                </>
              )}
            </div>
          )}
          {draft.type === 's3' && !extraSecret && (
            <>
              <Field label="S3 endpoint" hint="Leave empty for AWS S3. Set this for MinIO or another S3-compatible service.">
                <Input mono value={draft.s3_endpoint} placeholder="https://objects.example.com" onChange={(e) => set({ s3_endpoint: e.target.value })} />
              </Field>
              <Checkbox
                label="Use path-style addressing"
                description="Required by many S3-compatible services, including local MinIO."
                checked={draft.s3_use_path_style}
                onChange={(e) => set({ s3_use_path_style: e.target.checked })}
              />
            </>
          )}
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

import { useMemo, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FlaskConical, KeyRound, PencilLine, Plus, Trash2, X } from 'lucide-react'
import {
  secretStoreApi,
  type SecretStore,
  type SecretStoreAuthMethod,
  type SecretStoreProvider,
  type SecretStoreTestResult,
} from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  EmptyState,
  Field,
  IconButton,
  Input,
  Modal,
  Page,
  PageHeader,
  Select,
  Skeleton,
  errorMessage,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import { validStoreName } from './reference'
import { storesUnavailable } from './useSecretStores'
import '../workspace.css'

type SettingSpec = {
  key: string
  label: string
  placeholder?: string
  required?: boolean
  hint?: string
}

/** The settings each built-in provider reads, so the form can name them. Unknown providers get a free key/value list. */
const PROVIDER_SETTINGS: Record<string, SettingSpec[]> = {
  vault: [
    {
      key: 'address',
      label: 'Address',
      placeholder: 'https://vault.internal:8200',
      required: true,
    },
    { key: 'namespace', label: 'Namespace', placeholder: 'Empty unless you use Vault namespaces' },
    {
      key: 'mount',
      label: 'KV mount',
      placeholder: 'secret',
      hint: 'The KV version 2 secrets engine to read from.',
    },
  ],
  aws_secrets_manager: [
    { key: 'region', label: 'Region', placeholder: 'eu-west-1', required: true },
  ],
  aws_ssm: [{ key: 'region', label: 'Region', placeholder: 'eu-west-1', required: true }],
}

/** The auth settings each method reads, per provider family. */
function authSettingSpecs(provider: string, method: SecretStoreAuthMethod): SettingSpec[] {
  if (provider === 'vault') {
    if (method === 'oidc')
      return [
        { key: 'role', label: 'Vault role', placeholder: 'brokoli-loader', required: true },
        { key: 'auth_mount', label: 'JWT auth mount', placeholder: 'jwt' },
      ]
    if (method === 'ambient')
      return [
        {
          key: 'role',
          label: 'Vault role',
          placeholder: 'brokoli-loader',
          required: true,
          hint: "Logs in with the machine's Kubernetes service account.",
        },
        { key: 'auth_mount', label: 'Kubernetes auth mount', placeholder: 'kubernetes' },
      ]
    return []
  }
  if (provider.startsWith('aws_')) {
    if (method === 'oidc')
      return [
        {
          key: 'role_arn',
          label: 'Role ARN',
          placeholder: 'arn:aws:iam::123456789012:role/brokoli-reader',
          required: true,
        },
        { key: 'audience', label: 'Token audience', placeholder: 'sts.amazonaws.com' },
      ]
    if (method === 'ambient')
      return [
        {
          key: 'role_arn',
          label: 'Role to assume',
          placeholder: 'Empty to use the machine identity as it is',
        },
      ]
    return []
  }
  return []
}

const METHOD_LABEL: Record<SecretStoreAuthMethod, string> = {
  ambient: "The machine's own identity",
  oidc: 'Short-lived OIDC token',
  token: 'Stored token',
}

const METHOD_HINT: Record<SecretStoreAuthMethod, string> = {
  ambient:
    'Uses the identity of the machine that runs the node: an AWS role, a Kubernetes service account. Nothing is stored. Some deployments disable this method.',
  oidc: 'Exchanges a short-lived token naming the workspace, this store and the run for the secret manager’s own session. Nothing is stored.',
  token:
    'Stores a static token, encrypted. The least safe method: anyone with the database and its key can use it. Use it only when nothing else works.',
}

type Row = { key: string; value: string }

type Draft = {
  name: string
  description: string
  provider: string
  settings: Record<string, string>
  extraSettings: Row[]
  auth_method: SecretStoreAuthMethod | ''
  auth_settings: Record<string, string>
  credential: string
}

function draftFrom(s: SecretStore | undefined, providers: SecretStoreProvider[]): Draft {
  const provider = s?.provider ?? providers[0]?.name ?? ''
  const known = new Set((PROVIDER_SETTINGS[provider] ?? []).map((x) => x.key))
  const settings = { ...(s?.settings ?? {}) }
  const extraSettings = Object.entries(settings)
    .filter(([k]) => !known.has(k))
    .map(([key, value]) => ({ key, value }))
  return {
    name: s?.name ?? '',
    description: s?.description ?? '',
    provider,
    settings,
    extraSettings,
    auth_method:
      s?.auth_method ?? providers.find((p) => p.name === provider)?.auth_methods[0] ?? '',
    auth_settings: { ...(s?.auth_settings ?? {}) },
    credential: '',
  }
}

/** Create or edit a store. The token is write-only: empty keeps the stored one. */
function StoreForm({
  existing,
  providers,
  onClose,
}: {
  existing?: SecretStore
  providers: SecretStoreProvider[]
  onClose: () => void
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<Draft>(() => draftFrom(existing, providers))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const provider = providers.find((p) => p.name === draft.provider)
  const specs = PROVIDER_SETTINGS[draft.provider]
  const authSpecs = draft.auth_method ? authSettingSpecs(draft.provider, draft.auth_method) : []
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  // A token stays stored only while the store keeps its method, provider and address; the server drops it otherwise.
  const keepsToken =
    Boolean(existing?.has_credential) &&
    existing?.auth_method === 'token' &&
    existing?.provider === draft.provider &&
    sameSettings(existing?.settings ?? {}, settingsOf(draft))

  const problems = useMemo(() => {
    const p: Record<string, string> = {}
    if (!draft.name) p.name = 'Required'
    else if (!validStoreName(draft.name))
      p.name = "Lowercase letters, digits and '-', not starting or ending with '-'"
    if (!draft.provider) p.provider = 'Choose a provider'
    if (!draft.auth_method) p.auth_method = 'Choose how the store authenticates'
    for (const s of specs ?? [])
      if (s.required && !draft.settings[s.key]?.trim()) p[`settings.${s.key}`] = 'Required'
    for (const s of authSpecs)
      if (s.required && !draft.auth_settings[s.key]?.trim()) p[`auth.${s.key}`] = 'Required'
    if (draft.auth_method === 'token' && !draft.credential && !keepsToken)
      p.credential = 'Required for a stored token'
    return p
  }, [draft, specs, authSpecs, keepsToken])
  const invalid = Object.keys(problems).length > 0

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid || busy) return
    setBusy(true)
    setError('')
    const authKeys = new Set(authSpecs.map((s) => s.key))
    const body: Partial<SecretStore> = {
      name: draft.name,
      description: draft.description,
      provider: draft.provider,
      settings: settingsOf(draft),
      auth_method: draft.auth_method as SecretStoreAuthMethod,
      auth_settings: Object.fromEntries(
        Object.entries(draft.auth_settings).filter(([k, v]) => authKeys.has(k) && v.trim()),
      ),
    }
    if (draft.auth_method === 'token' && draft.credential) body.credential = draft.credential
    try {
      const saved = existing
        ? await secretStoreApi.update(existing.id, body)
        : await secretStoreApi.create(body)
      await queryClient.invalidateQueries({ queryKey: ['secret-stores'] })
      toast.success(existing ? `Updated ${saved.name}` : `Created ${saved.name}`)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <Modal
      title={existing ? `Secret store ${existing.name}` : 'New secret store'}
      description={`Connections refer to it as secret://${draft.name || 'name'}/<path>.`}
      size="md"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <span className="ws-spacer" />
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="ws-store-form"
            loading={busy}
            disabled={invalid}
          >
            {existing ? 'Save changes' : 'Create store'}
          </Button>
        </>
      }
    >
      <form id="ws-store-form" className="ws-form" onSubmit={submit} noValidate>
        <Field
          label="Name"
          required
          error={problems.name}
          hint="Used in references. Renaming is refused while a connection refers to the store."
        >
          <Input
            mono
            value={draft.name}
            placeholder="vault-prod"
            autoFocus={!existing}
            onChange={(e) => set({ name: e.target.value.trim().toLowerCase() })}
          />
        </Field>
        <Field label="Description">
          <Input
            value={draft.description}
            placeholder="Production Vault"
            onChange={(e) => set({ description: e.target.value })}
          />
        </Field>
        <Field label="Provider" required error={problems.provider}>
          <Select
            value={draft.provider}
            onChange={(e) => {
              const next = providers.find((p) => p.name === e.target.value)
              set({
                provider: e.target.value,
                settings: {},
                extraSettings: [],
                auth_settings: {},
                auth_method: next?.auth_methods[0] ?? '',
              })
            }}
          >
            {providers.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name}
              </option>
            ))}
          </Select>
        </Field>
        {specs?.map((s) => (
          <Field
            key={s.key}
            label={s.label}
            required={s.required}
            error={problems[`settings.${s.key}`]}
            hint={s.hint}
          >
            <Input
              mono
              value={draft.settings[s.key] ?? ''}
              placeholder={s.placeholder}
              onChange={(e) => set({ settings: { ...draft.settings, [s.key]: e.target.value } })}
            />
          </Field>
        ))}
        <RowsEditor
          label={specs ? 'Other settings' : 'Provider settings'}
          rows={draft.extraSettings}
          onChange={(rows) => set({ extraSettings: rows })}
        />
        <Field
          label="Authentication"
          required
          error={problems.auth_method}
          hint={draft.auth_method ? METHOD_HINT[draft.auth_method] : undefined}
        >
          <Select
            value={draft.auth_method}
            onChange={(e) =>
              set({ auth_method: e.target.value as SecretStoreAuthMethod, auth_settings: {} })
            }
          >
            {(provider?.auth_methods ?? []).map((m) => (
              <option key={m} value={m}>
                {METHOD_LABEL[m] ?? m}
              </option>
            ))}
          </Select>
        </Field>
        {authSpecs.map((s) => (
          <Field
            key={s.key}
            label={s.label}
            required={s.required}
            error={problems[`auth.${s.key}`]}
            hint={s.hint}
          >
            <Input
              mono
              value={draft.auth_settings[s.key] ?? ''}
              placeholder={s.placeholder}
              onChange={(e) =>
                set({ auth_settings: { ...draft.auth_settings, [s.key]: e.target.value } })
              }
            />
          </Field>
        ))}
        {draft.auth_method === 'token' && (
          <>
            <Callout tone="warning" title="The least safe method">
              The token is stored encrypted, but a copy of the database together with its key opens
              your secret manager. Prefer the machine identity or OIDC when your secret manager
              supports them.
            </Callout>
            <Field
              label="Token"
              required={!keepsToken}
              error={problems.credential}
              hint={
                keepsToken
                  ? 'A token is stored. Leave empty to keep it, or enter a new one to replace it.'
                  : 'Encrypted on the server and never shown again.'
              }
            >
              <Input
                type="password"
                autoComplete="new-password"
                value={draft.credential}
                placeholder={keepsToken ? 'Unchanged' : ''}
                onChange={(e) => set({ credential: e.target.value })}
              />
            </Field>
            {existing?.has_credential && !keepsToken && (
              <p className="ws-warning">
                The stored token is dropped because the provider or its settings changed: enter the
                token for the new target.
              </p>
            )}
          </>
        )}
        {error && (
          <Callout tone="danger" title="Not saved">
            {error}
          </Callout>
        )}
      </form>
    </Modal>
  )
}

/** Equal regardless of key order: the server returns settings with its own ordering. */
export function sameSettings(a: Record<string, string>, b: Record<string, string>) {
  const ka = Object.keys(a)
  return ka.length === Object.keys(b).length && ka.every((k) => a[k] === b[k])
}

function settingsOf(d: Draft): Record<string, string> {
  const out: Record<string, string> = {}
  for (const s of PROVIDER_SETTINGS[d.provider] ?? [])
    if (d.settings[s.key]?.trim()) out[s.key] = d.settings[s.key].trim()
  for (const r of d.extraSettings)
    if (r.key.trim() && r.value.trim()) out[r.key.trim()] = r.value.trim()
  return out
}

function RowsEditor({
  label,
  rows,
  onChange,
}: {
  label: string
  rows: Row[]
  onChange: (rows: Row[]) => void
}) {
  return (
    <div className="ws-rows">
      <span className="ws-rows-label">{label}</span>
      {rows.map((r, i) => (
        <div key={i} className="ws-rows-item">
          <Input
            mono
            aria-label={`${label} key ${i + 1}`}
            value={r.key}
            placeholder="key"
            onChange={(e) =>
              onChange(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))
            }
          />
          <Input
            mono
            aria-label={`${label} value ${i + 1}`}
            value={r.value}
            placeholder="value"
            onChange={(e) =>
              onChange(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
            }
          />
          <IconButton
            size="sm"
            label={`Remove ${r.key || 'setting'}`}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <X size={14} aria-hidden="true" />
          </IconButton>
        </div>
      ))}
      <Button
        size="sm"
        variant="ghost"
        icon={<Plus size={14} aria-hidden="true" />}
        onClick={() => onChange([...rows, { key: '', value: '' }])}
      >
        Add a setting
      </Button>
    </div>
  )
}

/** Reads one path and reports what it saw, never the value. */
function TestStore({ store, onClose }: { store: SecretStore; onClose: () => void }) {
  const [path, setPath] = useState('')
  const [version, setVersion] = useState('')
  const [field, setField] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<SecretStoreTestResult | null>(null)
  const run = async (e: FormEvent) => {
    e.preventDefault()
    if (!path.trim() || busy) return
    setBusy(true)
    setResult(null)
    try {
      setResult(
        await secretStoreApi.test(store.id, {
          path: path.trim(),
          version: version.trim(),
          field: field.trim(),
        }),
      )
    } catch (err) {
      setResult({ success: false, error: errorMessage(err) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Test ${store.name}`}
      description="Authenticates and reads one secret. The value is never shown."
      size="md"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <span className="ws-spacer" />
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Close
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="ws-store-test"
            loading={busy}
            disabled={!path.trim()}
            icon={<FlaskConical size={15} aria-hidden="true" />}
          >
            Read it
          </Button>
        </>
      }
    >
      <form id="ws-store-test" className="ws-form" onSubmit={run} noValidate>
        <Field label="Secret path" required hint="As your secret manager names it.">
          <Input
            mono
            value={path}
            placeholder="prod/warehouse"
            autoFocus
            onChange={(e) => setPath(e.target.value)}
          />
        </Field>
        <div className="ws-row">
          <Field label="Version">
            <Input
              mono
              value={version}
              placeholder="Current"
              onChange={(e) => setVersion(e.target.value)}
            />
          </Field>
          <Field label="Field" hint="Checks the field exists.">
            <Input
              mono
              value={field}
              placeholder="Optional"
              onChange={(e) => setField(e.target.value)}
            />
          </Field>
        </div>
        {result && (
          <Callout
            tone={result.success ? 'success' : 'danger'}
            title={result.success ? 'Read it' : 'Could not read it'}
          >
            {result.success ? (
              <>
                {result.shape === 'map' ? (
                  <>
                    A map of fields: <code>{(result.fields ?? []).join(', ') || 'none'}</code>.
                  </>
                ) : (
                  'A single value.'
                )}
                {result.version && <> Version {result.version}.</>}
              </>
            ) : (
              result.error || 'The server reported a failure without details.'
            )}
          </Callout>
        )}
        {result?.note && <p className="ws-muted">{result.note}</p>}
      </form>
    </Modal>
  )
}

/** A workspace's secret stores (ADR-041): where connections can read their credentials from. */
export function SecretStoresPage() {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const list = useQuery({
    queryKey: ['secret-stores'],
    queryFn: secretStoreApi.list,
    retry: (n, e) => !storesUnavailable(e) && n < 2,
  })
  const providers = useQuery({
    queryKey: ['secret-stores', 'providers'],
    queryFn: secretStoreApi.providers,
  })
  const [editing, setEditing] = useState<SecretStore | 'new' | null>(null)
  const [testing, setTesting] = useState<SecretStore | null>(null)
  const [deleting, setDeleting] = useState<SecretStore | null>(null)
  const all = list.data ?? []
  const providerList = providers.data ?? []
  const canCreate = session.can('connections.create') && providerList.length > 0
  const canEdit = session.can('connections.edit')

  const header = (
    <PageHeader
      eyebrow="Workspace"
      title="Secret stores"
      description="Your own secret managers. A connection's credential can be a reference into one, read where the node runs and never stored in Brokoli."
      actions={
        canCreate && (
          <Button
            variant="primary"
            icon={<Plus size={16} aria-hidden="true" />}
            onClick={() => setEditing('new')}
          >
            New secret store
          </Button>
        )
      }
    />
  )

  if (list.isError && storesUnavailable(list.error))
    return (
      <Page>
        {header}
        <Callout tone="info" title="Not available on this server">
          This server's metadata store does not support secret stores. Connections can still use
          stored credentials and the operator's references.
        </Callout>
      </Page>
    )

  return (
    <Page>
      {header}
      {list.isError ? (
        <Callout
          tone="danger"
          title="Secret stores could not be loaded"
          action={
            <Button size="sm" onClick={() => void list.refetch()}>
              Try again
            </Button>
          }
        >
          {errorMessage(list.error)}
        </Callout>
      ) : list.isPending ? (
        <div className="bk-table-wrap ws-loading">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} height={22} />
          ))}
        </div>
      ) : !all.length ? (
        <div className="ws-empty">
          <EmptyState
            icon={<KeyRound size={20} aria-hidden="true" />}
            title="No secret stores yet"
            action={
              canCreate && (
                <Button
                  variant="primary"
                  icon={<Plus size={16} aria-hidden="true" />}
                  onClick={() => setEditing('new')}
                >
                  Add a secret store
                </Button>
              )
            }
          >
            {providerList.length
              ? 'Connect a secret manager such as Vault or AWS Secrets Manager, then point connection credentials at it.'
              : 'This server offers no secret-manager providers yet.'}
          </EmptyState>
        </div>
      ) : (
        <div className="bk-table-wrap">
          <table className="bk-table ws-table">
            <thead>
              <tr>
                <th scope="col">Store</th>
                <th scope="col">Provider</th>
                <th scope="col">Authentication</th>
                <th scope="col">Updated</th>
                <th scope="col">
                  <span className="bk-sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {all.map((s) => (
                <tr
                  key={s.id}
                  className={canEdit ? 'is-clickable' : undefined}
                  onClick={() => canEdit && setEditing(s)}
                >
                  <td>
                    <div className="ws-name">
                      <span className="ws-type-icon is-secret">
                        <KeyRound size={16} aria-hidden="true" />
                      </span>
                      <span>
                        <code>{s.name}</code>
                        <small>{s.description || `secret://${s.name}/...`}</small>
                      </span>
                    </div>
                  </td>
                  <td>
                    <code>{s.provider}</code>
                  </td>
                  <td>
                    <Badge tone={s.auth_method === 'token' ? 'warning' : 'neutral'}>
                      {METHOD_LABEL[s.auth_method] ?? s.auth_method}
                    </Badge>
                  </td>
                  <td>{formatRelative(s.updated_at)}</td>
                  <td>
                    <div className="ws-actions" onClick={(e) => e.stopPropagation()}>
                      {session.can('connections.test') && (
                        <IconButton
                          size="sm"
                          label={`Test ${s.name}`}
                          onClick={() => setTesting(s)}
                        >
                          <FlaskConical size={15} aria-hidden="true" />
                        </IconButton>
                      )}
                      {canEdit && (
                        <IconButton
                          size="sm"
                          label={`Edit ${s.name}`}
                          onClick={() => setEditing(s)}
                        >
                          <PencilLine size={15} aria-hidden="true" />
                        </IconButton>
                      )}
                      {session.can('connections.delete') && (
                        <IconButton
                          size="sm"
                          variant="danger"
                          label={`Delete ${s.name}`}
                          onClick={() => setDeleting(s)}
                        >
                          <Trash2 size={15} aria-hidden="true" />
                        </IconButton>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && providerList.length > 0 && (
        <StoreForm
          existing={editing === 'new' ? undefined : editing}
          providers={providerList}
          onClose={() => setEditing(null)}
        />
      )}
      {testing && <TestStore store={testing} onClose={() => setTesting(null)} />}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          tone="danger"
          confirmLabel="Delete store"
          onCancel={() => setDeleting(null)}
          onConfirm={async () => {
            await secretStoreApi.remove(deleting.id)
            await queryClient.invalidateQueries({ queryKey: ['secret-stores'] })
            toast.success(`Deleted ${deleting.name}`)
            setDeleting(null)
          }}
        >
          <p>
            A store a connection refers to cannot be deleted; the server names those connections so
            you can change them first.
          </p>
        </ConfirmDialog>
      )}
    </Page>
  )
}

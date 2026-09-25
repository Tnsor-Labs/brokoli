import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Braces, Copy, KeyRound, PencilLine, Plus, Trash2 } from 'lucide-react'
import { variableApi, type Variable, type VariableType } from '@brokoli/api'
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
  Pagination,
  SearchInput,
  Select,
  Skeleton,
  Spinner,
  Textarea,
  errorMessage,
  formatRelative,
  useToast,
  type Tone,
} from '@brokoli/ui'
import '../workspace.css'

const PAGE_SIZE = 25
const KEY = /^[A-Za-z0-9._-]+$/
const TYPES: { value: VariableType; label: string; tone: Tone }[] = [
  { value: 'string', label: 'Text', tone: 'neutral' },
  { value: 'number', label: 'Number', tone: 'accent' },
  { value: 'json', label: 'JSON', tone: 'running' },
  { value: 'secret', label: 'Secret', tone: 'danger' },
]
const typeMeta = (t: string) => TYPES.find((x) => x.value === t) ?? { value: t, label: t, tone: 'neutral' as Tone }
const reference = (key: string) => `\${var.${key}}`

/*
 * Variable editor. Secrets are write-only: editing one leaves its value
 * blank to keep what is stored, and a value is only needed to set or change
 * it. The server keeps the stored ciphertext when a secret is saved blank
 * (core brokoli#600 stopped that path from re-encrypting it).
 */
function VariableForm({ existing, taken, onClose }: { existing?: Variable; taken: Set<string>; onClose: () => void }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const [key, setKey] = useState(existing?.key ?? '')
  const [type, setType] = useState<string>(existing?.type ?? 'string')
  const [value, setValue] = useState(existing && existing.type !== 'secret' ? existing.value : '')
  const [description, setDescription] = useState(existing?.description ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const wasSecret = existing?.type === 'secret'

  const problems = useMemo(() => {
    const p: Record<string, string> = {}
    if (!key) p.key = 'Required'
    else if (!KEY.test(key)) p.key = 'Letters, digits, dots, hyphens and underscores only'
    else if (!existing && taken.has(key)) p.key = 'A variable with this key exists. Edit it instead; creating would replace it.'
    if (type === 'number' && value.trim() && !Number.isFinite(Number(value))) p.value = 'Not a number'
    if (type === 'json' && value.trim()) {
      try {
        JSON.parse(value)
      } catch {
        p.value = 'Not valid JSON'
      }
    }
    // A value is needed to set or change a secret, but not to keep one that is already stored.
    if (type === 'secret' && !value && !(existing && wasSecret)) p.value = 'Required'
    return p
  }, [key, type, value, existing, taken, wasSecret])
  const invalid = Object.keys(problems).length > 0

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid || busy) return
    setBusy(true)
    setError('')
    try {
      const body = { key, type, value, description }
      const saved = existing ? await variableApi.update(body) : await variableApi.create(body)
      await queryClient.invalidateQueries({ queryKey: ['variables'] })
      toast.success(existing ? `Updated ${saved.key}` : `Created ${saved.key}`)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <Modal
      title={existing ? `Variable ${existing.key}` : 'New variable'}
      description={`Pipelines read it as ${reference(key || 'key')} in any node setting.`}
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="ws-variable-form" loading={busy} disabled={invalid}>
            {existing ? 'Save changes' : 'Create variable'}
          </Button>
        </>
      }
    >
      <form id="ws-variable-form" className="ws-form" onSubmit={submit} noValidate>
        <Field label="Key" required error={problems.key} hint={existing ? 'The key is how pipelines refer to it, so it cannot change.' : undefined}>
          <Input mono value={key} disabled={Boolean(existing)} placeholder="warehouse_schema" onChange={(e) => setKey(e.target.value.trim())} autoFocus={!existing} />
        </Field>
        <Field label="Type">
          <Select value={type} onChange={(e) => setType(e.target.value)}>
            {TYPES.map((t) => (
              <option key={t.value} value={t.value}>
                {t.value === 'secret' ? 'Secret (encrypted, never shown again)' : t.label}
              </option>
            ))}
          </Select>
        </Field>
        {type === 'secret' ? (
          <Field
            label="Secret value"
            required={!(existing && wasSecret)}
            error={problems.value}
            hint={
              existing && wasSecret
                ? 'The stored value is never shown. Leave blank to keep it, or enter a new one to replace it.'
                : 'Encrypted on the server. Nobody can read it back, including administrators.'
            }
          >
            <Input type="password" autoComplete="new-password" value={value} onChange={(e) => setValue(e.target.value)} />
          </Field>
        ) : type === 'json' ? (
          <Field label="Value (JSON)" error={problems.value}>
            <Textarea mono rows={5} value={value} placeholder='{"region": "eu-west-1"}' onChange={(e) => setValue(e.target.value)} />
          </Field>
        ) : (
          <Field label="Value" error={problems.value} hint={wasSecret ? 'This was a secret. Saving it as another type stores the value you enter in plain text.' : undefined}>
            <Input mono={type === 'number'} inputMode={type === 'number' ? 'decimal' : undefined} value={value} onChange={(e) => setValue(e.target.value)} />
          </Field>
        )}
        <Field label="Description">
          <Input value={description} placeholder="What pipelines use it for" onChange={(e) => setDescription(e.target.value)} />
        </Field>
        {error && <Callout tone="danger" title="Not saved">{error}</Callout>}
      </form>
    </Modal>
  )
}

function DeleteVariable({ variable, onClose }: { variable: Variable; onClose: () => void }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const usage = useQuery({ queryKey: ['variables', variable.key, 'used-by'], queryFn: () => variableApi.usedBy(variable.key) })
  const pipelines = [...new Set((usage.data ?? []).map((u) => u.pipeline_name))]
  return (
    <ConfirmDialog
      title={`Delete ${variable.key}?`}
      tone="danger"
      confirmLabel="Delete variable"
      confirmText={usage.data?.length ? variable.key : undefined}
      onCancel={onClose}
      onConfirm={async () => {
        await variableApi.remove(variable.key)
        await queryClient.invalidateQueries({ queryKey: ['variables'] })
        toast.success(`Deleted ${variable.key}`)
        onClose()
      }}
    >
      {usage.isPending ? (
        <p className="ws-inline">
          <Spinner size="sm" /> Checking which pipelines use it
        </p>
      ) : usage.isError ? (
        <Callout tone="warning">Could not check which pipelines use it: {errorMessage(usage.error)}</Callout>
      ) : usage.data.length ? (
        <Callout tone="danger" title={`Referenced by ${pipelines.length} pipeline${pipelines.length === 1 ? '' : 's'}`}>
          {pipelines.join(', ')}. After deletion {reference(variable.key)} resolves to an empty value in those nodes.
        </Callout>
      ) : (
        <p>
          No node setting references {reference(variable.key)} directly. References nested inside structured settings are not detected, so check before deleting a variable you rely on.
        </p>
      )}
    </ConfirmDialog>
  )
}

export function VariablesPage() {
  const session = useSession()
  const toast = useToast()
  const list = useQuery({ queryKey: ['variables'], queryFn: variableApi.list })
  const [params] = useSearchParams()
  const [search, setSearch] = useState(() => params.get('q') ?? '')
  // The search palette opens this page with ?q= naming one item; follow it when already here.
  useEffect(() => {
    const q = params.get('q')
    if (q !== null) setSearch(q)
  }, [params])
  const [type, setType] = useState('')
  const [page, setPage] = useState(1)
  const [editing, setEditing] = useState<Variable | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Variable | null>(null)
  const all = useMemo(() => list.data ?? [], [list.data])
  const taken = useMemo(() => new Set(all.map((v) => v.key)), [all])
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return all.filter((v) => (!type || v.type === type) && (!q || `${v.key} ${v.description ?? ''}`.toLowerCase().includes(q)))
  }, [all, search, type])
  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const current = Math.min(page, pages)
  const rows = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE)
  const canEdit = session.can('variables.edit')

  const copy = async (key: string) => {
    try {
      await navigator.clipboard.writeText(reference(key))
      toast.success('Reference copied', reference(key))
    } catch (e) {
      toast.error('Could not copy', e)
    }
  }

  return (
    <Page>
      <PageHeader
        eyebrow="Workspace"
        title="Variables"
        description={`Reusable values and secrets. Reference one in any node setting as ${reference('key')}.`}
        actions={
          session.can('variables.create') && (
            <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setEditing('new')}>
              New variable
            </Button>
          )
        }
      />
      {list.isError ? (
        <Callout tone="danger" title="Variables could not be loaded" action={<Button size="sm" onClick={() => void list.refetch()}>Try again</Button>}>
          {errorMessage(list.error)}
        </Callout>
      ) : list.isPending ? (
        <div className="bk-table-wrap ws-loading">
          {Array.from({ length: 5 }, (_, i) => (
            <Skeleton key={i} height={22} />
          ))}
        </div>
      ) : !all.length ? (
        <div className="ws-empty">
          <EmptyState
            icon={<Braces size={20} aria-hidden="true" />}
            title="No variables yet"
            action={
              session.can('variables.create') && (
                <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setEditing('new')}>
                  Add a variable
                </Button>
              )
            }
          >
            Keep values that change between environments, and secrets such as API keys, out of pipeline definitions.
          </EmptyState>
        </div>
      ) : (
        <section className="ws-list">
          <div className="ws-toolbar">
            <SearchInput
              value={search}
              onChange={(v) => {
                setSearch(v)
                setPage(1)
              }}
              placeholder="Search by key or description"
              className="ws-search"
            />
            <Select
              aria-label="Filter by type"
              value={type}
              onChange={(e) => {
                setType(e.target.value)
                setPage(1)
              }}
              className="ws-select"
            >
              <option value="">All types</option>
              {TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
            <span className="ws-count">
              {filtered.length} of {all.length}
            </span>
          </div>
          {!filtered.length ? (
            <div className="bk-table-wrap">
              <EmptyState
                title="No variables match"
                action={
                  <Button
                    onClick={() => {
                      setSearch('')
                      setType('')
                    }}
                  >
                    Clear filters
                  </Button>
                }
              />
            </div>
          ) : (
            <>
              <div className="bk-table-wrap">
                <table className="bk-table ws-table">
                  <thead>
                    <tr>
                      <th scope="col">Variable</th>
                      <th scope="col">Type</th>
                      <th scope="col">Value</th>
                      <th scope="col">Updated</th>
                      <th scope="col">
                        <span className="bk-sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((v) => {
                      const meta = typeMeta(v.type)
                      return (
                        <tr key={v.key} className={canEdit ? 'is-clickable' : undefined} onClick={() => canEdit && setEditing(v)}>
                          <td>
                            <div className="ws-name">
                              <span className={`ws-type-icon ${v.type === 'secret' ? 'is-secret' : ''}`}>
                                {v.type === 'secret' ? <KeyRound size={16} aria-hidden="true" /> : <Braces size={16} aria-hidden="true" />}
                              </span>
                              <span>
                                <code>{v.key}</code>
                                <small>{v.description || 'No description'}</small>
                              </span>
                            </div>
                          </td>
                          <td>
                            <Badge tone={meta.tone}>{meta.label}</Badge>
                          </td>
                          <td className="bk-mono ws-value">
                            {v.type === 'secret' ? <span className="ws-muted">Encrypted, not shown</span> : v.value ? v.value : <span className="ws-muted">Empty</span>}
                          </td>
                          <td>{formatRelative(v.updated_at)}</td>
                          <td>
                            <div className="ws-actions" onClick={(e) => e.stopPropagation()}>
                              <IconButton size="sm" label={`Copy the reference to ${v.key}`} onClick={() => void copy(v.key)}>
                                <Copy size={15} aria-hidden="true" />
                              </IconButton>
                              {canEdit && (
                                <IconButton size="sm" label={`Edit ${v.key}`} onClick={() => setEditing(v)}>
                                  <PencilLine size={15} aria-hidden="true" />
                                </IconButton>
                              )}
                              {session.can('variables.delete') && (
                                <IconButton size="sm" variant="danger" label={`Delete ${v.key}`} onClick={() => setDeleting(v)}>
                                  <Trash2 size={15} aria-hidden="true" />
                                </IconButton>
                              )}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              <Pagination page={current} pageSize={PAGE_SIZE} total={filtered.length} onPage={setPage} label="variables" />
            </>
          )}
        </section>
      )}
      {editing && <VariableForm existing={editing === 'new' ? undefined : editing} taken={taken} onClose={() => setEditing(null)} />}
      {deleting && <DeleteVariable variable={deleting} onClose={() => setDeleting(null)} />}
    </Page>
  )
}

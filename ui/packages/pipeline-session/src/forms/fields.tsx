import { useEffect, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Code2, Plus, Trash2 } from 'lucide-react'
import { connectionApi, type Connection, type PipelineEdge, type PipelineNode } from '@brokoli/api'
import { Button, Callout, Checkbox, Field, IconButton, Input, Select, Textarea, errorMessage } from '@brokoli/ui'
import { CodeEditorModal, type CodeLanguage } from './CodeEditorModal'
import { TemplatePreview } from './TemplatePreview'
import type { DatasetSchema } from '../document'

export type FormCtx = {
  node: PipelineNode
  readonly: boolean
  get: (key: string) => unknown
  /** One merged patch per user action; a string key coalesces keystrokes into one undo step. */
  set: (patch: Record<string, unknown>, historyKey?: string | boolean) => void
}

export type TypeFormProps = { ctx: FormCtx; nodes: PipelineNode[]; edges: PipelineEdge[] }

export const str = (v: unknown) => (typeof v === 'string' ? v : v === undefined || v === null ? '' : String(v))

export function Section({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="ps-form-section">
      <header>
        <h4>{title}</h4>
        {description && <p>{description}</p>}
      </header>
      <div className="ps-form-fields">{children}</div>
    </section>
  )
}

export function TextField({
  ctx,
  name,
  label,
  placeholder,
  hint,
  required,
  mono,
  multiline,
  rows = 4,
  templatable,
}: {
  ctx: FormCtx
  name: string
  label: string
  placeholder?: string
  hint?: ReactNode
  required?: boolean
  mono?: boolean
  multiline?: boolean
  rows?: number
  /** Show a live preview and date-filter validation of the field's ${...} variables. */
  templatable?: boolean
}) {
  const value = str(ctx.get(name))
  const error = required && !value.trim() ? 'Required' : undefined
  const onChange = (v: string) => ctx.set({ [name]: v }, `field:${ctx.node.id}:${name}`)
  const field = (
    <Field label={label} hint={hint} error={error} required={required}>
      {multiline ? (
        <Textarea value={value} rows={rows} mono={mono} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <Input value={value} mono={mono} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      )}
    </Field>
  )
  if (!templatable) return field
  return (
    <div className="ps-templatable">
      {field}
      <TemplatePreview value={value} />
    </div>
  )
}

/*
 * Numbers are written as JSON numbers because Go reads them as float64; a
 * numeric string would be silently ignored. An empty field removes the key
 * so the server default applies, and the placeholder says what that is.
 */
export function NumberField({
  ctx,
  name,
  label,
  defaultValue,
  min,
  max,
  step = 1,
  hint,
}: {
  ctx: FormCtx
  name: string
  label: string
  defaultValue?: number | string
  min?: number
  max?: number
  step?: number
  hint?: ReactNode
}) {
  const stored = ctx.get(name)
  const [text, setText] = useState(typeof stored === 'number' ? String(stored) : '')
  useEffect(() => {
    setText(typeof stored === 'number' ? String(stored) : '')
  }, [stored])
  const n = Number(text)
  const error =
    text !== '' && !Number.isFinite(n)
      ? 'Enter a number'
      : min !== undefined && text !== '' && n < min
        ? `At least ${min}`
        : max !== undefined && text !== '' && n > max
          ? `At most ${max}`
          : typeof stored === 'string'
            ? `The saved value "${stored}" is text and is ignored by the server; enter a number.`
            : undefined
  return (
    <Field label={label} hint={hint} error={error}>
      <Input
        type="number"
        inputMode="decimal"
        value={text}
        min={min}
        max={max}
        step={step}
        placeholder={defaultValue !== undefined ? `Default: ${defaultValue}` : undefined}
        onChange={(e) => {
          const raw = e.target.value
          setText(raw)
          if (raw === '') ctx.set({ [name]: undefined }, `field:${ctx.node.id}:${name}`)
          else if (Number.isFinite(Number(raw))) ctx.set({ [name]: Number(raw) }, `field:${ctx.node.id}:${name}`)
        }}
      />
    </Field>
  )
}

export function SelectField({
  ctx,
  name,
  label,
  options,
  defaultLabel,
  hint,
  required,
}: {
  ctx: FormCtx
  name: string
  label: string
  options: (string | { value: string; label: string })[]
  /** Shown as the empty choice, naming what the server does when the key is absent. */
  defaultLabel?: string
  hint?: ReactNode
  required?: boolean
}) {
  const value = str(ctx.get(name))
  const opts = options.map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
  const unknown = value && !opts.some((o) => o.value === value)
  return (
    <Field label={label} hint={hint} required={required} error={unknown ? `"${value}" is not a value this editor knows; the server may reject it.` : undefined}>
      <Select value={value} onChange={(e) => ctx.set({ [name]: e.target.value })}>
        <option value="">{defaultLabel ? `Default: ${defaultLabel}` : 'Choose...'}</option>
        {unknown && <option value={value}>{value}</option>}
        {opts.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
    </Field>
  )
}

export function CheckField({ ctx, name, label, description }: { ctx: FormCtx; name: string; label: string; description?: ReactNode }) {
  return <Checkbox label={label} description={description} checked={Boolean(ctx.get(name))} onChange={(e) => ctx.set({ [name]: e.target.checked || undefined })} />
}

/** Comma-separated list stored as string[]. */
export function ListField({ ctx, name, label, placeholder, hint, required }: { ctx: FormCtx; name: string; label: string; placeholder?: string; hint?: ReactNode; required?: boolean }) {
  const stored = ctx.get(name)
  const joined = Array.isArray(stored) ? stored.join(', ') : str(stored)
  const [text, setText] = useState(joined)
  useEffect(() => setText((t) => (split(t).join(', ') === joined ? t : joined)), [joined])
  return (
    <Field label={label} hint={hint ?? 'Separate names with commas.'} required={required} error={required && !split(text).length ? 'Required' : undefined}>
      <Input
        value={text}
        mono
        placeholder={placeholder}
        onChange={(e) => {
          setText(e.target.value)
          const list = split(e.target.value)
          ctx.set({ [name]: list.length ? list : undefined }, `field:${ctx.node.id}:${name}`)
        }}
      />
    </Field>
  )
}

export function split(text: string) {
  return text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

type Row = { id: number; key: string; value: string }
let rowSeq = 0

/** Editor for a string-to-string map, e.g. HTTP headers. Rows are local so renaming a key never merges rows mid-edit. */
export function MapEditor({
  value,
  onChange,
  keyPlaceholder = 'name',
  valuePlaceholder = 'value',
  addLabel = 'Add',
}: {
  value: Record<string, unknown> | undefined
  onChange: (next: Record<string, string> | undefined) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
  addLabel?: string
}) {
  const [rows, setRows] = useState<Row[]>(() => Object.entries(value ?? {}).map(([key, v]) => ({ id: ++rowSeq, key, value: str(v) })))
  const commit = (next: Row[]) => {
    setRows(next)
    const entries = next.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value] as const)
    onChange(entries.length ? Object.fromEntries(entries) : undefined)
  }
  const keys = rows.map((r) => r.key.trim()).filter(Boolean)
  const duplicate = keys.find((k, i) => keys.indexOf(k) !== i)
  return (
    <div className="ps-map">
      {rows.map((r) => (
        <div key={r.id} className="ps-map-row">
          <Input mono value={r.key} placeholder={keyPlaceholder} aria-label="Name" onChange={(e) => commit(rows.map((x) => (x.id === r.id ? { ...x, key: e.target.value } : x)))} />
          <Input value={r.value} placeholder={valuePlaceholder} aria-label={`Value for ${r.key || 'entry'}`} onChange={(e) => commit(rows.map((x) => (x.id === r.id ? { ...x, value: e.target.value } : x)))} />
          <IconButton size="sm" variant="danger" label="Remove" onClick={() => commit(rows.filter((x) => x.id !== r.id))}>
            <Trash2 size={14} aria-hidden="true" />
          </IconButton>
        </div>
      ))}
      <Button size="sm" variant="ghost" icon={<Plus size={14} aria-hidden="true" />} onClick={() => setRows([...rows, { id: ++rowSeq, key: '', value: '' }])}>
        {addLabel}
      </Button>
      {duplicate && <p className="ps-form-warning">"{duplicate}" appears twice; only the last one is kept.</p>}
    </div>
  )
}

export function MapField({ ctx, name, label, hint, ...rest }: { ctx: FormCtx; name: string; label: string; hint?: ReactNode; keyPlaceholder?: string; valuePlaceholder?: string; addLabel?: string }) {
  const value = ctx.get(name)
  return (
    <div className="bk-field">
      <div className="bk-field-label">
        <label>{label}</label>
      </div>
      <MapEditor key={ctx.node.id} value={value && typeof value === 'object' ? (value as Record<string, unknown>) : undefined} onChange={(v) => ctx.set({ [name]: v })} {...rest} />
      {hint && <small className="bk-field-hint">{hint}</small>}
    </div>
  )
}

type SchemaRow = { id: number; name: string; kind: string; nullable: boolean; precision: string; scale: string }
const SCHEMA_TYPES = ['int64', 'float64', 'string', 'boolean', 'bytes', 'decimal', 'date', 'timestamp', 'duration', 'json', 'unknown']
let schemaRowSeq = 0

function schemaRows(value: DatasetSchema | undefined): SchemaRow[] {
  const columns = Array.isArray(value?.columns) ? value.columns : []
  return columns.map((column) => ({
    id: ++schemaRowSeq,
    name: column.name,
    kind: typeof column.type?.kind === 'string' ? column.type.kind : 'unknown',
    nullable: Boolean(column.type?.nullable),
    precision: typeof column.type?.precision === 'number' ? String(column.type.precision) : '',
    scale: typeof column.type?.scale === 'number' ? String(column.type.scale) : '',
  }))
}

/** Authoring editor for the portable dataset-schema/v1 source declaration. */
export function DatasetSchemaField({ ctx, name = 'schema', label = 'Output schema' }: { ctx: FormCtx; name?: string; label?: string }) {
  const stored = ctx.get(name)
  const value = stored && typeof stored === 'object' && !Array.isArray(stored) ? (stored as DatasetSchema) : undefined
  const [rows, setRows] = useState<SchemaRow[]>(() => schemaRows(value))
  const [additional, setAdditional] = useState(value?.additional_columns ?? 'unknown')
  useEffect(() => {
    setRows(schemaRows(value))
    setAdditional(value?.additional_columns ?? 'unknown')
    // Schema rows are local while editing; reset when the selected node changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx.node.id])

  const commit = (nextRows: SchemaRow[], nextAdditional = additional) => {
    setRows(nextRows)
    setAdditional(nextAdditional)
    if (!nextRows.length) {
      ctx.set({ [name]: undefined }, `schema:${ctx.node.id}`)
      return
    }
    ctx.set(
      {
        [name]: {
          contract: 'brokoli.dataset-schema/v1',
          columns: nextRows.filter((row) => row.name.trim()).map((row) => ({
            name: row.name.trim(),
            type: {
              kind: row.kind,
              ...(row.kind === 'decimal' && row.precision ? { precision: Number(row.precision) } : {}),
              ...(row.kind === 'decimal' && row.scale ? { scale: Number(row.scale) } : {}),
              ...(row.nullable ? { nullable: true } : {}),
            },
          })),
          additional_columns: nextAdditional,
        },
      },
      `schema:${ctx.node.id}`,
    )
  }

  const names = rows.map((row) => row.name.trim()).filter(Boolean)
  const duplicate = names.find((entry, index) => names.indexOf(entry) !== index)
  return (
    <div className="bk-field ps-schema-editor">
      <div className="bk-field-label">
        <label>{label}</label>
      </div>
      <p className="bk-field-hint">Declare columns when the source shape is known. This enables field completion and schema validation downstream.</p>
      <div className="ps-schema-editor-rows">
        {rows.map((row) => (
          <div key={row.id} className="ps-schema-editor-row">
            <Input mono value={row.name} placeholder="column" aria-label="Column name" onChange={(event) => commit(rows.map((entry) => (entry.id === row.id ? { ...entry, name: event.target.value } : entry)))} />
            <Select aria-label={`Type for ${row.name || 'column'}`} value={row.kind} onChange={(event) => commit(rows.map((entry) => (entry.id === row.id ? { ...entry, kind: event.target.value, precision: event.target.value === 'decimal' ? entry.precision : '', scale: event.target.value === 'decimal' ? entry.scale : '' } : entry)))}>
              {SCHEMA_TYPES.map((type) => <option key={type} value={type}>{type}</option>)}
            </Select>
            {row.kind === 'decimal' && <Input type="number" min={1} value={row.precision} placeholder="precision" aria-label={`Precision for ${row.name || 'column'}`} onChange={(event) => commit(rows.map((entry) => (entry.id === row.id ? { ...entry, precision: event.target.value } : entry)))} />}
            {row.kind === 'decimal' && <Input type="number" min={0} value={row.scale} placeholder="scale" aria-label={`Scale for ${row.name || 'column'}`} onChange={(event) => commit(rows.map((entry) => (entry.id === row.id ? { ...entry, scale: event.target.value } : entry)))} />}
            <Checkbox label="Nullable" checked={row.nullable} onChange={(event) => commit(rows.map((entry) => (entry.id === row.id ? { ...entry, nullable: event.target.checked } : entry)))} />
            <IconButton size="sm" variant="danger" label={`Remove ${row.name || 'column'}`} onClick={() => commit(rows.filter((entry) => entry.id !== row.id))}><Trash2 size={14} aria-hidden="true" /></IconButton>
          </div>
        ))}
      </div>
      <Button size="sm" variant="ghost" icon={<Plus size={14} aria-hidden="true" />} onClick={() => setRows([...rows, { id: ++schemaRowSeq, name: '', kind: 'string', nullable: false, precision: '', scale: '' }])}>Add column</Button>
      <Field label="Additional columns" hint="Open accepts undeclared columns; unknown keeps the declaration non-authoritative.">
        <Select value={additional} onChange={(event) => commit(rows, event.target.value)}>
          <option value="closed">Closed</option>
          <option value="open">Open</option>
          <option value="unknown">Unknown</option>
        </Select>
      </Field>
      {duplicate && <p className="ps-form-warning">"{duplicate}" appears twice; downstream field validation cannot distinguish those columns.</p>}
    </div>
  )
}

export function ScriptField({ ctx, name, label, language, required, hint }: { ctx: FormCtx; name: string; label: string; language: CodeLanguage; required?: boolean; hint?: ReactNode }) {
  const [open, setOpen] = useState(false)
  const value = str(ctx.get(name))
  const lines = value.split('\n')
  return (
    <div className="bk-field">
      <div className="bk-field-label">
        <label>
          {label}
          {required && <span className="bk-field-required">*</span>}
        </label>
        <Button size="sm" variant="ghost" icon={<Code2 size={14} aria-hidden="true" />} onClick={() => setOpen(true)} disabled={false}>
          {ctx.readonly ? 'View' : value ? 'Edit' : 'Write'}
        </Button>
      </div>
      <button type="button" className="ps-script-preview" onClick={() => setOpen(true)} aria-label={`Open ${label} in the editor`}>
        {value ? (
          <pre>
            {lines.slice(0, 8).join('\n')}
            {lines.length > 8 ? `\n... ${lines.length - 8} more lines` : ''}
          </pre>
        ) : (
          <span>Nothing written yet. Click to open the editor.</span>
        )}
      </button>
      {required && !value.trim() && <small className="bk-field-error">Required</small>}
      {hint && <small className="bk-field-hint">{hint}</small>}
      {open && (
        <CodeEditorModal
          title={`${label}: ${ctx.node.name}`}
          language={language}
          value={value}
          readonly={ctx.readonly}
          onClose={() => setOpen(false)}
          onSave={(text) => {
            ctx.set({ [name]: text })
            setOpen(false)
          }}
        />
      )}
    </div>
  )
}

export function useConnections() {
  return useQuery({ queryKey: ['connections'], queryFn: connectionApi.list, staleTime: 30_000 })
}

/*
 * Picks a saved connection. Only types that actually work for the node are
 * offered (for databases, the ones the engine can build a URI for). A
 * stored id that no longer exists is shown as missing rather than quietly
 * rendered as "manual".
 */
export function ConnectionField({
  ctx,
  idKey = 'conn_id',
  uriKey,
  types,
  label = 'Connection',
  noneLabel,
  hint,
}: {
  ctx: FormCtx
  idKey?: string
  uriKey?: string
  types: string[]
  label?: string
  noneLabel: string
  hint?: ReactNode
}) {
  const connections = useConnections()
  const value = str(ctx.get(idKey))
  const usable = (connections.data ?? []).filter((c: Connection) => types.includes(c.type))
  const missing = value && connections.isSuccess && !usable.some((c) => c.conn_id === value)
  return (
    <Field
      label={label}
      hint={
        connections.isError
          ? `Connections could not be loaded: ${errorMessage(connections.error)}`
          : hint ?? (connections.isSuccess && !usable.length ? `No saved ${types.join(' or ')} connections yet.` : undefined)
      }
      error={missing ? `The connection "${value}" does not exist or is not a supported type.` : undefined}
    >
      <Select
        value={value}
        onChange={(e) => {
          const next = e.target.value
          ctx.set({ [idKey]: next, ...(uriKey && next ? { [uriKey]: undefined } : {}) })
        }}
      >
        <option value="">{noneLabel}</option>
        {missing && <option value={value}>{value} (not found)</option>}
        {usable.map((c) => (
          <option key={c.conn_id} value={c.conn_id}>
            {c.conn_id} · {c.type}
            {c.description ? ` · ${c.description}` : ''}
          </option>
        ))}
      </Select>
    </Field>
  )
}

export function TestConnection({ connId, uri }: { connId?: string; uri?: string }) {
  const [state, setState] = useState<{ busy: boolean; ok?: boolean; message?: string }>({ busy: false })
  const run = async () => {
    setState({ busy: true })
    try {
      const result = connId ? await connectionApi.test(connId) : await connectionApi.testUri(uri ?? '')
      setState({
        busy: false,
        ok: result.success,
        message: result.success ? `Connected${result.driver ? ` with the ${result.driver} driver` : ''}${result.message ? `: ${result.message}` : ''}` : result.error || result.message || 'The server reported a failure without details.',
      })
    } catch (e) {
      setState({ busy: false, ok: false, message: errorMessage(e) })
    }
  }
  return (
    <div className="ps-test">
      <Button size="sm" onClick={run} loading={state.busy} disabled={!connId && !uri}>
        Test connection
      </Button>
      {state.message && <Callout tone={state.ok ? 'success' : 'danger'}>{state.message}</Callout>}
    </div>
  )
}

export function LegacyKeyNotice({ ctx, name, children, fix }: { ctx: FormCtx; name: string; children: ReactNode; fix?: { label: string; patch: Record<string, unknown> } }) {
  if (ctx.get(name) === undefined) return null
  return (
    <Callout
      tone="warning"
      title={`"${name}" has no effect`}
      action={
        !ctx.readonly && (
          <Button size="sm" onClick={() => ctx.set(fix?.patch ?? { [name]: undefined })}>
            {fix?.label ?? 'Remove it'}
          </Button>
        )
      }
    >
      {children}
    </Callout>
  )
}

import { useState } from 'react'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { Button, Checkbox, Field, IconButton, Input, Select } from '@brokoli/ui'
import { MapEditor, split, str, type FormCtx } from './fields'
import type { DatasetSchema, DatasetSchemaColumn } from '../document'

type Rule = Record<string, unknown> & { type: string }

const TYPES: [string, string][] = [
  ['rename_columns', 'Rename columns'],
  ['filter_rows', 'Filter rows'],
  ['drop_columns', 'Drop columns'],
  ['apply_function', 'Apply a function'],
  ['add_column', 'Add a column'],
  ['replace_values', 'Replace values'],
  ['sort', 'Sort'],
  ['deduplicate', 'Remove duplicates'],
  ['aggregate', 'Aggregate'],
]

/** Short names the engine also accepts (engine/transform.go). */
const ALIASES: Record<string, string> = {
  rename: 'rename_columns',
  filter: 'filter_rows',
  drop: 'drop_columns',
  function: 'apply_function',
  replace: 'replace_values',
  dedup: 'deduplicate',
  agg: 'aggregate',
}

const DEFAULTS: Record<string, () => Rule> = {
  rename_columns: () => ({ type: 'rename_columns', mapping: {} }),
  filter_rows: () => ({ type: 'filter_rows', condition: '' }),
  drop_columns: () => ({ type: 'drop_columns', columns: [] }),
  apply_function: () => ({ type: 'apply_function', column: '', function: 'lower' }),
  add_column: () => ({ type: 'add_column', name: '', expression: '' }),
  replace_values: () => ({ type: 'replace_values', column: '', mapping: {} }),
  // The engine's sort defaults to descending when `ascending` is absent, so it is always written.
  sort: () => ({ type: 'sort', columns: [], ascending: true }),
  deduplicate: () => ({ type: 'deduplicate', columns: [] }),
  aggregate: () => ({
    type: 'aggregate',
    group_by: [],
    agg_fields: [{ column: '', function: 'count', alias: 'count' }],
  }),
}

function knownColumns(schema: DatasetSchema | undefined) {
  return schema?.columns.map((column) => column.name) ?? []
}

function missingColumns(columns: string[], schema: DatasetSchema | undefined) {
  if (!schema) return []
  const known = new Set(knownColumns(schema))
  return columns.filter((column) => !known.has(column))
}

function ColumnsInput({
  value,
  onChange,
  schema,
  label = 'Columns',
  placeholder = 'id, email',
}: {
  value: unknown
  onChange: (cols: string[]) => void
  schema?: DatasetSchema
  label?: string
  placeholder?: string
}) {
  const joined = Array.isArray(value) ? value.join(', ') : ''
  const [text, setText] = useState(joined)
  const missing = missingColumns(split(text), schema)
  return (
    <>
      <Field
        label={label}
        hint={missing.length ? undefined : 'Separate column names with commas.'}
        error={
          missing.length
            ? `Unknown column${missing.length > 1 ? 's' : ''}: ${missing.join(', ')}`
            : undefined
        }
      >
        <Input
          mono
          value={text}
          placeholder={placeholder}
          list={schema ? `${label.replaceAll(' ', '-').toLowerCase()}-columns` : undefined}
          onChange={(e) => {
            setText(e.target.value)
            onChange(split(e.target.value))
          }}
        />
      </Field>
      {schema && (
        <datalist id={`${label.replaceAll(' ', '-').toLowerCase()}-columns`}>
          {knownColumns(schema).map((column) => (
            <option key={column} value={column} />
          ))}
        </datalist>
      )}
    </>
  )
}

function ColumnField({
  value,
  onChange,
  schema,
  label = 'Column',
}: {
  value: unknown
  onChange: (value: string) => void
  schema?: DatasetSchema
  label?: string
}) {
  const column = str(value)
  const missing = schema && column && !knownColumns(schema).includes(column)
  const list = `${label.replaceAll(' ', '-').toLowerCase()}-columns`
  return (
    <>
      <Field label={label} error={missing ? `Unknown column: ${column}` : undefined}>
        <Input
          mono
          value={column}
          list={schema ? list : undefined}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>
      {schema && (
        <datalist id={list}>
          {knownColumns(schema).map((name) => (
            <option key={name} value={name} />
          ))}
        </datalist>
      )}
    </>
  )
}

function expressionReferences(expression: string, schema: DatasetSchema | undefined) {
  if (!schema || !/[+*/<>=()-]/.test(expression)) return []
  const known = new Set(knownColumns(schema))
  const ignored = new Set(['and', 'or', 'not', 'in', 'true', 'false', 'null'])
  return [...new Set(expression.match(/[A-Za-z_][A-Za-z0-9_.]*/g) ?? [])].filter(
    (token) => !ignored.has(token.toLowerCase()) && !known.has(token),
  )
}

export function expressionType(
  expression: string,
  schema: DatasetSchema | undefined,
): DatasetSchemaColumn['type'] | undefined {
  const value = expression.trim()
  if (!value) return undefined
  const direct = schema?.columns.find((column) => column.name === value)?.type
  if (direct) return direct
  if (/^(true|false)$/i.test(value)) return { kind: 'boolean' }
  if (/^-?\d+$/.test(value)) return { kind: 'int64' }
  if (/^-?(?:\d+\.\d*|\d*\.\d+)$/.test(value)) return { kind: 'float64' }
  if (/^(['"]).*\1$/.test(value)) return { kind: 'string' }
  if (/[+*/-]/.test(value)) {
    const descriptors = (value.match(/[A-Za-z_][A-Za-z0-9_.]*/g) ?? []).map(
      (name) => schema?.columns.find((column) => column.name === name)?.type,
    )
    const types = descriptors.map((type) => type?.kind)
    if (types.length && types.every((type) => type === 'decimal') && !value.includes('/'))
      return descriptors[0]
    if (types.includes('float64') || value.includes('/')) return { kind: 'float64' }
    if (types.length && types.every((type) => type === 'int64')) return { kind: 'int64' }
  }
  return { kind: 'unknown' }
}

function typeLabel(type: DatasetSchemaColumn['type'] | undefined) {
  if (!type) return ''
  if (type.kind !== 'decimal') return type.kind ?? 'unknown'
  const size = type.precision
    ? `(${type.precision}${type.scale !== undefined ? `, ${type.scale}` : ''})`
    : ''
  return `decimal${size}`
}

function RuleBody({
  rule,
  update,
  schema,
}: {
  rule: Rule
  update: (patch: Record<string, unknown>) => void
  schema?: DatasetSchema
}) {
  const type = ALIASES[rule.type] ?? rule.type
  switch (type) {
    case 'rename_columns':
      return (
        <MapEditor
          value={rule.mapping as Record<string, unknown>}
          onChange={(m) => update({ mapping: m ?? {} })}
          keyPlaceholder="current name"
          valuePlaceholder="new name"
          addLabel="Add a column"
        />
      )
    case 'replace_values':
      return (
        <>
          <ColumnField
            value={rule.column}
            schema={schema}
            onChange={(column) => update({ column })}
          />
          <MapEditor
            value={rule.mapping as Record<string, unknown>}
            onChange={(m) => update({ mapping: m ?? {} })}
            keyPlaceholder="value"
            valuePlaceholder="replacement"
            addLabel="Add a value"
          />
        </>
      )
    case 'filter_rows':
      return (
        <Field
          label="Keep rows where"
          hint={
            expressionReferences(str(rule.condition), schema).length
              ? undefined
              : 'Forms: status = active, amount >= 100, country in [PT, BR]. Numbers compare numerically, anything else as text.'
          }
          error={
            expressionReferences(str(rule.condition), schema).length
              ? `Unknown columns: ${expressionReferences(str(rule.condition), schema).join(', ')}`
              : undefined
          }
        >
          <Input
            mono
            value={str(rule.condition)}
            placeholder="amount >= 100"
            onChange={(e) => update({ condition: e.target.value })}
          />
        </Field>
      )
    case 'drop_columns':
    case 'deduplicate':
      return (
        <ColumnsInput
          value={rule.columns}
          schema={schema}
          onChange={(columns) => update({ columns })}
          label={type === 'deduplicate' ? 'Compare on columns' : 'Columns to drop'}
        />
      )
    case 'sort':
      return (
        <>
          <ColumnsInput
            value={rule.columns}
            schema={schema}
            onChange={(columns) => update({ columns })}
            label="Sort by"
          />
          <Checkbox
            label="Ascending"
            description="Unchecked sorts from highest to lowest."
            checked={rule.ascending === true}
            onChange={(e) => update({ ascending: e.target.checked })}
          />
        </>
      )
    case 'apply_function':
      return (
        <div className="ps-form-row">
          <Field label="Column">
            <ColumnField
              value={rule.column}
              schema={schema}
              onChange={(column) => update({ column })}
            />
          </Field>
          <Field label="Function">
            <Select
              value={str(rule.function)}
              onChange={(e) => update({ function: e.target.value })}
            >
              <option value="lower">lowercase</option>
              <option value="upper">UPPERCASE</option>
              <option value="trim">trim spaces</option>
              <option value="title">Title Case</option>
            </Select>
          </Field>
        </div>
      )
    case 'add_column':
      return (
        <>
          <Field label="New column">
            <Input mono value={str(rule.name)} onChange={(e) => update({ name: e.target.value })} />
          </Field>
          <Field
            label="Expression"
            hint={
              expressionReferences(str(rule.expression), schema).length
                ? undefined
                : `Inferred type: ${typeLabel(expressionType(str(rule.expression), schema)) || 'unknown'}. Use fields from the upstream schema in arithmetic expressions.`
            }
            error={
              expressionReferences(str(rule.expression), schema).length
                ? `Unknown columns: ${expressionReferences(str(rule.expression), schema).join(', ')}`
                : undefined
            }
          >
            <Input
              mono
              value={str(rule.expression)}
              onChange={(e) => update({ expression: e.target.value })}
            />
          </Field>
        </>
      )
    case 'aggregate': {
      const fields =
        ((rule.agg_fields ?? rule.aggregations) as
          { column?: string; function?: string; alias?: string }[] | undefined) ?? []
      const setFields = (next: typeof fields) =>
        update({ agg_fields: next, aggregations: undefined })
      const missing = schema
        ? fields.map((field) => str(field.column)).filter((column) => column && !knownColumns(schema).includes(column))
        : []
      return (
        <>
          <ColumnsInput
            value={rule.group_by}
            schema={schema}
            onChange={(group_by) => update({ group_by })}
            label="Group by"
            placeholder="country"
          />
          <div className="ps-agg">
            {fields.map((f, i) => (
              <div key={i} className="ps-agg-row">
                <Select
                  aria-label="Function"
                  value={f.function ?? 'count'}
                  onChange={(e) =>
                    setFields(
                      fields.map((x, j) => (j === i ? { ...x, function: e.target.value } : x)),
                    )
                  }
                >
                  {['count', 'sum', 'avg', 'min', 'max'].map((fn) => (
                    <option key={fn} value={fn}>
                      {fn}
                    </option>
                  ))}
                </Select>
                <Input
                  mono
                  aria-label="Column"
                  aria-invalid={missing.includes(f.column ?? '') || undefined}
                  placeholder="column"
                  list={schema ? 'aggregate-columns' : undefined}
                  value={f.column ?? ''}
                  onChange={(e) =>
                    setFields(
                      fields.map((x, j) => (j === i ? { ...x, column: e.target.value } : x)),
                    )
                  }
                />
                <Input
                  mono
                  aria-label="Output name"
                  placeholder="output name"
                  value={f.alias ?? ''}
                  onChange={(e) =>
                    setFields(fields.map((x, j) => (j === i ? { ...x, alias: e.target.value } : x)))
                  }
                />
                <IconButton
                  size="sm"
                  variant="danger"
                  label="Remove aggregation"
                  onClick={() => setFields(fields.filter((_, j) => j !== i))}
                >
                  <Trash2 size={14} aria-hidden="true" />
                </IconButton>
              </div>
            ))}
            {schema && (
              <datalist id="aggregate-columns">
                {knownColumns(schema).map((column) => (
                  <option key={column} value={column} />
                ))}
              </datalist>
            )}
            {missing.length > 0 && (
              <p className="ps-form-warning">
                Unknown aggregate column{missing.length > 1 ? 's' : ''}:{' '}
                {[...new Set(missing)].join(', ')}
              </p>
            )}
            <Button
              size="sm"
              variant="ghost"
              icon={<Plus size={14} aria-hidden="true" />}
              onClick={() => setFields([...fields, { column: '', function: 'sum', alias: '' }])}
            >
              Add aggregation
            </Button>
          </div>
        </>
      )
    }
    default:
      return (
        <p className="ps-form-warning">
          The engine does not know the rule type "{rule.type}"; this rule fails the run as it
          stands.
        </p>
      )
  }
}

export function TransformRules({
  ctx,
  inputSchema,
}: {
  ctx: FormCtx
  inputSchema?: DatasetSchema
}) {
  const rules = (Array.isArray(ctx.get('rules')) ? (ctx.get('rules') as Rule[]) : []) ?? []
  const write = (next: Rule[]) => ctx.set({ rules: next }, `rules:${ctx.node.id}`)
  const move = (i: number, by: number) => {
    const next = [...rules]
    const [r] = next.splice(i, 1)
    next.splice(i + by, 0, r)
    ctx.set({ rules: next })
  }
  return (
    <div className="ps-rules">
      {rules.length === 0 && (
        <p className="ps-form-note">
          No rules yet. Rules run top to bottom; each one receives the output of the previous.
        </p>
      )}
      {rules.map((rule, i) => (
        <div key={i} className="ps-rule">
          <div className="ps-rule-head">
            <span className="ps-rule-index">{i + 1}</span>
            <Select
              aria-label="Rule type"
              value={ALIASES[rule.type] ?? rule.type}
              onChange={(e) =>
                ctx.set({ rules: rules.map((r, j) => (j === i ? DEFAULTS[e.target.value]() : r)) })
              }
            >
              {!TYPES.some(([t]) => t === (ALIASES[rule.type] ?? rule.type)) && (
                <option value={rule.type}>{rule.type}</option>
              )}
              {TYPES.map(([t, label]) => (
                <option key={t} value={t}>
                  {label}
                </option>
              ))}
            </Select>
            <IconButton size="sm" label="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
              <ArrowUp size={14} aria-hidden="true" />
            </IconButton>
            <IconButton
              size="sm"
              label="Move down"
              disabled={i === rules.length - 1}
              onClick={() => move(i, 1)}
            >
              <ArrowDown size={14} aria-hidden="true" />
            </IconButton>
            <IconButton
              size="sm"
              variant="danger"
              label="Remove rule"
              onClick={() => ctx.set({ rules: rules.filter((_, j) => j !== i) })}
            >
              <Trash2 size={14} aria-hidden="true" />
            </IconButton>
          </div>
          <div className="ps-rule-body">
            <RuleBody
              schema={inputSchema}
              rule={rule}
              update={(patch) =>
                write(
                  rules.map((r, j) =>
                    j === i
                      ? (Object.fromEntries(
                          Object.entries({ ...r, ...patch }).filter(([, v]) => v !== undefined),
                        ) as Rule)
                      : r,
                  ),
                )
              }
            />
          </div>
        </div>
      ))}
      <Button
        size="sm"
        icon={<Plus size={14} aria-hidden="true" />}
        onClick={() => ctx.set({ rules: [...rules, DEFAULTS.rename_columns()] })}
      >
        Add rule
      </Button>
    </div>
  )
}

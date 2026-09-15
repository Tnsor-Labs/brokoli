import { Plus, Trash2 } from 'lucide-react'
import { Button, Field, IconButton, Input, Select } from '@brokoli/ui'
import { str, type FormCtx } from './fields'

type Check = { column?: string; rule: string; params?: Record<string, unknown>; on_failure?: string }

/*
 * Every rule the quality package implements (quality/rules.go), including
 * the three the Svelte form did not offer. Parameters are written as
 * strings: the engine parses numbers from strings, and `freshness.max_hours`
 * is only honoured as a string.
 */
const RULES: { value: string; label: string; params: { key: string; label: string; placeholder?: string; options?: string[] }[]; noColumn?: boolean }[] = [
  { value: 'not_null', label: 'Not null', params: [] },
  { value: 'no_blank', label: 'Not blank', params: [] },
  { value: 'unique', label: 'Unique', params: [] },
  { value: 'min', label: 'Minimum', params: [{ key: 'min', label: 'Minimum', placeholder: '0' }] },
  { value: 'max', label: 'Maximum', params: [{ key: 'max', label: 'Maximum', placeholder: '100' }] },
  {
    value: 'range',
    label: 'Within a range',
    params: [
      { key: 'min', label: 'From', placeholder: 'no lower bound' },
      { key: 'max', label: 'To', placeholder: 'no upper bound' },
    ],
  },
  { value: 'regex', label: 'Matches a pattern', params: [{ key: 'pattern', label: 'Pattern (RE2)', placeholder: '^[A-Z]{2}$' }] },
  { value: 'type_check', label: 'Has a type', params: [{ key: 'expected_type', label: 'Type', options: ['int', 'float', 'number', 'date', 'email'] }] },
  { value: 'freshness', label: 'Fresh', params: [{ key: 'max_hours', label: 'Newer than (hours)', placeholder: '24' }] },
  {
    value: 'row_count',
    label: 'Row count',
    noColumn: true,
    params: [
      { key: 'min', label: 'At least', placeholder: 'no minimum' },
      { key: 'max', label: 'At most', placeholder: 'no maximum' },
    ],
  },
]

export function QualityRules({ ctx }: { ctx: FormCtx }) {
  const source = ctx.get('rules') ?? ctx.get('checks')
  const checks = (Array.isArray(source) ? source : []) as Check[]
  // Writes always go to `rules`; the `checks` alias is folded in on the first edit.
  const write = (next: Check[], key?: string) => ctx.set({ rules: next, checks: undefined }, key ?? true)
  const update = (i: number, patch: Partial<Check>, key?: string) => write(checks.map((c, j) => (j === i ? { ...c, ...patch } : c)), key)
  return (
    <div className="ps-rules">
      {checks.length === 0 && <p className="ps-form-note">No checks yet. A blocking check that fails stops the run; a warning is recorded and the data passes through.</p>}
      {checks.map((check, i) => {
        const def = RULES.find((r) => r.value === check.rule)
        return (
          <div key={i} className="ps-rule">
            <div className="ps-rule-head">
              <span className="ps-rule-index">{i + 1}</span>
              <Select aria-label="Check" value={check.rule} onChange={(e) => update(i, { rule: e.target.value, params: {} })}>
                {!def && <option value={check.rule}>{check.rule}</option>}
                {RULES.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </Select>
              <IconButton size="sm" variant="danger" label="Remove check" onClick={() => write(checks.filter((_, j) => j !== i))}>
                <Trash2 size={14} aria-hidden="true" />
              </IconButton>
            </div>
            <div className="ps-rule-body">
              {!def?.noColumn && (
                <Field label="Column">
                  <Input mono value={str(check.column)} onChange={(e) => update(i, { column: e.target.value }, `q:${ctx.node.id}:${i}:col`)} />
                </Field>
              )}
              {def?.params.map((p) => (
                <Field key={p.key} label={p.label}>
                  {p.options ? (
                    <Select value={str(check.params?.[p.key]) || 'int'} onChange={(e) => update(i, { params: { ...check.params, [p.key]: e.target.value } })}>
                      {p.options.map((o) => (
                        <option key={o} value={o}>
                          {o}
                        </option>
                      ))}
                    </Select>
                  ) : (
                    <Input
                      mono
                      value={str(check.params?.[p.key])}
                      placeholder={p.placeholder}
                      onChange={(e) => {
                        const params = { ...check.params, [p.key]: e.target.value }
                        if (!e.target.value) delete params[p.key]
                        update(i, { params }, `q:${ctx.node.id}:${i}:${p.key}`)
                      }}
                    />
                  )}
                </Field>
              ))}
              <Field label="When it fails">
                <Select
                  value={str(check.on_failure)}
                  onChange={(e) => {
                    const next: Check = { ...check, on_failure: e.target.value }
                    if (!e.target.value) delete next.on_failure
                    write(checks.map((c, j) => (j === i ? next : c)))
                  }}
                >
                  <option value="">Node default ({str(ctx.get('on_failure')) || 'warn'})</option>
                  <option value="block">Block the run</option>
                  <option value="warn">Warn and continue</option>
                </Select>
              </Field>
            </div>
          </div>
        )
      })}
      <Button size="sm" icon={<Plus size={14} aria-hidden="true" />} onClick={() => write([...checks, { column: '', rule: 'not_null', params: {} }])}>
        Add check
      </Button>
    </div>
  )
}

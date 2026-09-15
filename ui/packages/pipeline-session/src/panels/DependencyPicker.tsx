import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { pipelineApi, type DependencyMode, type DependencyRule, type DependencyState } from '@brokoli/api'
import { Badge, Button, Field, IconButton, Input, Select, errorMessage } from '@brokoli/ui'

const STATES: { value: DependencyState; label: string }[] = [
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'completed', label: 'Finished (any outcome)' },
  { value: 'failed', label: 'Failed' },
]
const MODES: { value: DependencyMode; label: string }[] = [
  { value: 'gate', label: 'Gate: wait for it' },
  { value: 'trigger', label: 'Trigger: start when it does' },
]

/*
 * Upstream dependencies. Structured rules come first; legacy `depends_on`
 * ids without a rule are shown too (and can be converted), so nothing
 * stored is hidden. The same upstream cannot be added twice.
 */
export function DependencyPicker({
  pipelineId,
  rules,
  legacy,
  readonly,
  onChange,
}: {
  pipelineId: string
  rules: DependencyRule[]
  legacy: string[]
  readonly: boolean
  onChange: (rules: DependencyRule[], legacy: string[]) => void
}) {
  const list = useQuery({ queryKey: ['pipelines', 'list'], queryFn: pipelineApi.list, staleTime: 30_000 })
  const [adding, setAdding] = useState<DependencyRule | null>(null)
  const names = new Map((list.data ?? []).map((p) => [p.id, p.name]))
  const used = new Set([...rules.map((r) => r.pipeline_id), ...legacy])
  const available = (list.data ?? []).filter((p) => p.id !== pipelineId && !used.has(p.id))
  const legacyOnly = legacy.filter((id) => !rules.some((r) => r.pipeline_id === id))
  const setRule = (i: number, patch: Partial<DependencyRule>) =>
    onChange(
      rules.map((r, j) => {
        if (j !== i) return r
        const next: DependencyRule = { ...r, ...patch }
        if (next.within_seconds === undefined) delete next.within_seconds
        return next
      }),
      legacy,
    )
  const label = (id: string) => names.get(id) ?? `${id.slice(0, 8)} (not found)`

  return (
    <div className="ps-deps">
      {list.isError && <p className="ps-form-warning">Pipelines could not be listed: {errorMessage(list.error)}</p>}
      {!rules.length && !legacyOnly.length && <p className="ps-form-note">No upstream dependencies. This pipeline runs on its own schedule.</p>}
      {rules.map((r, i) => (
        <div key={r.pipeline_id} className="ps-dep">
          <div className="ps-dep-head">
            <strong>{label(r.pipeline_id)}</strong>
            {!readonly && (
              <IconButton size="sm" variant="danger" label={`Remove ${label(r.pipeline_id)}`} onClick={() => onChange(rules.filter((_, j) => j !== i), legacy.filter((id) => id !== r.pipeline_id))}>
                <Trash2 size={14} aria-hidden="true" />
              </IconButton>
            )}
          </div>
          <div className="ps-dep-fields">
            <Select aria-label="Upstream state" value={r.state ?? 'succeeded'} disabled={readonly} onChange={(e) => setRule(i, { state: e.target.value as DependencyState })}>
              {STATES.map((s) => (
                <option key={s.value} value={s.value}>
                  {s.label}
                </option>
              ))}
            </Select>
            <Select aria-label="Mode" value={r.mode ?? 'gate'} disabled={readonly} onChange={(e) => setRule(i, { mode: e.target.value as DependencyMode })}>
              {MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </Select>
            <Input
              type="number"
              min={0}
              aria-label="Within hours"
              placeholder="Any time"
              disabled={readonly}
              value={r.within_seconds ? String(r.within_seconds / 3600) : ''}
              onChange={(e) => setRule(i, { within_seconds: Number(e.target.value) > 0 ? Math.round(Number(e.target.value) * 3600) : undefined })}
            />
          </div>
        </div>
      ))}
      {legacyOnly.map((id) => (
        <div key={id} className="ps-dep is-legacy">
          <div className="ps-dep-head">
            <strong>{label(id)}</strong>
            <Badge>legacy</Badge>
            {!readonly && (
              <>
                <Button size="sm" variant="ghost" onClick={() => onChange([...rules, { pipeline_id: id, state: 'succeeded', mode: 'gate' }], legacy.filter((x) => x !== id))}>
                  Convert to a rule
                </Button>
                <IconButton size="sm" variant="danger" label={`Remove ${label(id)}`} onClick={() => onChange(rules, legacy.filter((x) => x !== id))}>
                  <Trash2 size={14} aria-hidden="true" />
                </IconButton>
              </>
            )}
          </div>
          <p className="ps-form-note">Waits for the latest run to succeed, at any age.</p>
        </div>
      ))}
      {!readonly &&
        (adding ? (
          <div className="ps-dep is-new">
            <Field label="Upstream pipeline">
              <Select value={adding.pipeline_id} onChange={(e) => setAdding({ ...adding, pipeline_id: e.target.value })}>
                <option value="">Choose a pipeline</option>
                {available.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </Field>
            <div className="ps-dep-fields">
              <Select aria-label="Upstream state" value={adding.state} onChange={(e) => setAdding({ ...adding, state: e.target.value as DependencyState })}>
                {STATES.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </Select>
              <Select aria-label="Mode" value={adding.mode} onChange={(e) => setAdding({ ...adding, mode: e.target.value as DependencyMode })}>
                {MODES.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.label}
                  </option>
                ))}
              </Select>
            </div>
            <div className="ps-dep-actions">
              <Button size="sm" variant="ghost" onClick={() => setAdding(null)}>
                Cancel
              </Button>
              <Button
                size="sm"
                variant="primary"
                disabled={!adding.pipeline_id}
                onClick={() => {
                  onChange([...rules, adding], legacy)
                  setAdding(null)
                }}
              >
                Add dependency
              </Button>
            </div>
          </div>
        ) : (
          <Button size="sm" icon={<Plus size={14} aria-hidden="true" />} disabled={!available.length} onClick={() => setAdding({ pipeline_id: '', state: 'succeeded', mode: 'gate' })}>
            Add dependency
          </Button>
        ))}
    </div>
  )
}

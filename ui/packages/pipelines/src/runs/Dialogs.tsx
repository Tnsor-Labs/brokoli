import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { pipelineApi, type BackfillPlan } from '@brokoli/api'
import { Button, Callout, Checkbox, Field, IconButton, Input, Modal, errorMessage, formatDateTime } from '@brokoli/ui'

type Row = { id: number; key: string; value: string }

/*
 * Run with parameters. Rows start from the pipeline's declared defaults,
 * keys must be unique and non-empty, and nothing typed here survives a
 * cancel (the Svelte modal leaked cancelled values into the next Run Now).
 */
export function RunParamsDialog({
  defaults,
  onRun,
  onClose,
}: {
  defaults: Record<string, string> | undefined
  onRun: (params: Record<string, string>) => Promise<void>
  onClose: () => void
}) {
  const [rows, setRows] = useState<Row[]>(() => {
    const entries = Object.entries(defaults ?? {})
    return (entries.length ? entries : [['', '']]).map(([key, value], id) => ({ id, key, value }))
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const keysSeen = rows.map((r) => r.key.trim())
  const duplicate = keysSeen.find((k, i) => k && keysSeen.indexOf(k) !== i)
  const incomplete = rows.some((r) => !r.key.trim() && r.value)

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      await onRun(Object.fromEntries(rows.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value])))
      onClose()
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }
  const set = (id: number, patch: Partial<Row>) => setRows((all) => all.map((r) => (r.id === id ? { ...r, ...patch } : r)))

  return (
    <Modal
      title="Run with parameters"
      description="Values are passed to the run as string parameters and can be referenced as ${param.name} in node settings."
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={Boolean(duplicate) || incomplete}>
            Start run
          </Button>
        </>
      }
    >
      <div className="bk-params">
        {rows.map((r) => (
          <div key={r.id} className="bk-params-row">
            <Input mono aria-label="Parameter name" placeholder="name" value={r.key} onChange={(e) => set(r.id, { key: e.target.value })} />
            <Input aria-label={`Value for ${r.key || 'parameter'}`} placeholder="value" value={r.value} onChange={(e) => set(r.id, { value: e.target.value })} />
            <IconButton label="Remove parameter" variant="danger" onClick={() => setRows((all) => all.filter((x) => x.id !== r.id))}>
              <Trash2 size={15} aria-hidden="true" />
            </IconButton>
          </div>
        ))}
        <Button size="sm" variant="ghost" icon={<Plus size={14} aria-hidden="true" />} onClick={() => setRows((all) => [...all, { id: Date.now(), key: '', value: '' }])}>
          Add parameter
        </Button>
        {duplicate && <Callout tone="warning">The parameter "{duplicate}" is listed twice.</Callout>}
        {incomplete && <Callout tone="warning">Every value needs a parameter name.</Callout>}
        {error && <Callout tone="danger">{error}</Callout>}
      </div>
    </Modal>
  )
}

/*
 * Backfill runs every schedule interval between two dates (end date
 * inclusive), oldest first, one after the other.
 */
export function BackfillDialog({
  pipelineId,
  schedule,
  timezone,
  onDone,
  onClose,
}: {
  pipelineId: string
  schedule: string
  timezone?: string
  onDone: (plan: BackfillPlan) => void
  onClose: () => void
}) {
  const today = new Date().toISOString().slice(0, 10)
  const [start, setStart] = useState('')
  const [end, setEnd] = useState(today)
  const [force, setForce] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const invalid = Boolean(start && end && start > end)

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      onDone(await pipelineApi.backfill(pipelineId, start, end, force))
      onClose()
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Backfill"
      description={`Runs every interval of "${schedule}"${timezone ? ` (${timezone})` : ''} in the range, oldest first.`}
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!start || !end || invalid}>
            Start backfill
          </Button>
        </>
      }
    >
      <div className="bk-backfill">
        <div className="bk-backfill-dates">
          <Field label="From" hint="Starts at midnight UTC.">
            <Input type="date" value={start} max={end || today} onChange={(e) => setStart(e.target.value)} />
          </Field>
          <Field label="To (inclusive)">
            <Input type="date" value={end} min={start} onChange={(e) => setEnd(e.target.value)} />
          </Field>
        </div>
        <Checkbox
          label="Backfill even if no node reads the interval"
          description="The server normally refuses when no query or path uses ${interval.start} or ${interval.end}, because every interval would load the same data."
          checked={force}
          onChange={(e) => setForce(e.target.checked)}
        />
        {invalid && <Callout tone="warning">The start date is after the end date.</Callout>}
        {error && <Callout tone="danger">{error}</Callout>}
      </div>
    </Modal>
  )
}

export function backfillMessage(plan: BackfillPlan) {
  if (!plan.intervals) return 'No intervals to run in that range.'
  return `${plan.intervals} interval${plan.intervals === 1 ? '' : 's'} from ${formatDateTime(plan.first_interval_start)} to ${formatDateTime(plan.last_interval_end)}.`
}

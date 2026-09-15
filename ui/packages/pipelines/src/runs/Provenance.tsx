import { useMemo, useState } from 'react'
import type { RunEvent } from '@brokoli/api'
import { SegmentedControl, cx, formatDateTime } from '@brokoli/ui'
import { chronicleDetail, chronicleLabel } from './model'

type Filter = 'all' | 'run' | 'attempt' | 'recovery'

/** The run's durable event log: why it reached its outcome, fact by fact. */
export function Provenance({ events, nodeNames }: { events: RunEvent[]; nodeNames: Record<string, string> }) {
  const [filter, setFilter] = useState<Filter>('all')
  const [open, setOpen] = useState<string | null>(null)
  const counts = useMemo(
    () => ({
      all: events.length,
      run: events.filter((e) => !e.node_id).length,
      attempt: events.filter((e) => e.node_id).length,
      recovery: events.filter((e) => e.event_type.includes('recovery')).length,
    }),
    [events],
  )
  const shown = events.filter((e) =>
    filter === 'all' ? true : filter === 'run' ? !e.node_id : filter === 'attempt' ? Boolean(e.node_id) : e.event_type.includes('recovery'),
  )
  return (
    <div className="bk-provenance">
      <SegmentedControl
        label="Filter events"
        size="sm"
        value={filter}
        onChange={setFilter}
        options={[
          { value: 'all', label: 'All', count: counts.all },
          { value: 'run', label: 'Run', count: counts.run },
          { value: 'attempt', label: 'Attempts', count: counts.attempt },
          { value: 'recovery', label: 'Recovery', count: counts.recovery },
        ]}
      />
      {shown.length === 0 ? (
        <p className="bk-muted">No events in this view.</p>
      ) : (
        <ol className="bk-events">
          {shown.map((e) => {
            const key = `${e.id}-${e.created_at}`
            const failed = e.event_type.endsWith('failed')
            return (
              <li key={key} className={cx(failed && 'is-failed', open === key && 'is-open')}>
                <button type="button" onClick={() => setOpen(open === key ? null : key)} aria-expanded={open === key}>
                  <i aria-hidden="true" />
                  <span className="bk-event-main">
                    <strong>{chronicleLabel(e.event_type)}</strong>
                    <span>{chronicleDetail(e)}</span>
                  </span>
                  <span className="bk-event-side">
                    <span>{e.node_id ? `${nodeNames[e.node_id] ?? e.node_id} · attempt ${(e.attempt ?? 0) + 1}` : 'Run'}</span>
                    <time>{formatDateTime(e.created_at)}</time>
                  </span>
                </button>
                {open === key && <pre className="bk-event-payload">{JSON.stringify(e.payload ?? {}, null, 2)}</pre>}
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

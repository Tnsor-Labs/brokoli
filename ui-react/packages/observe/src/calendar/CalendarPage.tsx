import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { CalendarDays, X } from 'lucide-react'
import { observeApi } from '@brokoli/api'
import { useNow, useThrottledActivity } from '@brokoli/pipelines'
import { Button, Callout, EmptyState, IconButton, Page, PageHeader, SegmentedControl, Skeleton, errorMessage, formatNumber } from '@brokoli/ui'
import { Stat, rateTone } from '../Stat'
import { finishedRate } from '../dashboard/model'
import { HeatmapGrid, latestActive } from './Heatmap'
import { buildHeatmap, formatUtcDay, type HeatCell, type Heatmap } from './heatmap'
import '../observe.css'

type Range = '90' | '182' | '365'
const RANGES: { value: Range; label: string }[] = [
  { value: '90', label: '3 months' },
  { value: '182', label: '6 months' },
  { value: '365', label: '12 months' },
]

/** Daily volume, failures stacked on top. Decorative: the same numbers are in the grid and the day card. */
function Volume({ map }: { map: Heatmap }) {
  const peak = map.cells.reduce<HeatCell | null>((best, c) => (!best || c.total > best.total ? c : best), null)
  // With one active day the chart is a single line that says nothing the grid does not.
  if (!peak?.total || map.totals.activeDays < 2) return null
  const n = map.cells.length
  return (
    <figure className="ob-volume">
      <svg viewBox={`0 0 ${n} 100`} preserveAspectRatio="none" aria-hidden="true">
        {map.cells.map((c, i) => {
          if (!c.total) return null
          const h = (c.total / map.max) * 100
          const f = (c.failed / map.max) * 100
          return (
            <g key={c.date}>
              <rect className="ob-volume-ok" x={i + 0.1} width={0.8} y={100 - h} height={h - f} />
              {f > 0 && <rect className="ob-volume-fail" x={i + 0.1} width={0.8} y={100 - f} height={f} />}
            </g>
          )
        })}
      </svg>
      <figcaption>
        Busiest day: {formatUtcDay(peak.time)}, {formatNumber(peak.total)} runs
      </figcaption>
    </figure>
  )
}

function DayCard({ cell, onClose }: { cell: HeatCell; onClose: () => void }) {
  const rate = finishedRate(cell.success, cell.failed)
  const segments = [
    { key: 'ok', value: cell.success, label: 'Succeeded' },
    { key: 'run', value: cell.running, label: 'Running' },
    { key: 'fail', value: cell.failed, label: 'Failed' },
    { key: 'other', value: cell.other, label: 'Other' },
  ].filter((s) => s.value > 0)
  return (
    <section className="ob-day" aria-live="polite">
      <header>
        <div>
          <span className="ob-eyebrow">Selected day (UTC)</span>
          <h2>{formatUtcDay(cell.time)}</h2>
        </div>
        <IconButton size="sm" label="Clear the selected day" onClick={onClose}>
          <X size={15} aria-hidden="true" />
        </IconButton>
      </header>
      {cell.total ? (
        <>
          <div className="ob-split" aria-hidden="true">
            {segments.map((s) => (
              <i key={s.key} className={`ob-split-${s.key}`} style={{ flexGrow: s.value }} title={`${s.label}: ${s.value}`} />
            ))}
          </div>
          <dl className="ob-day-facts">
            <div>
              <dt>Runs</dt>
              <dd>{formatNumber(cell.total)}</dd>
            </div>
            <div>
              <dt>Succeeded</dt>
              <dd>{formatNumber(cell.success)}</dd>
            </div>
            <div>
              <dt>Failed</dt>
              <dd className={cell.failed ? 'ob-danger' : undefined}>{formatNumber(cell.failed)}</dd>
            </div>
            {cell.running > 0 && (
              <div>
                <dt>Running</dt>
                <dd>{formatNumber(cell.running)}</dd>
              </div>
            )}
            {cell.other > 0 && (
              <div>
                <dt title="Pending, cancelled, blocked or skipped">Other</dt>
                <dd>{formatNumber(cell.other)}</dd>
              </div>
            )}
            <div>
              <dt>Success rate</dt>
              <dd>{rate === null ? '-' : `${rate}%`}</dd>
            </div>
          </dl>
        </>
      ) : (
        <p className="ob-quiet">No runs started on this day.</p>
      )}
      <p className="ob-quiet ob-small">
        The server reports daily totals only. Individual runs are on each pipeline's runs page, from <Link to="/pipelines">Pipelines</Link>.
      </p>
    </section>
  )
}

export function CalendarPage() {
  const [range, setRange] = useState<Range>('365')
  const days = Number(range)
  const query = useQuery({ queryKey: ['observe', 'calendar', days], queryFn: () => observeApi.calendar(days), staleTime: 60_000 })
  const now = useNow(60_000)
  const map = useMemo(() => buildHeatmap(query.data ?? [], days, now), [query.data, days, now])
  // The selection is a date, so changing the range keeps the same day selected when it is still in view.
  const [selectedDate, setSelectedDate] = useState<string | null | undefined>(undefined)
  const selectedIndex = selectedDate === null ? null : selectedDate === undefined ? latestActive(map) : map.cells.findIndex((c) => c.date === selectedDate)
  const selected = selectedIndex !== null && selectedIndex >= 0 ? map.cells[selectedIndex] : null
  useThrottledActivity(() => void query.refetch(), 30_000)
  const { totals } = map
  const rate = finishedRate(totals.success, totals.failed)
  const ready = query.isSuccess

  return (
    <Page wide>
      <PageHeader
        eyebrow="Observe"
        title="Calendar"
        description="How many runs started each day, and how they ended. Days are UTC dates, the way the server records them."
        actions={<SegmentedControl label="Period" size="sm" value={range} onChange={setRange} options={RANGES} />}
      />
      <div className="ob-stats">
        <Stat label="Runs" value={ready ? formatNumber(totals.total) : '-'} />
        <Stat label="Succeeded" value={ready ? formatNumber(totals.success) : '-'} />
        <Stat label="Failed" value={ready ? formatNumber(totals.failed) : '-'} tone={totals.failed ? 'danger' : 'neutral'} />
        <Stat label="Days with runs" value={ready ? `${totals.activeDays} of ${days}` : '-'} />
        <Stat
          label="Success rate"
          value={ready && rate !== null ? `${rate}%` : '-'}
          foot={ready ? (rate === null ? 'No finished runs' : 'Of finished runs') : undefined}
          tone={ready ? rateTone(rate) : 'neutral'}
          title="Runs still in flight, pending or cancelled are not counted either way."
        />
      </div>
      {query.isError ? (
        <Callout tone="danger" title="Run history could not be loaded" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : query.isPending ? (
        <div className="ob-box">
          <Skeleton height={120} />
        </div>
      ) : !totals.total ? (
        <div className="ob-box">
          <EmptyState
            icon={<CalendarDays size={20} aria-hidden="true" />}
            title={`No runs in the last ${RANGES.find((r) => r.value === range)?.label}`}
            action={
              <Link className="bk-button bk-button-primary bk-button-md" to="/pipelines">
                Go to pipelines
              </Link>
            }
          >
            Runs appear here as soon as a pipeline starts, on a schedule or by hand.
          </EmptyState>
        </div>
      ) : (
        <div className="ob-calendar">
          <section className="ob-box">
            <Volume map={map} />
            <HeatmapGrid
              map={map}
              label="Runs per day"
              selected={selectedIndex !== null && selectedIndex >= 0 ? selectedIndex : null}
              onSelect={(i) => setSelectedDate(map.cells[i].date)}
            />
          </section>
          {selected && <DayCard cell={selected} onClose={() => setSelectedDate(null)} />}
        </div>
      )}
    </Page>
  )
}

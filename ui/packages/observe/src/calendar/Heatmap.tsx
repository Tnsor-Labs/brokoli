import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { cx } from '@brokoli/ui'
import { formatUtcDay, moveIndex, type HeatCell, type Heatmap } from './heatmap'

export function describeDay(c: HeatCell) {
  if (!c.total) return `${formatUtcDay(c.time)}: no runs`
  const parts = [`${c.total} run${c.total === 1 ? '' : 's'}`, `${c.success} succeeded`, `${c.failed} failed`]
  if (c.running) parts.push(`${c.running} running`)
  if (c.other) parts.push(`${c.other} other`)
  return `${formatUtcDay(c.time)}: ${parts.join(', ')}`
}

export function latestActive(map: Heatmap) {
  for (let i = map.cells.length - 1; i >= 0; i--) if (map.cells[i].total > 0) return i
  return map.cells.length - 1
}

/*
 * Weekday-aligned run heatmap. Interactive when `onSelect` is given: one tab
 * stop, arrow keys move a day (up and down) or a week (left and right), and
 * the selection follows the focus. Shade is volume on a log scale; a day
 * with any failed run is outlined in the danger colour, so failures stay
 * visible on busy days without hiding how busy the day was.
 */
export function HeatmapGrid({
  map,
  label,
  selected = null,
  onSelect,
  size = 'md',
}: {
  map: Heatmap
  label: string
  selected?: number | null
  onSelect?: (index: number) => void
  size?: 'sm' | 'md'
}) {
  const [focus, setFocus] = useState(() => selected ?? latestActive(map))
  const cells = useRef<(HTMLButtonElement | null)[]>([])
  const scroller = useRef<HTMLDivElement>(null)
  const focusIndex = Math.min(focus, map.cells.length - 1)

  // The newest days are on the right; start there when the grid is wider than the screen.
  useEffect(() => {
    const el = scroller.current
    if (el) el.scrollLeft = el.scrollWidth
  }, [map.weeks.length])

  const onKey = (e: KeyboardEvent, index: number) => {
    const next = moveIndex(index, e.key, map.cells.length)
    if (next === null) return
    e.preventDefault()
    setFocus(next)
    cells.current[next]?.focus()
    onSelect?.(next)
  }

  return (
    <div className={cx('ob-heat', `ob-heat-${size}`)}>
      <div className="ob-heat-scroll" ref={scroller}>
        <div className="ob-heat-grid" role="group" aria-label={label} style={{ '--ob-weeks': map.weeks.length } as CSSProperties}>
          {map.months.map((m) => (
            <span key={m.column} className="ob-heat-month" aria-hidden="true" style={{ gridColumn: `${m.column + 2} / span 3`, gridRow: 1 }}>
              {m.label}
            </span>
          ))}
          {['Mon', 'Wed', 'Fri'].map((d, i) => (
            <span key={d} className="ob-heat-weekday" aria-hidden="true" style={{ gridColumn: 1, gridRow: i * 2 + 3 }}>
              {d}
            </span>
          ))}
          {map.weeks.flatMap((week, w) =>
            week.map((c, d) => {
              const style = { gridColumn: w + 2, gridRow: d + 2 }
              if (!c) return <span key={`pad-${w}-${d}`} style={style} aria-hidden="true" />
              const className = cx('ob-heat-cell', `is-l${c.level}`, c.failed > 0 && 'has-failed', c.isToday && 'is-today', selected === c.index && 'is-selected')
              const text = describeDay(c)
              return onSelect ? (
                <button
                  key={c.date}
                  ref={(el) => {
                    cells.current[c.index] = el
                  }}
                  type="button"
                  style={style}
                  className={className}
                  tabIndex={c.index === focusIndex ? 0 : -1}
                  aria-label={text}
                  aria-pressed={selected === c.index}
                  title={text}
                  onClick={() => {
                    setFocus(c.index)
                    onSelect(c.index)
                  }}
                  onKeyDown={(e) => onKey(e, c.index)}
                />
              ) : (
                <span key={c.date} style={style} className={className} title={text} />
              )
            }),
          )}
        </div>
      </div>
      <div className="ob-heat-legend" aria-hidden="true">
        <span>Fewer runs</span>
        {[0, 1, 2, 3, 4].map((l) => (
          <i key={l} className={`ob-heat-cell is-l${l}`} />
        ))}
        <span>More</span>
        <i className="ob-heat-cell is-l2 has-failed" />
        <span>At least one failed run</span>
        <span className="ob-heat-utc">Days are UTC dates</span>
      </div>
    </div>
  )
}

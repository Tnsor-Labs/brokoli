import { useEffect, useMemo, useRef, useState } from 'react'
import { CalendarClock, ChevronDown } from 'lucide-react'
import { schedulerApi, type Pipeline, type SchedulePreview } from '@brokoli/api'
import { Checkbox, Field, Input, Select, Spinner, cx, errorMessage } from '@brokoli/ui'

function timezones(current?: string) {
  let list: string[] = []
  try {
    list = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    list = []
  }
  const browser = Intl.DateTimeFormat().resolvedOptions().timeZone
  const all = new Set(['UTC', ...list, browser, ...(current ? [current] : [])])
  return [...all]
}

function formatNext(iso: string, timeZone: string) {
  try {
    return new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone }).format(new Date(iso))
  } catch {
    return iso
  }
}

/*
 * Schedule. Plain language ("every weekday at 9am") or cron, checked by the
 * server as you type. Only a schedule the server accepts is stored; a
 * refused one stays in the field with the reason and never replaces the
 * stored cron, and the toolbar says so instead of displaying the rejected
 * text as if it were set.
 */
export function SchedulePopover({ pipeline, readonly, onChange }: { pipeline: Pipeline; readonly: boolean; onChange: (patch: Partial<Pipeline>) => void }) {
  const [open, setOpen] = useState(false)
  const [input, setInput] = useState(pipeline.schedule ?? '')
  const [touched, setTouched] = useState(false)
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const [checking, setChecking] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const change = useRef(onChange)
  change.current = onChange
  const stored = pipeline.schedule ?? ''
  const tz = pipeline.schedule_timezone || ''
  const zones = useMemo(() => timezones(pipeline.schedule_timezone), [pipeline.schedule_timezone])

  // A stored schedule changed underneath us (version restore): show it.
  const seen = useRef(stored)
  useEffect(() => {
    if (stored !== seen.current && !touched) setInput(stored)
    seen.current = stored
  }, [stored, touched])

  useEffect(() => {
    const text = input.trim()
    if (!text) {
      setPreview(null)
      setChecking(false)
      if (touched && stored) change.current({ schedule: '' })
      return
    }
    const controller = new AbortController()
    setChecking(true)
    const timer = setTimeout(async () => {
      try {
        const result = await schedulerApi.preview(text, tz, controller.signal)
        setPreview(result)
        if (touched && result.valid && result.cron !== stored) change.current({ schedule: result.cron })
      } catch (e) {
        if (!controller.signal.aborted) setPreview({ valid: false, error: `The schedule could not be checked: ${errorMessage(e)}` })
      } finally {
        if (!controller.signal.aborted) setChecking(false)
      }
    }, 250)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
    // `stored` is read at fire time; re-running on it would re-check after every accepted change.
  }, [input, tz, touched]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => !root.current?.contains(e.target as Node) && setOpen(false)
    document.addEventListener('pointerdown', onPointer)
    return () => document.removeEventListener('pointerdown', onPointer)
  }, [open])

  const pending = touched && input.trim() !== '' && !(preview?.valid && preview.cron === stored)
  const summary = !stored ? 'Manual' : preview?.valid && preview.cron === stored ? preview.description || stored : stored
  const zone = preview?.valid ? preview.timezone || 'UTC' : tz || 'UTC'

  return (
    <div className="ps-schedule" ref={root}>
      <button
        type="button"
        className={cx('ps-schedule-button', stored && 'is-set', pending && 'is-pending')}
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => setOpen((v) => !v)}
        title="When this pipeline runs"
      >
        <CalendarClock size={15} aria-hidden="true" />
        <span className="ps-schedule-summary">{summary}</span>
        {pending && <span className="ps-schedule-flag">not applied</span>}
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {open && (
        <div className="ps-schedule-pop" role="dialog" aria-label="Schedule" onKeyDown={(e) => e.key === 'Escape' && (e.stopPropagation(), setOpen(false))}>
          <Field label="Runs" hint='Plain language such as "every weekday at 9am" or "every 15 minutes", or a cron expression. Leave empty to run only manually.'>
            <Input
              autoFocus
              value={input}
              disabled={readonly}
              placeholder="No schedule (manual)"
              onChange={(e) => {
                setTouched(true)
                setInput(e.target.value)
              }}
            />
          </Field>
          <div className="ps-schedule-row">
            <Field label="Time zone">
              <Select value={pipeline.schedule_timezone || 'UTC'} disabled={readonly} onChange={(e) => change.current({ schedule_timezone: e.target.value })}>
                {zones.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Checkbox
            label="Catch up after downtime"
            description="Run every missed interval, oldest first, instead of only the latest one."
            checked={Boolean(pipeline.catchup)}
            disabled={readonly}
            onChange={(e) => change.current({ catchup: e.target.checked })}
          />
          {input.trim() && (
            <div className={cx('ps-schedule-echo', preview && !preview.valid && 'is-invalid')} aria-live="polite">
              {checking && !preview ? (
                <span className="ps-schedule-checking">
                  <Spinner size="sm" /> Checking
                </span>
              ) : preview?.valid ? (
                <>
                  <strong>
                    {preview.description || 'Custom schedule'} <span className="bk-muted">({zone})</span>
                  </strong>
                  <code>{preview.cron}</code>
                  {preview.next.length > 0 && <span>Next: {preview.next.map((n) => formatNext(n, zone)).join(' · ')}</span>}
                  {pipeline.catchup && <em>Catch-up is on, so changing this also changes what a backfill covers.</em>}
                </>
              ) : preview ? (
                <>
                  <strong>Not a schedule the server accepts</strong>
                  <span>{preview.error}</span>
                  {preview.suggestion && !readonly && (
                    <button
                      type="button"
                      className="bk-link"
                      onClick={() => {
                        setTouched(true)
                        setInput(preview.suggestion!)
                      }}
                    >
                      Use "{preview.suggestion}"
                    </button>
                  )}
                  {stored && <span className="bk-muted">The saved schedule stays {stored}.</span>}
                </>
              ) : null}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

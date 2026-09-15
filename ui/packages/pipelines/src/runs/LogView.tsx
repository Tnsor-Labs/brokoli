import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { downloadText, runApi, runLogsKey, watchKey, type LogEntry, type Run } from '@brokoli/api'
import { Button, Callout, Checkbox, SearchInput, Select, Spinner, cx, errorMessage, useToast } from '@brokoli/ui'
import { keys } from '../keys'
import { isActive, mergeLiveTail, type LiveLine } from './model'

const LEVEL_RANK: Record<string, number> = { debug: 0, info: 1, warning: 2, warn: 2, error: 3 }
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hourCycle: 'h23' })

function formatStamp(ts: string) {
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : timeFmt.format(d)
}

/*
 * Run logs: the full history over REST, plus the live tail over SODP while
 * the run is active. The view follows new lines only while the reader is at
 * the bottom; scrolling up pauses it instead of yanking the page back.
 */
export function LogView({ run, nodeNames }: { run: Run; nodeNames: Record<string, string> }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const history = useQuery({ queryKey: keys.runLogs(run.id), queryFn: () => runApi.logs(run.id) })
  const [lines, setLines] = useState<LogEntry[]>([])
  const [level, setLevel] = useState('all')
  const [node, setNode] = useState('')
  const [search, setSearch] = useState('')
  const [follow, setFollow] = useState(true)
  const [exporting, setExporting] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const active = isActive(run.status)

  useEffect(() => {
    if (history.data) setLines(history.data)
  }, [history.data])

  // Live tail while the run is active. A null value (the key was evicted) is ignored, never shown as "no logs".
  useEffect(() => {
    if (!active || !history.data) return
    return watchKey<LiveLine[]>(runLogsKey(run.id), (tail) => {
      if (tail) setLines((shown) => mergeLiveTail(shown, tail, run.id))
    })
  }, [active, history.data, run.id])

  // When the run finishes, reload the authoritative history (full fields, full precision).
  const wasActive = useRef(active)
  useEffect(() => {
    if (wasActive.current && !active) void queryClient.invalidateQueries({ queryKey: keys.runLogs(run.id) })
    wasActive.current = active
  }, [active, queryClient, run.id])

  const nodes = useMemo(() => [...new Set(lines.map((l) => l.node_id).filter(Boolean))], [lines])
  const visible = useMemo(() => {
    const min = level === 'all' ? -1 : (LEVEL_RANK[level] ?? -1)
    const q = search.trim().toLowerCase()
    return lines.filter(
      (l) =>
        (LEVEL_RANK[l.level] ?? 1) >= min && (!node || l.node_id === node) && (!q || l.message.toLowerCase().includes(q)),
    )
  }, [lines, level, node, search])

  useEffect(() => {
    const el = box.current
    if (el && follow) el.scrollTop = el.scrollHeight
  }, [visible, follow])

  const onScroll = () => {
    const el = box.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    if (atBottom !== follow) setFollow(atBottom)
  }

  const download = async () => {
    setExporting(true)
    try {
      const file = await runApi.exportLogs(run.id)
      downloadText(file.filename, file.text)
    } catch (e) {
      toast.error('Could not download the logs', e)
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="bk-logs">
      <div className="bk-logs-toolbar">
        <SearchInput value={search} onChange={setSearch} placeholder="Filter log messages" className="bk-logs-search" />
        <Select value={level} onChange={(e) => setLevel(e.target.value)} aria-label="Minimum level" className="bk-logs-select">
          <option value="all">All levels</option>
          <option value="info">Info and above</option>
          <option value="warning">Warnings and errors</option>
          <option value="error">Errors only</option>
        </Select>
        {nodes.length > 1 && (
          <Select value={node} onChange={(e) => setNode(e.target.value)} aria-label="Node" className="bk-logs-select">
            <option value="">All nodes</option>
            {nodes.map((n) => (
              <option key={n} value={n}>
                {nodeNames[n] ?? n}
              </option>
            ))}
          </Select>
        )}
        <Checkbox label="Follow" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
        <Button size="sm" icon={<Download size={14} aria-hidden="true" />} onClick={download} loading={exporting}>
          Download
        </Button>
      </div>
      {history.isError && (
        <Callout tone="danger" title="Logs could not be loaded">
          {errorMessage(history.error)}
        </Callout>
      )}
      <div ref={box} className="bk-logs-body" onScroll={onScroll} role="log" aria-live={active ? 'polite' : 'off'} aria-busy={history.isPending}>
        {history.isPending ? (
          <div className="bk-logs-empty">
            <Spinner size="sm" /> Loading logs
          </div>
        ) : visible.length === 0 ? (
          <div className="bk-logs-empty">{lines.length ? 'No lines match the filters.' : active ? 'Waiting for the first log line.' : 'This run produced no logs.'}</div>
        ) : (
          visible.map((l, i) => (
            <div key={i} className={cx('bk-log-line', `is-${l.level === 'warn' ? 'warning' : l.level}`)}>
              <time>{formatStamp(l.timestamp)}</time>
              <span className="bk-log-level">{l.level}</span>
              {l.node_id && !node ? <span className="bk-log-node">{nodeNames[l.node_id] ?? l.node_id}</span> : <span />}
              <span className="bk-log-msg">{l.message}</span>
            </div>
          ))
        )}
      </div>
      <div className="bk-logs-foot">
        {visible.length === lines.length ? `${lines.length} lines` : `${visible.length} of ${lines.length} lines`}
        {active && <span> · live</span>}
      </div>
    </div>
  )
}

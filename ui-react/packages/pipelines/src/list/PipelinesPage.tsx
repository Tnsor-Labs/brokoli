import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { Copy, Download, FileUp, History, LayoutGrid, Pause, PencilLine, Play, Plus, Trash2, Workflow } from 'lucide-react'
import { downloadText, pipelineApi, schedulerApi, type PipelineSummary } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  Kbd,
  Menu,
  Page,
  PageHeader,
  Pagination,
  SearchInput,
  SegmentedControl,
  Select,
  Skeleton,
  StatusBadge,
  Switch,
  cx,
  errorMessage,
  formatDateTime,
  formatRelative,
  statusMeta,
  toDate,
  useToast,
  type MenuItem,
} from '@brokoli/ui'
import { ACTIVE, FAILURE, SUCCESS, keys, paths } from '../keys'
import { useNow, useRunActivity } from '../live'
import { CreatePipelineModal } from './CreatePipelineModal'
import { DeletePipelineFlow } from './DeletePipelineFlow'
import './list.css'

type Scope = 'all' | 'healthy' | 'failing' | 'active' | 'never' | 'paused'
type Sort = 'name' | 'last_run' | 'updated' | 'nodes'
type Density = 'comfortable' | 'compact'
type View = { scope: Scope; tag: string; sort: Sort; density: Density }

const VIEW_KEY = 'brokoli-pipeline-view'
const PAGE_SIZE = 25
const DEFAULT_VIEW: View = { scope: 'all', tag: '', sort: 'name', density: 'comfortable' }
const SCOPES: Scope[] = ['all', 'healthy', 'failing', 'active', 'never', 'paused']
const SORTS: Sort[] = ['name', 'last_run', 'updated', 'nodes']
/** The Svelte UI stored a status filter under the same key; map it so a saved view survives the migration. */
const LEGACY_SCOPE: Record<string, Scope> = { success: 'healthy', failed: 'failing', running: 'active', paused: 'paused', never: 'never' }

function readView(): View {
  try {
    const raw = JSON.parse(localStorage.getItem(VIEW_KEY) ?? 'null') as Record<string, unknown> | null
    if (!raw || typeof raw !== 'object') return DEFAULT_VIEW
    const scope = SCOPES.includes(raw.scope as Scope) ? (raw.scope as Scope) : (LEGACY_SCOPE[String(raw.statusFilter)] ?? 'all')
    const sort = SORTS.includes((raw.sort ?? raw.sortBy) as Sort) ? ((raw.sort ?? raw.sortBy) as Sort) : 'name'
    const tag = typeof (raw.tag ?? raw.tagFilter) === 'string' ? String(raw.tag ?? raw.tagFilter) : ''
    const density = raw.density === 'compact' ? 'compact' : 'comfortable'
    return { scope, sort, tag, density }
  } catch {
    return DEFAULT_VIEW
  }
}

const lastStatus = (p: PipelineSummary) => (p.last_run_status || '').toLowerCase()

function inScope(p: PipelineSummary, scope: Scope) {
  const paused = p.enabled === false
  if (scope === 'all') return true
  if (scope === 'paused') return paused
  if (paused) return false
  const s = lastStatus(p)
  if (scope === 'healthy') return SUCCESS.has(s)
  if (scope === 'failing') return FAILURE.has(s)
  if (scope === 'active') return ACTIVE.has(s)
  return s === ''
}

function compareBy(sort: Sort) {
  const time = (v?: string) => toDate(v)?.getTime() ?? -Infinity
  return (a: PipelineSummary, b: PipelineSummary) => {
    if (sort === 'last_run') return time(b.last_run_at) - time(a.last_run_at) || a.name.localeCompare(b.name)
    if (sort === 'updated') return time(b.updated_at) - time(a.updated_at)
    if (sort === 'nodes') return (b.node_count ?? 0) - (a.node_count ?? 0) || a.name.localeCompare(b.name)
    return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
  }
}

/** Drops separators that would lead, trail or double up after permission filtering. */
function tidy(items: (MenuItem | false)[]): MenuItem[] {
  const out: MenuItem[] = []
  for (const item of items) {
    if (!item) continue
    if (item === 'separator' && (!out.length || out[out.length - 1] === 'separator')) continue
    out.push(item)
  }
  if (out[out.length - 1] === 'separator') out.pop()
  return out
}

function RunHistory({ history }: { history: string[] | null }) {
  const slots = Array.from({ length: 5 }, (_, i) => history?.[i] ?? '')
  const known = slots.filter(Boolean)
  const label = known.length
    ? `Last ${known.length} run${known.length === 1 ? '' : 's'}, newest first: ${known.map((s) => statusMeta(s).label).join(', ')}`
    : 'No runs yet'
  return (
    <span className="bk-run-bars" role="img" aria-label={label} title={label}>
      {slots.map((s, i) => (
        <i key={i} className={s ? `bk-tone-${statusMeta(s).tone}` : 'is-empty'} />
      ))}
    </span>
  )
}

export function PipelinesPage() {
  const session = useSession()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const now = useNow()
  const summary = useQuery({ queryKey: keys.summary, queryFn: pipelineApi.summary })
  const scheduler = useQuery({ queryKey: keys.scheduler, queryFn: schedulerApi.status })
  const [view, setView] = useState<View>(readView)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<PipelineSummary | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  useRunActivity(() => {
    void queryClient.invalidateQueries({ queryKey: keys.summary })
  })

  // "/" focuses search, the convention most tools share; the Svelte page advertised a shortcut that opened something else.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return
      if (el.closest('input, textarea, select, [contenteditable="true"], [role="dialog"]')) return
      e.preventDefault()
      searchRef.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const update = (patch: Partial<View>) => {
    setView((v) => ({ ...v, ...patch }))
    setPage(1)
  }

  const all = useMemo(() => summary.data ?? [], [summary.data])
  const nextRun = useMemo(() => new Map((scheduler.data ?? []).map((e) => [e.pipeline_id, e.next_run])), [scheduler.data])
  const tags = useMemo(() => [...new Set(all.flatMap((p) => p.tags ?? []))].sort(), [all])
  const counts = useMemo(() => Object.fromEntries(SCOPES.map((s) => [s, all.filter((p) => inScope(p, s)).length])) as Record<Scope, number>, [all])
  const tagMissing = Boolean(view.tag) && !tags.includes(view.tag) && summary.isSuccess

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return all
      .filter((p) => inScope(p, view.scope))
      .filter((p) => !view.tag || (p.tags ?? []).includes(view.tag))
      .filter((p) => !q || `${p.name} ${p.description ?? ''} ${(p.tags ?? []).join(' ')}`.toLowerCase().includes(q))
      .sort(compareBy(view.sort))
  }, [all, view, search])

  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const current = Math.min(page, pageCount)
  const rows = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE)

  const saveView = () => {
    try {
      localStorage.setItem(VIEW_KEY, JSON.stringify(view))
      toast.success('View saved', 'Filters, sort and density will be restored next time.')
    } catch (e) {
      toast.error('The view could not be saved', e)
    }
  }

  const act = async (id: string, work: () => Promise<void>) => {
    setBusyId(id)
    try {
      await work()
    } finally {
      setBusyId(null)
    }
  }

  const toggle = (p: PipelineSummary) =>
    act(p.id, async () => {
      try {
        // The server decodes the full document strictly, so read it, flip the flag, write it back.
        const full = await pipelineApi.get(p.id)
        const saved = await pipelineApi.update(p.id, { ...full, enabled: !full.enabled })
        queryClient.setQueryData<PipelineSummary[]>(keys.summary, (list) =>
          list?.map((x) => (x.id === p.id ? { ...x, enabled: saved.enabled } : x)),
        )
        void queryClient.invalidateQueries({ queryKey: keys.scheduler })
        toast.success(saved.enabled ? `${p.name} enabled` : `${p.name} paused`)
      } catch (e) {
        toast.error(`Could not ${p.enabled ? 'pause' : 'enable'} ${p.name}`, e)
      }
    })

  const run = (p: PipelineSummary) =>
    act(p.id, async () => {
      try {
        const started = await pipelineApi.run(p.id)
        toast.success(`Run started for ${p.name}`, `Run ${started.id.slice(0, 8)} is ${started.status}.`)
        void queryClient.invalidateQueries({ queryKey: keys.summary })
      } catch (e) {
        toast.error(`Could not start ${p.name}`, e)
      }
    })

  const clone = (p: PipelineSummary) =>
    act(p.id, async () => {
      try {
        const copy = await pipelineApi.clone(p.id)
        await queryClient.invalidateQueries({ queryKey: keys.summary })
        toast.success(`Cloned as "${copy.name}"`)
      } catch (e) {
        toast.error(`Could not clone ${p.name}`, e)
      }
    })

  const exportYaml = (p: PipelineSummary) =>
    act(p.id, async () => {
      try {
        const file = await pipelineApi.exportYaml(p.id)
        downloadText(file.filename, file.text, 'application/x-yaml')
      } catch (e) {
        toast.error(`Could not export ${p.name}`, e)
      }
    })

  const onImport = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    // The server reads at most 1 MiB and would fail on a truncated document with a confusing parse error.
    if (file.size > 1024 * 1024) return toast.error('Import failed', `${file.name} is larger than 1 MiB, the most the server accepts.`)
    let text = await file.text()
    if (file.name.toLowerCase().endsWith('.json')) {
      try {
        const doc = JSON.parse(text) as Record<string, unknown>
        if (!doc || typeof doc !== 'object' || Array.isArray(doc)) throw new Error('not an object')
        // An import creates a new pipeline. Identity fields from an export would collide with the original.
        for (const k of ['id', 'pipeline_id', 'created_at', 'updated_at', 'workspace_id', 'org_id', 'source']) delete doc[k]
        text = JSON.stringify(doc)
      } catch {
        return toast.error('Import failed', `${file.name} is not a valid JSON pipeline document.`)
      }
    }
    try {
      const created = await pipelineApi.importDocument(text, file.name)
      await queryClient.invalidateQueries({ queryKey: keys.summary })
      toast.success('Pipeline imported', created.name)
    } catch (err) {
      toast.error(`Could not import ${file.name}`, err)
    }
  }

  const canCreate = session.can('pipelines.create')
  const menuFor = (p: PipelineSummary): MenuItem[] =>
    tidy([
      { id: 'edit', label: 'Open editor', icon: <PencilLine size={15} />, onSelect: () => navigate(paths.editor(p.id)) },
      { id: 'runs', label: 'View runs', icon: <History size={15} />, onSelect: () => navigate(paths.runs(p.id)) },
      { id: 'grid', label: 'Run grid', icon: <LayoutGrid size={15} />, onSelect: () => navigate(paths.grid(p.id)) },
      'separator',
      session.can('pipelines.edit') &&
        p.source !== 'git' && {
          id: 'toggle',
          label: p.enabled === false ? 'Enable schedule' : 'Pause schedule',
          icon: p.enabled === false ? <Play size={15} /> : <Pause size={15} />,
          onSelect: () => void toggle(p),
        },
      session.can('pipelines.edit') && { id: 'clone', label: 'Clone', icon: <Copy size={15} />, onSelect: () => void clone(p) },
      session.can('pipelines.export') && { id: 'export', label: 'Export YAML', icon: <Download size={15} />, onSelect: () => void exportYaml(p) },
      'separator',
      session.can('pipelines.delete') && {
        id: 'delete',
        label: 'Delete',
        icon: <Trash2 size={15} />,
        tone: 'danger',
        onSelect: () => setDeleting(p),
      },
    ])

  const firstLoad = summary.isPending
  const scopeOptions = [
    { value: 'all' as const, label: 'All', count: counts.all },
    { value: 'healthy' as const, label: 'Healthy', count: counts.healthy },
    { value: 'failing' as const, label: 'Failing', count: counts.failing },
    { value: 'active' as const, label: 'Running', count: counts.active },
    { value: 'never' as const, label: 'Never run', count: counts.never },
    { value: 'paused' as const, label: 'Paused', count: counts.paused },
  ]

  return (
    <Page>
      <input ref={fileRef} type="file" accept=".yaml,.yml,.json" hidden onChange={onImport} />
      <PageHeader
        eyebrow="Workspace"
        title="Pipelines"
        description="Build, schedule and operate every data workflow from one inventory."
        actions={
          canCreate && (
            <>
              <Button icon={<FileUp size={16} aria-hidden="true" />} onClick={() => fileRef.current?.click()}>
                Import
              </Button>
              <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setCreating(true)}>
                New pipeline
              </Button>
            </>
          )
        }
      />

      {summary.isError && !summary.data && (
        <Callout
          tone="danger"
          title="The pipeline inventory could not be loaded"
          action={
            <Button size="sm" onClick={() => void summary.refetch()} loading={summary.isFetching}>
              Try again
            </Button>
          }
        >
          {errorMessage(summary.error)} Your pipelines have not been changed.
        </Callout>
      )}
      {summary.isError && summary.data && (
        <Callout tone="warning" className="bk-list-stale">
          Showing the list as of {formatRelative(summary.dataUpdatedAt, now)}. Refreshing failed: {errorMessage(summary.error)}
        </Callout>
      )}

      {!summary.isError && summary.isSuccess && all.length === 0 ? (
        <div className="bk-list-empty">
          <EmptyState
            icon={<Workflow size={20} aria-hidden="true" />}
            title="Build your first pipeline"
            action={
              canCreate && (
                <>
                  <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setCreating(true)}>
                    Create a pipeline
                  </Button>
                  <Button icon={<FileUp size={16} aria-hidden="true" />} onClick={() => fileRef.current?.click()}>
                    Import YAML or JSON
                  </Button>
                </>
              )
            }
          >
            Start from an empty draft or a template, then connect sources, transforms and outputs on the canvas.
          </EmptyState>
        </div>
      ) : (
        (firstLoad || all.length > 0) && (
          <section className="bk-list" aria-label="Pipeline inventory">
            <div className="bk-list-scopes">
              <SegmentedControl label="Filter by health" options={scopeOptions} value={view.scope} onChange={(scope) => update({ scope })} />
            </div>
            <div className="bk-list-toolbar">
              <SearchInput
                ref={searchRef}
                value={search}
                onChange={(v) => {
                  setSearch(v)
                  setPage(1)
                }}
                placeholder="Search by name, description or tag"
                shortcut={<Kbd>/</Kbd>}
                className="bk-list-search"
              />
              {(tags.length > 0 || view.tag) && (
                <Select value={view.tag} onChange={(e) => update({ tag: e.target.value })} aria-label="Filter by tag" className="bk-list-select">
                  <option value="">All tags</option>
                  {tagMissing && <option value={view.tag}>{view.tag} (no longer used)</option>}
                  {tags.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </Select>
              )}
              <Select value={view.sort} onChange={(e) => update({ sort: e.target.value as Sort })} aria-label="Sort pipelines" className="bk-list-select">
                <option value="name">Sort: name</option>
                <option value="last_run">Sort: last run</option>
                <option value="updated">Sort: recently edited</option>
                <option value="nodes">Sort: node count</option>
              </Select>
              <SegmentedControl
                label="Row density"
                size="sm"
                value={view.density}
                onChange={(density) => update({ density })}
                options={[
                  { value: 'comfortable', label: 'Comfortable' },
                  { value: 'compact', label: 'Compact' },
                ]}
              />
              <Button size="sm" variant="ghost" onClick={saveView}>
                Save view
              </Button>
            </div>

            {firstLoad ? (
              <div className="bk-table-wrap bk-list-loading" aria-busy="true" aria-label="Loading pipelines">
                {Array.from({ length: 6 }, (_, i) => (
                  <Skeleton key={i} height={22} />
                ))}
              </div>
            ) : filtered.length === 0 ? (
              <div className="bk-table-wrap">
                <EmptyState
                  title="No pipelines match these filters"
                  action={
                    <Button
                      onClick={() => {
                        setSearch('')
                        update({ scope: 'all', tag: '' })
                      }}
                    >
                      Clear filters
                    </Button>
                  }
                >
                  {all.length} pipeline{all.length === 1 ? '' : 's'} in the workspace; none match the current search, health filter or tag.
                </EmptyState>
              </div>
            ) : (
              <>
                <div className="bk-table-wrap">
                  <table className={cx('bk-table', 'bk-pipelines-table', view.density === 'compact' && 'is-compact')}>
                    <thead>
                      <tr>
                        <th scope="col">Pipeline</th>
                        <th scope="col">Last result</th>
                        <th scope="col">Recent runs</th>
                        <th scope="col">Schedule</th>
                        <th scope="col">Last run</th>
                        <th scope="col" className="is-num">
                          Nodes
                        </th>
                        <th scope="col">
                          <span className="bk-sr-only">Actions</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((p) => {
                        const status = lastStatus(p)
                        const next = nextRun.get(p.id)
                        const running = ACTIVE.has(status)
                        const runBlocked = p.draft
                          ? 'Drafts cannot run. Publish it from the editor first.'
                          : p.enabled === false
                            ? 'This pipeline is paused. Enable it to run.'
                            : running
                              ? 'A run is already in progress.'
                              : ''
                        return (
                          <tr key={p.id} className={cx('is-clickable', p.enabled === false && 'is-paused')} onClick={() => navigate(paths.runs(p.id))}>
                            <td>
                              <div className="bk-pl-name">
                                <Switch
                                  size="sm"
                                  checked={p.enabled !== false}
                                  label={p.enabled === false ? `Enable ${p.name}` : `Pause ${p.name}`}
                                  disabled={!session.can('pipelines.edit') || p.source === 'git' || busyId === p.id}
                                  onChange={() => void toggle(p)}
                                />
                                <div>
                                  <Link to={paths.runs(p.id)} onClick={(e) => e.stopPropagation()} className="bk-pl-title">
                                    {p.name}
                                  </Link>
                                  <div className="bk-pl-meta">
                                    {p.draft && (
                                      <Badge tone="warning" title="Not scheduled and cannot run until it is published">
                                        Draft
                                      </Badge>
                                    )}
                                    {p.source === 'git' && <Badge tone="accent">Git</Badge>}
                                    <span className="bk-pl-desc">{p.description || 'No description'}</span>
                                    {(p.tags ?? []).slice(0, 3).map((t) => (
                                      <span key={t} className="bk-tag">
                                        {t}
                                      </span>
                                    ))}
                                  </div>
                                </div>
                              </div>
                            </td>
                            <td>{status ? <StatusBadge status={status} /> : <span className="bk-muted">Never run</span>}</td>
                            <td>
                              <RunHistory history={p.run_history} />
                            </td>
                            <td>
                              {p.schedule ? (
                                <div className="bk-pl-schedule">
                                  <code>{p.schedule}</code>
                                  {next && (
                                    <small title={formatDateTime(next)}>
                                      Next {toDate(next) && toDate(next)!.getTime() < now ? 'overdue' : formatRelative(next, now)}
                                    </small>
                                  )}
                                </div>
                              ) : (
                                <span className="bk-muted">Manual</span>
                              )}
                            </td>
                            <td>
                              {p.last_run_at ? (
                                <span title={formatDateTime(p.last_run_at)}>{formatRelative(p.last_run_at, now)}</span>
                              ) : (
                                <span className="bk-muted">No runs yet</span>
                              )}
                            </td>
                            <td className="is-num">{p.node_count ?? 0}</td>
                            <td>
                              <div className="bk-pl-actions" onClick={(e) => e.stopPropagation()}>
                                {session.can('pipelines.run') && (
                                  <Button
                                    size="sm"
                                    icon={<Play size={14} aria-hidden="true" />}
                                    disabled={Boolean(runBlocked) || busyId === p.id}
                                    title={runBlocked || `Run ${p.name} now`}
                                    onClick={() => void run(p)}
                                  >
                                    Run
                                  </Button>
                                )}
                                <Menu label={`Actions for ${p.name}`} items={menuFor(p)} />
                              </div>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
                <Pagination page={current} pageSize={PAGE_SIZE} total={filtered.length} onPage={setPage} label="pipelines" />
              </>
            )}
          </section>
        )
      )}

      {creating && <CreatePipelineModal onClose={() => setCreating(false)} />}
      {deleting && <DeletePipelineFlow pipeline={deleting} onClose={() => setDeleting(null)} />}
    </Page>
  )
}

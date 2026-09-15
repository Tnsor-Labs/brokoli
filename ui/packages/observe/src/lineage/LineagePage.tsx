import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { MarkerType, ReactFlowProvider, useNodesState, useReactFlow, type Edge } from '@xyflow/react'
import { Cog, Database, FileText, Globe, RotateCcw, Waypoints, X } from 'lucide-react'
import { observeApi, type LineageNode } from '@brokoli/api'
import { paths } from '@brokoli/pipelines'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  IconButton,
  Page,
  PageHeader,
  SearchInput,
  Skeleton,
  cx,
  errorMessage,
  formatDateTime,
  formatNumber,
  formatRelative,
  type Tone,
} from '@brokoli/ui'
import { CARD_HEIGHT, CARD_WIDTH, FlowSurface, type CardNode } from '../graph/FlowSurface'
import { layeredLayout } from '../graph/layout'
import { buildModel, columnGroups, isAsset, kindOf, pipelinesOf, type LineageModel } from './model'

const LAYOUT = { colGap: 300, rowGap: 84, align: 'center' as const }

function iconFor(n: LineageNode) {
  const props = { size: 16, 'aria-hidden': true }
  if (n.type === 'file') return <FileText {...props} />
  if (n.type === 'table') return <Database {...props} />
  if (n.type === 'api') return <Globe {...props} />
  return <Cog {...props} />
}

function toCards(model: LineageModel): CardNode[] {
  const positions = layeredLayout(model.nodes, model.edges, LAYOUT)
  return model.nodes.map((n) => {
    const kind = kindOf(n)
    return {
      id: n.id,
      type: 'card',
      position: positions.get(n.id) ?? { x: 0, y: 0 },
      data: {
        title: n.name || n.id,
        subtitle: n.type === 'processing' && n.pipeline ? `${kind.label} in ${n.pipeline}` : kind.label,
        icon: iconFor(n),
        family: kind.family,
        mini: kind.mini,
        dashed: n.type === 'processing',
        hint: `${n.name}\n${n.id}`,
      },
    }
  })
}

const pct = (v: number | undefined) => (v === undefined || !Number.isFinite(v) ? '-' : `${v.toFixed(1)}%`)

/*
 * How a column edge was established (core ADR-039), instead of the constant
 * 0.7 that used to read as a confidence. Declared and attested are facts;
 * inferred is a name match, labelled as the guess it is.
 */
const EVIDENCE: Record<string, { label: string; tone: Tone }> = {
  declared: { label: 'Declared', tone: 'success' },
  attested: { label: 'Attested', tone: 'success' },
  parsed: { label: 'Parsed from SQL', tone: 'accent' },
  inferred: { label: 'Name match', tone: 'warning' },
}
const evidenceMeta = (e: string | undefined) => EVIDENCE[e ?? ''] ?? { label: e || 'inferred', tone: 'neutral' as Tone }

/*
 * The opaque-step message: the step's own reason for why its columns can't
 * be traced, followed by the reassurance that its inputs and outputs still
 * are. The reason comes from the backend and may not end in a full stop, so
 * close it before the next sentence to avoid a run-on ("...unknown Which").
 */
const OPAQUE_FALLBACK = 'This step cannot say which output column came from which input, so no column edges are drawn through it.'
function opaqueMessage(reason: string | undefined) {
  const first = (reason?.trim() || OPAQUE_FALLBACK).replace(/([^.!?])$/, '$1.')
  return `${first} Which datasets it read and wrote is still shown.`
}

function Detail({ model, id, onSelect, onClose }: { model: LineageModel; id: string; onSelect: (id: string) => void; onClose: () => void }) {
  const node = model.byId.get(id)
  if (!node) return null
  const kind = kindOf(node)
  const meta = node.metadata
  const columns = meta?.columns ?? []
  const used = pipelinesOf(model, id)
  const groups = columnGroups(model, id)
  const up = [...new Set(model.upstream.get(id) ?? [])]
  const down = [...new Set(model.downstream.get(id) ?? [])]
  const name = (nid: string) => model.byId.get(nid)?.name || nid

  const neighbours = (title: string, ids: string[]) => (
    <section>
      <h3>
        {title} ({ids.length})
      </h3>
      {ids.length ? (
        <ul className="ob-list">
          {ids.map((nid) => {
            const n = model.byId.get(nid)
            return (
              <li key={nid}>
                <button type="button" onClick={() => onSelect(nid)} title={nid}>
                  <span className="ob-dot" style={{ ['--family' as string]: n ? kindOf(n).family : 'var(--bk-color-text-muted)' }} />
                  <span className="ob-grow">{name(nid)}</span>
                  <small>{n ? kindOf(n).label : ''}</small>
                </button>
              </li>
            )
          })}
        </ul>
      ) : (
        <p className="ob-muted">None.</p>
      )}
    </section>
  )

  return (
    <aside className="ob-detail" aria-label={`Details of ${node.name}`}>
      <div className="ob-detail-head">
        <div>
          <span className="ob-kind" style={{ ['--family' as string]: kind.family }}>
            <i />
            {kind.label}
          </span>
          <h2>{node.name || node.id}</h2>
        </div>
        <IconButton size="sm" label="Close details" onClick={onClose}>
          <X size={15} aria-hidden="true" />
        </IconButton>
      </div>
      <section>
        <h3>{isAsset(node) ? 'Asset id' : 'Step id'}</h3>
        <code className="ob-id">{node.id}</code>
        {node.type === 'processing' && node.pipeline_id && (
          <div className="ob-links">
            <Link className="bk-button bk-button-secondary bk-button-sm" to={paths.editor(node.pipeline_id)}>
              Open in editor
            </Link>
            <Link className="bk-button bk-button-ghost bk-button-sm" to={paths.runs(node.pipeline_id)}>
              View runs
            </Link>
          </div>
        )}
      </section>
      <section>
        <h3>Observed data</h3>
        {meta ? (
          <>
            <p>
              From the latest profile
              {meta.observed_at && (
                <>
                  , taken <time title={formatDateTime(meta.observed_at)}>{formatRelative(meta.observed_at)}</time>
                </>
              )}
              .
            </p>
            <dl className="ob-facts">
              <div>
                <dt>Rows</dt>
                <dd>{formatNumber(meta.row_count ?? 0)}</dd>
              </div>
              <div>
                <dt>Columns</dt>
                <dd>{formatNumber(meta.column_count ?? columns.length)}</dd>
              </div>
            </dl>
            {columns.length > 0 && (
              <div className="ob-columns">
                <table>
                  <thead>
                    <tr>
                      <th scope="col">Column</th>
                      <th scope="col">Type</th>
                      <th scope="col">Null</th>
                      <th scope="col">Unique</th>
                      <th scope="col">Min</th>
                      <th scope="col">Max</th>
                    </tr>
                  </thead>
                  <tbody>
                    {columns.map((c) => (
                      <tr key={c.name}>
                        <td>{c.name}</td>
                        <td>{c.type || 'unknown'}</td>
                        <td className="is-num">{pct(c.null_pct)}</td>
                        <td className="is-num">{pct(c.unique_pct)}</td>
                        <td title={c.min_val}>{c.min_val ?? '-'}</td>
                        <td title={c.max_val}>{c.max_val ?? '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        ) : (
          <p className="ob-muted">No profile has been recorded for this {isAsset(node) ? 'asset' : 'step'} yet. Profiles are saved when a run with profiling reaches it.</p>
        )}
      </section>
      {node.columns_opaque && (
        <section>
          <Callout tone="info" title="Opaque to column lineage">
            {opaqueMessage(node.opaque_reason)}
          </Callout>
        </section>
      )}
      {groups.length > 0 && (
        <section>
          <h3>Column mappings</h3>
          <p className="ob-muted">How each column got here, and how the graph knows: declared in the pipeline or attested by a run are exact; a name match is a guess.</p>
          {groups.map((g) => (
            <div key={`${g.direction}|${g.neighbour}`} className="ob-mapping">
              <header>
                <small className="ob-muted">{g.direction === 'upstream' ? 'From' : 'To'}</small>
                <button type="button" onClick={() => onSelect(g.neighbour)} title={g.neighbour}>
                  {name(g.neighbour)}
                </button>
              </header>
              <ul>
                {g.mappings.map((m) => (
                  <li key={`${m.from_column}>${m.to_column}`}>
                    <span className="ob-map-cols">
                      {m.from_column} &rarr; {m.to_column}
                    </span>
                    <Badge tone={evidenceMeta(m.evidence).tone}>{evidenceMeta(m.evidence).label}</Badge>
                    {m.mapping_reason && <span className="ob-map-reason">{m.mapping_reason}</span>}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </section>
      )}
      <section>
        <h3>Pipelines ({used.length})</h3>
        {used.length ? (
          <ul className="ob-list">
            {used.map((p) => (
              <li key={p.id}>
                <Link to={paths.runs(p.id)}>
                  <span className="ob-grow">{p.name}</span>
                  <small>Runs</small>
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <p className="ob-muted">No pipeline reads or writes this asset through a connected step.</p>
        )}
      </section>
      {neighbours('Upstream', up)}
      {neighbours('Downstream', down)}
    </aside>
  )
}

function LineageMap({ model }: { model: LineageModel }) {
  const flow = useReactFlow<CardNode, Edge>()
  const initial = useMemo(() => toCards(model), [model])
  const [nodes, setNodes, onNodesChange] = useNodesState<CardNode>(initial)
  const [hovered, setHovered] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const findRef = useRef<HTMLDivElement>(null)

  useEffect(() => setNodes(initial), [initial, setNodes])

  const selectedId = nodes.find((n) => n.selected)?.id ?? null
  const related = useMemo(
    () => (selectedId ? new Set([...(model.upstream.get(selectedId) ?? []), ...(model.downstream.get(selectedId) ?? [])]) : null),
    [model, selectedId],
  )

  const shownNodes = useMemo(
    () =>
      nodes.map((n) => {
        const emphasis = !related || n.id === selectedId ? undefined : related.has(n.id) ? ('related' as const) : ('dim' as const)
        return emphasis === n.data.emphasis ? n : { ...n, data: { ...n.data, emphasis } }
      }),
    [nodes, related, selectedId],
  )

  const edges = useMemo<Edge[]>(
    () =>
      model.edges.map((e) => {
        const touching = selectedId !== null && (e.from === selectedId || e.to === selectedId)
        const label = e.pipelines.map((p) => p.name).join(', ')
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          selectable: false,
          focusable: false,
          className: cx('ob-edge', touching && 'is-active', selectedId !== null && !touching && 'is-dim'),
          label: touching || hovered === e.id ? label : undefined,
          labelBgPadding: [6, 3] as [number, number],
          labelBgBorderRadius: 4,
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: touching ? 'var(--bk-color-edge-active)' : 'var(--bk-color-edge)' },
          ariaLabel: `${model.byId.get(e.from)?.name} to ${model.byId.get(e.to)?.name}${label ? `, in ${label}` : ''}`,
        }
      }),
    [model, selectedId, hovered],
  )

  const select = (id: string | null, centre = false) => {
    setNodes((all) => all.map((n) => (Boolean(n.selected) === (n.id === id) ? n : { ...n, selected: n.id === id })))
    if (id && centre) {
      const n = flow.getNode(id)
      if (n) void flow.setCenter(n.position.x + CARD_WIDTH / 2, n.position.y + CARD_HEIGHT / 2, { zoom: Math.max(flow.getZoom(), 0.9), duration: 350 })
    }
  }

  const reset = () => {
    setNodes(initial.map((n) => ({ ...n, selected: false })))
    setHovered(null)
    requestAnimationFrame(() => void flow.fitView({ padding: 0.15, maxZoom: 1, duration: 300 }))
  }

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return []
    return model.nodes.filter((n) => `${n.name} ${n.id} ${n.pipeline ?? ''}`.toLowerCase().includes(q)).slice(0, 8)
  }, [model, query])

  const pick = (id: string) => {
    setQuery('')
    select(id, true)
  }

  const assets = model.nodes.filter(isAsset).length
  return (
    <div className="ob-graph-card">
      <div className="ob-graph-toolbar">
        <ul className="ob-counts" aria-label="Map size">
          <li>
            <strong>{assets}</strong> {assets === 1 ? 'asset' : 'assets'}
          </li>
          <li>
            <strong>{model.nodes.length - assets}</strong> {model.nodes.length - assets === 1 ? 'step' : 'steps'}
          </li>
          <li>
            <strong>{model.edges.length}</strong> {model.edges.length === 1 ? 'connection' : 'connections'}
          </li>
          <li>
            <strong>{model.columnEdges.length}</strong> column {model.columnEdges.length === 1 ? 'mapping' : 'mappings'}
          </li>
        </ul>
        <ul className="ob-legend" aria-label="Legend">
          {[
            ['Files and APIs', 'var(--bk-color-tax-source)'],
            ['Tables', 'var(--bk-color-tax-output)'],
            ['Processing steps', 'var(--bk-color-tax-processing)'],
          ].map(([label, family]) => (
            <li key={label}>
              <span className="ob-legend-swatch" style={{ ['--family' as string]: family }} />
              {label}
            </li>
          ))}
        </ul>
        <div className="ob-toolbar-end">
          <div
            className="ob-find"
            ref={findRef}
            onBlur={(e) => {
              if (!findRef.current?.contains(e.relatedTarget as globalThis.Node | null)) setQuery('')
            }}
          >
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder="Find an asset or step"
              onKeyDown={(e) => {
                if (e.key === 'Enter' && matches[0]) pick(matches[0].id)
              }}
            />
            {query.trim() && (
              <ul className="ob-find-results" aria-label="Matching nodes">
                {matches.length ? (
                  matches.map((n) => (
                    <li key={n.id}>
                      <button type="button" onClick={() => pick(n.id)}>
                        <span>{n.name || n.id}</span>
                        <small>
                          {kindOf(n).label}
                          {n.pipeline ? ` in ${n.pipeline}` : ''}
                        </small>
                      </button>
                    </li>
                  ))
                ) : (
                  <li className="ob-find-empty">Nothing matches.</li>
                )}
              </ul>
            )}
          </div>
          <Button size="sm" variant="secondary" icon={<RotateCcw size={14} aria-hidden="true" />} onClick={reset}>
            Reset layout
          </Button>
        </div>
      </div>
      <div className="ob-graph-body">
        <FlowSurface
          nodes={shownNodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgeMouseEnter={(_, e) => setHovered(e.id)}
          onEdgeMouseLeave={() => setHovered(null)}
          aria-label="Lineage map"
        />
        {selectedId && <Detail model={model} id={selectedId} onSelect={(id) => select(id, true)} onClose={() => select(null)} />}
      </div>
    </div>
  )
}

/*
 * Data lineage across every pipeline: the files, tables and APIs pipelines
 * read and write, and the steps in between. A request error is shown as an
 * error, never as an empty map (the previous page read any failed response
 * as "no lineage").
 */
export function LineagePage() {
  const navigate = useNavigate()
  const query = useQuery({ queryKey: ['observe', 'lineage'], queryFn: observeApi.lineage })
  const model = useMemo(() => (query.data ? buildModel(query.data) : null), [query.data])
  return (
    <Page wide>
      <PageHeader
        eyebrow="Observe"
        title="Data lineage"
        description="The files, tables and APIs your pipelines read and write, and the steps in between. Select anything to trace where its data comes from and where it goes."
      />
      {query.isError ? (
        <Callout tone="danger" title="Lineage could not be loaded" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : !model ? (
        <div className="ob-graph-card ob-state-loading" aria-busy="true">
          <Skeleton height={22} width="40%" />
          <Skeleton height={360} />
        </div>
      ) : !model.nodes.length ? (
        <div className="ob-empty">
          <EmptyState
            icon={<Waypoints size={20} aria-hidden="true" />}
            title="No lineage yet"
            action={<Button onClick={() => navigate(paths.list)}>Go to pipelines</Button>}
          >
            Lineage is drawn from pipeline definitions. Add a pipeline with a source and a sink, and the assets it connects appear here.
          </EmptyState>
        </div>
      ) : (
        <ReactFlowProvider>
          <LineageMap model={model} />
        </ReactFlowProvider>
      )}
    </Page>
  )
}

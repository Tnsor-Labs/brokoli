import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { MarkerType, ReactFlowProvider, useNodesState, useReactFlow, type Edge } from '@xyflow/react'
import { GitBranch, Network, X } from 'lucide-react'
import { observeApi, type DependencyGraph, type DependencyMode, type DependencyState } from '@brokoli/api'
import { paths } from '@brokoli/pipelines'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  IconButton,
  Page,
  PageHeader,
  Skeleton,
  Spinner,
  StatusBadge,
  Switch,
  cx,
  errorMessage,
  formatDateTime,
  formatRelative,
} from '@brokoli/ui'
import { CARD_HEIGHT, CARD_WIDTH, FlowSurface, type CardNode } from '../graph/FlowSurface'
import { layeredLayout, reachable } from '../graph/layout'

type DepEdge = { from: string; to: string; state: DependencyState; mode: DependencyMode }
type Model = { names: Map<string, string>; edges: DepEdge[]; participants: Set<string>; truncated: boolean }

/** Edge labels: the state the upstream run must reach, in words rather than glyphs that read like outcomes. */
const ON_STATE: Record<string, string> = { succeeded: 'on success', completed: 'on completion', failed: 'on failure' }
const NEEDS_STATE: Record<string, string> = { succeeded: 'to succeed', completed: 'to finish, whatever the outcome', failed: 'to fail' }
const onState = (s: string) => ON_STATE[s] ?? `on ${s}`
const modeLabel = (m: string) => (m === 'trigger' ? 'Trigger' : 'Gate')

function buildModel(graph: DependencyGraph): Model {
  const names = new Map<string, string>()
  for (const n of graph.nodes ?? []) if (!names.has(n.id)) names.set(n.id, n.name || n.id)
  const seen = new Set<string>()
  const edges: DepEdge[] = []
  for (const e of graph.edges ?? []) {
    const key = `${e.from}>${e.to}`
    if (seen.has(key) || !names.has(e.from) || !names.has(e.to)) continue
    seen.add(key)
    edges.push({ from: e.from, to: e.to, state: e.state || 'succeeded', mode: e.mode || 'gate' })
  }
  const participants = new Set(edges.flatMap((e) => [e.from, e.to]))
  return { names, edges, participants, truncated: Boolean(graph.truncated) }
}

function Detail({ model, id, onSelect, onClose }: { model: Model; id: string; onSelect: (id: string) => void; onClose: () => void }) {
  const status = useQuery({ queryKey: ['observe', 'dependency-status', id], queryFn: () => observeApi.dependencyStatus(id) })
  const name = model.names.get(id) ?? id
  const downstream = model.edges.filter((e) => e.from === id)
  const deps = status.data?.deps ?? []
  return (
    <aside className="ob-detail" aria-label={`Dependencies of ${name}`}>
      <div className="ob-detail-head">
        <div>
          <span className="ob-kind" style={{ ['--family' as string]: 'var(--bk-color-accent)' }}>
            <i />
            Pipeline
          </span>
          <h2>{name}</h2>
        </div>
        <IconButton size="sm" label="Close details" onClick={onClose}>
          <X size={15} aria-hidden="true" />
        </IconButton>
      </div>
      <section>
        <code className="ob-id">{id}</code>
        <div className="ob-links">
          <Link className="bk-button bk-button-secondary bk-button-sm" to={paths.editor(id)}>
            Open in editor
          </Link>
          <Link className="bk-button bk-button-ghost bk-button-sm" to={paths.runs(id)}>
            View runs
          </Link>
        </div>
      </section>
      <section>
        <h3>Waits for</h3>
        {status.isPending ? (
          <p className="ob-muted">
            <Spinner size="sm" /> Checking each upstream rule
          </p>
        ) : status.isError ? (
          <Callout tone="danger" title="Could not check the rules" action={<Button size="sm" onClick={() => void status.refetch()}>Try again</Button>}>
            {errorMessage(status.error)}
          </Callout>
        ) : !deps.length ? (
          <p className="ob-muted">This pipeline does not depend on any other pipeline.</p>
        ) : (
          <>
            {/* The overall reason repeats the per-rule reasons below, so only the verdict is shown here. */}
            <p>{status.data.satisfied ? <Badge tone="success">All rules met right now</Badge> : <Badge tone="warning">Not all rules are met</Badge>}</p>
            {deps.map((d) => (
              <div key={d.pipeline_id} className={cx('ob-rule', !d.satisfied && 'is-unmet')}>
                <header>
                  {model.names.has(d.pipeline_id) ? (
                    <button type="button" onClick={() => onSelect(d.pipeline_id)}>
                      {d.name || model.names.get(d.pipeline_id)}
                    </button>
                  ) : (
                    <strong>{d.name || d.pipeline_id}</strong>
                  )}
                  <Badge tone={d.mode === 'trigger' ? 'accent' : 'running'}>{modeLabel(d.mode)}</Badge>
                  <Badge tone={d.satisfied ? 'success' : 'warning'}>{d.satisfied ? 'Met' : 'Not met'}</Badge>
                </header>
                <p>
                  {d.missing
                    ? 'This upstream pipeline no longer exists, so the rule can never be met.'
                    : `${d.mode === 'trigger' ? 'Starts this pipeline when it runs' : 'Holds this pipeline until it runs'} and needs it ${NEEDS_STATE[d.state] ?? d.state}.`}
                </p>
                {d.reason && <p className="ob-muted">{d.reason}</p>}
                {(d.last_status || d.last_run_at) && (
                  <div className="ob-rule-meta">
                    Last run
                    {d.last_status && <StatusBadge status={d.last_status} />}
                    {d.last_run_at && <time title={formatDateTime(d.last_run_at)}>{formatRelative(d.last_run_at)}</time>}
                  </div>
                )}
              </div>
            ))}
          </>
        )}
      </section>
      <section>
        <h3>Depended on by ({downstream.length})</h3>
        {downstream.length ? (
          <ul className="ob-list">
            {downstream.map((e) => (
              <li key={e.to}>
                <button type="button" onClick={() => onSelect(e.to)}>
                  <span className="ob-grow">{model.names.get(e.to)}</span>
                  <small>
                    {modeLabel(e.mode)}, {onState(e.state)}
                  </small>
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="ob-muted">No pipeline depends on this one.</p>
        )}
      </section>
    </aside>
  )
}

function DependencyMap({ model }: { model: Model }) {
  const flow = useReactFlow<CardNode, Edge>()
  const [showAll, setShowAll] = useState(false)
  const initial = useMemo<CardNode[]>(() => {
    const visible = [...model.names].filter(([id]) => showAll || model.participants.has(id)).map(([id, name]) => ({ id, name }))
    const positions = layeredLayout(visible, model.edges, { colGap: 340, rowGap: 82, align: 'top' })
    return visible.map((n) => {
      const up = model.edges.filter((e) => e.to === n.id).length
      const down = model.edges.filter((e) => e.from === n.id).length
      return {
        id: n.id,
        type: 'card',
        position: positions.get(n.id) ?? { x: 0, y: 0 },
        data: {
          title: n.name,
          subtitle: up || down ? `Waits for ${up}, needed by ${down}` : 'No dependencies',
          icon: <GitBranch size={16} aria-hidden="true" />,
          family: model.participants.has(n.id) ? 'var(--bk-color-accent)' : 'var(--bk-color-text-muted)',
          mini: model.participants.has(n.id) ? 'ob-mini-accent' : 'ob-mini-other',
          hint: `${n.name}\n${n.id}`,
        },
      }
    })
  }, [model, showAll])
  const [nodes, setNodes, onNodesChange] = useNodesState<CardNode>(initial)
  useEffect(() => {
    setNodes(initial)
    requestAnimationFrame(() => void flow.fitView({ padding: 0.15, maxZoom: 1, duration: 200 }))
  }, [initial, setNodes, flow])

  const selectedId = nodes.find((n) => n.selected)?.id ?? null
  // The whole chain: everything the selection waits on, and everything waiting on it.
  const chain = useMemo(() => {
    if (!selectedId) return null
    return { up: reachable(selectedId, model.edges, 'up'), down: reachable(selectedId, model.edges, 'down') }
  }, [model, selectedId])

  const shownNodes = useMemo(
    () =>
      nodes.map((n) => {
        const emphasis = !chain || n.id === selectedId ? undefined : chain.up.has(n.id) || chain.down.has(n.id) ? ('related' as const) : ('dim' as const)
        return emphasis === n.data.emphasis ? n : { ...n, data: { ...n.data, emphasis } }
      }),
    [nodes, chain, selectedId],
  )

  const edges = useMemo<Edge[]>(
    () =>
      model.edges.map((e) => {
        const inUp = (id: string) => id === selectedId || Boolean(chain?.up.has(id))
        const inDown = (id: string) => id === selectedId || Boolean(chain?.down.has(id))
        const active = Boolean(chain) && ((inUp(e.from) && inUp(e.to)) || (inDown(e.from) && inDown(e.to)))
        const trigger = e.mode === 'trigger'
        return {
          id: `${e.from}>${e.to}`,
          source: e.from,
          target: e.to,
          selectable: false,
          focusable: false,
          label: onState(e.state),
          labelBgPadding: [6, 3] as [number, number],
          labelBgBorderRadius: 4,
          className: cx('ob-edge', trigger ? 'ob-dep-trigger' : 'ob-dep-gate', active && 'is-active', chain && !active && 'is-dim'),
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: trigger ? 'var(--bk-color-tax-integration)' : 'var(--bk-color-running)' },
          ariaLabel: `${model.names.get(e.to)} ${trigger ? 'is started by' : 'waits for'} ${model.names.get(e.from)} ${onState(e.state)}`,
        }
      }),
    [model, chain, selectedId],
  )

  const select = (id: string | null, centre = false) => {
    setNodes((all) => all.map((n) => (Boolean(n.selected) === (n.id === id) ? n : { ...n, selected: n.id === id })))
    if (id && centre) {
      const n = flow.getNode(id)
      if (n) void flow.setCenter(n.position.x + CARD_WIDTH / 2, n.position.y + CARD_HEIGHT / 2, { zoom: Math.max(flow.getZoom(), 0.9), duration: 350 })
    }
  }

  const independent = model.names.size - model.participants.size
  return (
    <div className="ob-graph-card">
      <div className="ob-graph-toolbar">
        <ul className="ob-counts" aria-label="Map size">
          <li>
            <strong>{model.participants.size}</strong> connected {model.participants.size === 1 ? 'pipeline' : 'pipelines'}
          </li>
          <li>
            <strong>{model.edges.length}</strong> {model.edges.length === 1 ? 'dependency' : 'dependencies'}
          </li>
          <li>
            <strong>{independent}</strong> independent
          </li>
        </ul>
        <ul className="ob-legend" aria-label="Legend">
          <li>
            <i aria-hidden="true" />
            Gate: held until the upstream run reaches the state
          </li>
          <li>
            <i className="is-trigger" aria-hidden="true" />
            Trigger: started when the upstream run reaches it
          </li>
        </ul>
        <div className="ob-toolbar-end">
          {independent > 0 && (
            <span className="ob-toggle">
              <Switch size="sm" checked={showAll} onChange={setShowAll} label="Show independent pipelines" />
              Show independent pipelines
            </span>
          )}
        </div>
      </div>
      <div className="ob-graph-body">
        <FlowSurface nodes={shownNodes} edges={edges} onNodesChange={onNodesChange} nodesDraggable aria-label="Pipeline dependency map" />
        {selectedId && <Detail model={model} id={selectedId} onSelect={(id) => select(id, true)} onClose={() => select(null)} />}
      </div>
    </div>
  )
}

/*
 * Which pipelines wait for, or are started by, which others. Upstream
 * pipelines sit to the left. Selecting one highlights its whole chain in
 * both directions and checks each of its rules against the latest runs.
 */
export function DependenciesPage() {
  const navigate = useNavigate()
  const query = useQuery({ queryKey: ['observe', 'dependency-graph'], queryFn: observeApi.dependencyGraph })
  const model = useMemo(() => (query.data ? buildModel(query.data) : null), [query.data])
  return (
    <Page wide>
      <PageHeader
        eyebrow="Observe"
        title="Pipeline dependencies"
        description="Which pipelines wait for or start others. Select a pipeline to follow its chain and check whether its rules are met right now."
      />
      {query.isError ? (
        <Callout tone="danger" title="Dependencies could not be loaded" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : !model ? (
        <div className="ob-graph-card ob-state-loading" aria-busy="true">
          <Skeleton height={22} width="40%" />
          <Skeleton height={360} />
        </div>
      ) : (
        <>
          {model.truncated && (
            <Callout tone="warning" title="Only part of the map is shown">
              The server stops at 2000 pipelines, so some pipelines and the dependencies between them are missing, and which ones is not predictable.
            </Callout>
          )}
          {!model.names.size ? (
            <div className="ob-empty">
              <EmptyState icon={<Network size={20} aria-hidden="true" />} title="No pipelines yet" action={<Button onClick={() => navigate(paths.list)}>Go to pipelines</Button>}>
                Create pipelines, then make one wait for or start another to see the relationships here.
              </EmptyState>
            </div>
          ) : !model.edges.length ? (
            <div className="ob-empty">
              <EmptyState icon={<Network size={20} aria-hidden="true" />} title="No pipeline depends on another" action={<Button onClick={() => navigate(paths.list)}>Go to pipelines</Button>}>
                {`You have ${model.names.size} ${model.names.size === 1 ? 'pipeline' : 'pipelines'}, all independent. To make one wait for or be started by another, open it in the editor and add a dependency in its pipeline settings.`}
              </EmptyState>
            </div>
          ) : (
            <ReactFlowProvider>
              <DependencyMap model={model} />
            </ReactFlowProvider>
          )}
        </>
      )}
    </Page>
  )
}

import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useBlocker, useNavigate } from 'react-router-dom'
import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  applyNodeChanges,
  useReactFlow,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import {
  CheckCheck,
  Code2,
  Copy,
  Download,
  Eye,
  GitBranch,
  History,
  LayoutGrid,
  Lock,
  Play,
  Redo2,
  Rocket,
  Settings2,
  Undo2,
  Workflow,
} from 'lucide-react'
import { ApiError, downloadText, pipelineApi, type DryRunResponse, type NodeIssue, type Pipeline, type PipelineEdge } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Button, Callout, ConfirmDialog, IconButton, Kbd, Menu, Modal, PageLoading, cx, errorMessage, formatRelative, useToast } from '@brokoli/ui'
import { catalogEntry } from '../catalog'
import {
  autoLayout,
  buildSavePayload,
  connectionProblem,
  createNode,
  duplicateNode,
  nextFreePosition,
  nodeWarnings,
  NODE_HEIGHT,
  NODE_WIDTH,
} from '../document'
import { nodeTypes, type FlowNode } from '../NodeCard'
import { edgeId, toFlowEdges } from '../flow'
import { NodeInspector } from '../forms/NodeInspector'
import { CodeViewModal } from '../panels/CodeViewModal'
import { Overview } from '../panels/Overview'
import { Palette, PALETTE_MIME } from '../panels/Palette'
import { PreviewPanel } from '../panels/PreviewPanel'
import { SchedulePopover } from '../panels/SchedulePopover'
import { SettingsDrawer } from '../panels/SettingsDrawer'
import { VersionsPanel } from '../panels/VersionsPanel'
import { useDocument } from './useDocument'
import '../graph.css'
import './editor.css'

/** Same key the pipelines package uses, so the runs page and the editor share one cached copy. */
const pipelineKey = (id: string) => ['pipeline', id] as const

type Selection = { kind: 'node' | 'edge'; id: string } | null
type Side = 'inspector' | 'versions'

export function PipelineEditorPage({ pipelineId }: { pipelineId: string }) {
  return (
    <ReactFlowProvider>
      <Editor pipelineId={pipelineId} />
    </ReactFlowProvider>
  )
}

function isTyping(target: EventTarget | null) {
  const el = target as HTMLElement | null
  return Boolean(el?.closest('input, textarea, select, [contenteditable="true"], .cm-editor'))
}

function Editor({ pipelineId }: { pipelineId: string }) {
  const session = useSession()
  const toast = useToast()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const flow = useReactFlow()
  const canvasRef = useRef<HTMLDivElement>(null)
  const loaded = useQuery({ queryKey: pipelineKey(pipelineId), queryFn: () => pipelineApi.get(pipelineId), staleTime: Infinity, refetchOnWindowFocus: false })
  const { doc, docRef, change, undo, redo, reset, saved, revision, dirty, canUndo, canRedo } = useDocument(loaded.data)

  const [selection, setSelection] = useState<Selection>(null)
  const [side, setSide] = useState<Side>('inspector')
  const [issues, setIssues] = useState<Record<string, NodeIssue>>({})
  const [busy, setBusy] = useState<null | 'save' | 'publish' | 'run' | 'preview' | 'validate' | 'clone'>(null)
  const [banner, setBanner] = useState<{ tone: 'danger' | 'warning' | 'success'; title: string; body?: string } | null>(null)
  const [preview, setPreview] = useState<DryRunResponse | null>(null)
  const [showSettings, setShowSettings] = useState(false)
  const [showCode, setShowCode] = useState(false)
  const [branch, setBranch] = useState<{ from: string; to: string } | null>(null)
  const [confirmClone, setConfirmClone] = useState(false)
  const [flowNodes, setFlowNodes] = useState<FlowNode[]>([])

  const pipeline = doc?.pipeline
  const readonlyReason = !pipeline
    ? ''
    : pipeline.source === 'git'
      ? 'This pipeline is managed in Git. Change it in its repository; edits here cannot be saved.'
      : !session.can('pipelines.edit')
        ? 'Your role can view this pipeline but not change it.'
        : ''
  const readonly = Boolean(readonlyReason)

  // Warn before closing the tab or reloading with unsaved work.
  useEffect(() => {
    if (!dirty) return
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname)

  const nodeById = useMemo(() => new Map((doc?.nodes ?? []).map((n) => [n.id, n])), [doc?.nodes])
  const issueFor = useCallback(
    (id: string) => {
      const node = nodeById.get(id)
      if (!node || !doc) return undefined
      const server = issues[id]
      const local = nodeWarnings(node, doc.edges)
      const errors = server?.errors ?? []
      const warnings = [...(server?.warnings ?? []), ...local]
      if (errors.length) return { level: 'error' as const, messages: [...errors, ...warnings] }
      if (warnings.length) return { level: 'warning' as const, messages: warnings }
      return undefined
    },
    [nodeById, issues, doc],
  )

  // React Flow keeps measured sizes on its node objects, so node view state is kept and patched rather than rebuilt.
  useEffect(() => {
    if (!doc) return
    setFlowNodes((prev) => {
      const byId = new Map(prev.map((n) => [n.id, n]))
      return doc.nodes.map((node) => {
        const previous = byId.get(node.id)
        const data = { node, issue: issueFor(node.id), readonly }
        const selected = selection?.kind === 'node' && selection.id === node.id
        return previous ? { ...previous, position: node.position, data, selected } : { id: node.id, type: 'brokoli' as const, position: node.position, data, selected }
      })
    })
  }, [doc, issueFor, selection, readonly])

  const flowEdges = useMemo(() => toFlowEdges(doc?.edges ?? [], undefined, selection?.kind === 'edge' ? selection.id : null), [doc?.edges, selection])

  const selectedNode = selection?.kind === 'node' ? (nodeById.get(selection.id) ?? null) : null

  // ---- document operations -------------------------------------------------
  const dropIssue = (id: string) =>
    setIssues((all) => {
      if (!(id in all)) return all
      const next = { ...all }
      delete next[id]
      return next
    })

  const addNode = (type: string, at?: { x: number; y: number }) => {
    if (!doc || readonly) return
    let position = at
    if (!position) {
      const rect = canvasRef.current?.getBoundingClientRect()
      const centre = rect ? flow.screenToFlowPosition({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }) : undefined
      position = nextFreePosition(doc.nodes, centre && { x: centre.x - NODE_WIDTH / 2, y: centre.y - NODE_HEIGHT / 2 })
    }
    const node = createNode(type, position, doc.nodes.map((n) => n.id))
    change((d) => ({ ...d, nodes: [...d.nodes, node] }))
    setSelection({ kind: 'node', id: node.id })
    setSide('inspector')
  }

  const removeSelection = () => {
    if (!selection || readonly) return
    if (selection.kind === 'node') {
      const id = selection.id
      change((d) => ({ ...d, nodes: d.nodes.filter((n) => n.id !== id), edges: d.edges.filter((e) => e.from !== id && e.to !== id) }))
      dropIssue(id)
    } else {
      const index = flowEdges.find((e) => e.id === selection.id)?.data?.index
      if (index === undefined) return
      change((d) => ({ ...d, edges: d.edges.filter((_, i) => i !== index) }))
    }
    setSelection(null)
  }

  const duplicateSelected = () => {
    if (!selectedNode || readonly || !doc) return
    const copy = duplicateNode(selectedNode, doc.nodes.map((n) => n.id))
    change((d) => ({ ...d, nodes: [...d.nodes, copy] }))
    setSelection({ kind: 'node', id: copy.id })
  }

  const addEdge = (edge: PipelineEdge) => change((d) => ({ ...d, edges: [...d.edges, edge] }))

  const onConnect = (c: Connection) => {
    if (!doc || readonly || !c.source || !c.target) return
    const problem = connectionProblem(doc.nodes, doc.edges, c.source, c.target)
    if (problem) return toast.warning('Not connected', problem)
    if (nodeById.get(c.source)?.type === 'condition') return setBranch({ from: c.source, to: c.target })
    addEdge({ from: c.source, to: c.target })
  }

  const onNodesChange = (changes: NodeChange<FlowNode>[]) => {
    setFlowNodes((nodes) => applyNodeChanges(changes, nodes))
    const moved = changes.filter((c) => c.type === 'position' && c.position)
    if (moved.length && !readonly)
      change(
        (d) => ({
          ...d,
          nodes: d.nodes.map((n) => {
            const m = moved.find((c) => c.type === 'position' && c.id === n.id)
            return m && m.type === 'position' && m.position ? { ...n, position: { x: Math.round(m.position.x), y: Math.round(m.position.y) } } : n
          }),
        }),
        false,
      )
  }

  const onEdgesChange = (changes: EdgeChange[]) => {
    const picked = changes.find((c) => c.type === 'select' && c.selected)
    if (picked && picked.type === 'select') setSelection({ kind: 'edge', id: picked.id })
  }

  const layout = () => {
    if (!doc || readonly) return
    change((d) => ({ ...d, nodes: autoLayout(d.nodes, d.edges) }))
    requestAnimationFrame(() => void flow.fitView({ padding: 0.2, duration: 300 }))
  }

  const updatePipeline = (patch: Partial<Pipeline>) => change((d) => ({ ...d, pipeline: { ...d.pipeline, ...patch } }), false)

  // ---- server operations ---------------------------------------------------
  const save = async (options: { quiet?: boolean; draft?: boolean } = {}): Promise<Pipeline | null> => {
    const current = docRef.current
    if (!current || readonly) return null
    let payload: Pipeline
    try {
      payload = buildSavePayload(current.pipeline, current.nodes, current.edges)
    } catch (e) {
      setBanner({ tone: 'danger', title: 'The pipeline was not saved', body: errorMessage(e) })
      return null
    }
    if (options.draft !== undefined) payload = { ...payload, draft: options.draft }
    const at = revision.current
    try {
      const server = await pipelineApi.update(pipelineId, payload)
      saved(server, at)
      queryClient.setQueryData(pipelineKey(pipelineId), server)
      void queryClient.invalidateQueries({ queryKey: ['pipelines', 'summary'] })
      setBanner(null)
      if (!options.quiet) toast.success('Pipeline saved')
      return server
    } catch (e) {
      setBanner({ tone: 'danger', title: options.draft === false ? 'Not published yet' : 'The pipeline was not saved', body: errorMessage(e) })
      return null
    }
  }

  const withBusy = async <T,>(kind: NonNullable<typeof busy>, work: () => Promise<T>) => {
    setBusy(kind)
    try {
      return await work()
    } finally {
      setBusy(null)
    }
  }

  const ensureSaved = async () => (dirty ? Boolean(await save({ quiet: true })) : true)

  const onSave = () => void withBusy('save', () => save())

  const publish = () =>
    withBusy('publish', async () => {
      const server = await save({ quiet: true, draft: false })
      if (server) toast.success('Published', 'The pipeline can run now, and its schedule is active.')
    })

  const runNow = () =>
    withBusy('run', async () => {
      if (!(await ensureSaved())) return
      try {
        const run = await pipelineApi.run(pipelineId)
        toast.success('Run started', `Run ${run.id.slice(0, 8)} is ${run.status}.`)
        navigate(`/pipelines/${encodeURIComponent(pipelineId)}/runs?run=${encodeURIComponent(run.id)}`)
      } catch (e) {
        setBanner({ tone: 'danger', title: 'The run did not start', body: errorMessage(e) })
      }
    })

  const validate = () =>
    withBusy('validate', async () => {
      if (!(await ensureSaved())) return
      try {
        const list = await pipelineApi.validateNodes(pipelineId)
        setIssues(Object.fromEntries(list.map((i) => [i.node_id, i])))
        const errors = list.filter((i) => i.errors?.length).length
        if (!list.length) setBanner({ tone: 'success', title: 'Every node passed validation' })
        else setBanner({ tone: errors ? 'danger' : 'warning', title: `${list.length} node${list.length === 1 ? '' : 's'} need${list.length === 1 ? 's' : ''} attention`, body: 'Nodes with problems are marked on the canvas; select one to see the details.' })
      } catch (e) {
        setBanner({ tone: 'danger', title: 'Validation could not run', body: errorMessage(e) })
      }
    })

  const runPreview = () =>
    withBusy('preview', async () => {
      if (!(await ensureSaved())) return
      try {
        setPreview(await pipelineApi.dryRun(pipelineId))
      } catch (e) {
        setBanner({ tone: 'danger', title: 'Preview could not run', body: errorMessage(e) })
      }
    })

  const clone = () =>
    withBusy('clone', async () => {
      if (dirty && !(await save({ quiet: true }))) return
      try {
        const copy = await pipelineApi.clone(pipelineId)
        void queryClient.invalidateQueries({ queryKey: ['pipelines', 'summary'] })
        toast.success(`Duplicated as "${copy.name}"`)
        navigate(`/pipelines/${encodeURIComponent(copy.id)}/edit`)
      } catch (e) {
        setBanner({ tone: 'danger', title: 'The pipeline was not duplicated', body: errorMessage(e) })
      }
    })

  const downloadExport = async () => {
    try {
      const file = await pipelineApi.exportYaml(pipelineId)
      downloadText(file.filename, file.text, 'application/x-yaml')
    } catch (e) {
      toast.error('Export failed', e)
    }
  }

  // ---- keyboard ------------------------------------------------------------
  const keys = useRef({ save: onSave, undo, redo, removeSelection, duplicateSelected, readonly, selection })
  keys.current = { save: onSave, undo, redo, removeSelection, duplicateSelected, readonly, selection }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      const key = e.key.toLowerCase()
      if (mod && key === 's') {
        e.preventDefault()
        if (!keys.current.readonly) keys.current.save()
        return
      }
      if (document.querySelector('[role="dialog"]')) return
      if (isTyping(e.target)) return
      if (mod && key === 'z') {
        e.preventDefault()
        if (e.shiftKey) keys.current.redo()
        else keys.current.undo()
      } else if (mod && key === 'y') {
        e.preventDefault()
        keys.current.redo()
      } else if ((e.key === 'Delete' || e.key === 'Backspace') && keys.current.selection) {
        e.preventDefault()
        keys.current.removeSelection()
      } else if (key === 'd' && !mod && !e.altKey && keys.current.selection?.kind === 'node') {
        e.preventDefault()
        keys.current.duplicateSelected()
      } else if (e.key === 'Escape') setSelection(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // ---- render --------------------------------------------------------------
  if (loaded.isError)
    return (
      <div className="ps-editor-error">
        <Callout
          tone="danger"
          title={loaded.error instanceof ApiError && loaded.error.status === 404 ? 'This pipeline does not exist' : 'The pipeline could not be loaded'}
          action={<Button onClick={() => navigate('/pipelines')}>Back to pipelines</Button>}
        >
          {errorMessage(loaded.error)}
        </Callout>
      </div>
    )
  if (!doc || !pipeline) return <PageLoading label="Loading editor" />

  const issueCount = Object.keys(issues).length
  const nodeNames = Object.fromEntries(doc.nodes.map((n) => [n.id, n.name]))
  const saveState = busy === 'save' ? 'Saving' : dirty ? 'Unsaved changes' : pipeline.updated_at ? `Saved ${formatRelative(pipeline.updated_at)}` : 'Saved'

  const onDrop = (e: DragEvent) => {
    const type = e.dataTransfer.getData(PALETTE_MIME)
    if (!type) return
    e.preventDefault()
    const p = flow.screenToFlowPosition({ x: e.clientX, y: e.clientY })
    addNode(type, { x: Math.round(p.x - NODE_WIDTH / 2), y: Math.round(p.y - NODE_HEIGHT / 2) })
  }

  return (
    <div className="ps-editor">
      <header className="ps-toolbar">
        <div className="ps-toolbar-left">
          <nav className="ps-crumbs" aria-label="Breadcrumb">
            <Link to="/pipelines">Pipelines</Link>
            <span aria-hidden="true">/</span>
            <Link to={`/pipelines/${encodeURIComponent(pipelineId)}/runs`} className="ps-crumb-name" title={pipeline.name}>
              {pipeline.name}
            </Link>
          </nav>
          {pipeline.draft && <span className="ps-pill is-draft">Draft</span>}
          {readonly && (
            <span className="ps-pill" title={readonlyReason}>
              <Lock size={12} aria-hidden="true" /> Read only
            </span>
          )}
          <span className={cx('ps-save-state', dirty && 'is-dirty')} role="status">
            <i aria-hidden="true" />
            {saveState}
          </span>
        </div>
        <div className="ps-toolbar-center">
          <SchedulePopover pipeline={pipeline} readonly={readonly} onChange={updatePipeline} />
        </div>
        <div className="ps-toolbar-right">
          <IconButton label="Undo (Ctrl+Z)" onClick={undo} disabled={!canUndo || readonly}>
            <Undo2 size={16} aria-hidden="true" />
          </IconButton>
          <IconButton label="Redo (Ctrl+Shift+Z)" onClick={redo} disabled={!canRedo || readonly}>
            <Redo2 size={16} aria-hidden="true" />
          </IconButton>
          <IconButton label="Arrange nodes automatically" onClick={layout} disabled={readonly || !doc.nodes.length}>
            <LayoutGrid size={16} aria-hidden="true" />
          </IconButton>
          <span className="ps-divider" aria-hidden="true" />
          <Button size="sm" variant="ghost" icon={<CheckCheck size={15} aria-hidden="true" />} onClick={validate} loading={busy === 'validate'} disabled={readonly && dirty}>
            Validate
          </Button>
          <Button
            size="sm"
            variant="ghost"
            icon={<Eye size={15} aria-hidden="true" />}
            onClick={runPreview}
            loading={busy === 'preview'}
            disabled={!session.can('pipelines.run') || !doc.nodes.length}
            title="Run the saved pipeline on up to 10 rows per node"
          >
            Preview
          </Button>
          <Menu
            label="More actions"
            items={[
              { id: 'runs', label: 'View runs', icon: <Workflow size={15} />, onSelect: () => navigate(`/pipelines/${encodeURIComponent(pipelineId)}/runs`) },
              { id: 'settings', label: 'Pipeline settings', icon: <Settings2 size={15} />, onSelect: () => setShowSettings(true) },
              { id: 'versions', label: 'Version history', icon: <History size={15} />, onSelect: () => setSide('versions') },
              { id: 'code', label: 'View as YAML or JSON', icon: <Code2 size={15} />, onSelect: () => setShowCode(true) },
              'separator',
              ...(session.can('pipelines.edit') ? [{ id: 'clone', label: 'Duplicate pipeline', icon: <Copy size={15} />, onSelect: () => (dirty ? setConfirmClone(true) : void clone()) }] : []),
              ...(session.can('pipelines.export') ? [{ id: 'export', label: 'Download saved YAML', icon: <Download size={15} />, onSelect: () => void downloadExport() }] : []),
            ]}
          />
          <span className="ps-divider" aria-hidden="true" />
          {!readonly && (
            <Button size="sm" variant={dirty ? 'primary' : 'secondary'} onClick={onSave} loading={busy === 'save'} title="Save (Ctrl+S)">
              Save
            </Button>
          )}
          {pipeline.draft
            ? !readonly && (
                <Button size="sm" variant="primary" icon={<Rocket size={14} aria-hidden="true" />} onClick={() => void publish()} loading={busy === 'publish'} title="Validate the pipeline fully and make it runnable">
                  Publish
                </Button>
              )
            : session.can('pipelines.run') && (
                <Button size="sm" variant="primary" icon={<Play size={14} aria-hidden="true" />} onClick={() => void runNow()} loading={busy === 'run'} title={dirty ? 'Save and run' : 'Run now'}>
                  Run
                </Button>
              )}
        </div>
      </header>

      {readonly && <div className="ps-readonly">{readonlyReason}</div>}
      {banner && (
        <div className="ps-banner">
          <Callout tone={banner.tone === 'success' ? 'success' : banner.tone} title={banner.title} onDismiss={() => setBanner(null)}>
            {banner.body && <span className="ps-banner-body">{banner.body}</span>}
          </Callout>
        </div>
      )}
      {issueCount > 0 && (
        <div className="ps-issues" role="list" aria-label="Validation results">
          {Object.values(issues).map((i) => (
            <button
              key={i.node_id}
              type="button"
              role="listitem"
              className={cx('ps-issue', i.errors?.length ? 'is-error' : 'is-warning')}
              onClick={() => {
                setSelection({ kind: 'node', id: i.node_id })
                setSide('inspector')
                void flow.fitView({ nodes: [{ id: i.node_id }], duration: 300, maxZoom: 1.2 })
              }}
            >
              <i aria-hidden="true" />
              <strong>{nodeNames[i.node_id] ?? i.node_name}</strong>
              <span>{(i.errors?.length ? i.errors : i.warnings)?.[0]}</span>
            </button>
          ))}
        </div>
      )}

      <div className="ps-workspace">
        {!readonly && <Palette onAdd={(type) => addNode(type)} />}
        <div className="ps-canvas" ref={canvasRef} onDragOver={(e) => e.dataTransfer.types.includes(PALETTE_MIME) && e.preventDefault()} onDrop={onDrop}>
          <ReactFlow
            nodes={flowNodes}
            edges={flowEdges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            isValidConnection={(c) => !doc || !c.source || !c.target ? false : !connectionProblem(doc.nodes, doc.edges, c.source, c.target)}
            onNodeDragStart={() => !readonly && change((d) => d)}
            onNodeClick={(_, n) => {
              setSelection({ kind: 'node', id: n.id })
              setSide('inspector')
            }}
            onEdgeClick={(_, e) => setSelection({ kind: 'edge', id: e.id })}
            onPaneClick={() => setSelection(null)}
            nodesDraggable={!readonly}
            nodesConnectable={!readonly}
            deleteKeyCode={null}
            selectionKeyCode={null}
            multiSelectionKeyCode={null}
            snapToGrid
            snapGrid={[10, 10]}
            fitView
            fitViewOptions={{ padding: 0.25, maxZoom: 1.1 }}
            minZoom={0.15}
            maxZoom={2}
          >
            <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
            <Controls position="bottom-left" showInteractive={false} />
            <MiniMap
              position="bottom-right"
              pannable
              zoomable
              style={{ width: 168, height: 104 }}
              nodeBorderRadius={6}
              nodeClassName={(n) => `ps-mini-${catalogEntry((n.data as { node: { type: string } }).node.type).family}`}
            />
          </ReactFlow>
          {!doc.nodes.length && (
            <div className="ps-canvas-empty">
              <strong>Start by adding a source</strong>
              <span>Drag a node from the library onto the canvas, or click it to add it here. Connect nodes by dragging from a node's right edge to another node's left edge.</span>
            </div>
          )}
          {selection?.kind === 'edge' && !readonly && (
            <div className="ps-edge-hint" role="status">
              Connection selected. Press <Kbd>Delete</Kbd> to remove it.
            </div>
          )}
        </div>
        <aside className="ps-side">
          {side === 'versions' ? (
            <VersionsPanel
              pipelineId={pipelineId}
              dirty={dirty}
              readonly={readonly}
              onClose={() => setSide('inspector')}
              onRestored={(restored) => {
                reset(restored)
                queryClient.setQueryData(pipelineKey(pipelineId), restored)
                setIssues({})
                setSelection(null)
                setSide('inspector')
              }}
            />
          ) : selectedNode ? (
            <NodeInspector
              key={selectedNode.id}
              node={selectedNode}
              nodes={doc.nodes}
              edges={doc.edges}
              issue={issueFor(selectedNode.id)}
              readonly={readonly}
              onChange={(updated, historyKey) => {
                change((d) => ({ ...d, nodes: d.nodes.map((n) => (n.id === updated.id ? updated : n)) }), historyKey)
                dropIssue(updated.id)
              }}
              onDelete={removeSelection}
              onDuplicate={duplicateSelected}
            />
          ) : (
            <Overview pipeline={pipeline} nodes={doc.nodes} edges={doc.edges} issues={issueCount} onSettings={() => setShowSettings(true)} />
          )}
        </aside>
      </div>

      {preview && <PreviewPanel result={preview} nodeNames={nodeNames} onClose={() => setPreview(null)} />}

      {showSettings && <SettingsDrawer pipeline={pipeline} readonly={readonly} onChange={updatePipeline} onClose={() => setShowSettings(false)} />}
      {showCode && (
        <CodeViewModal
          payload={(() => {
            try {
              return buildSavePayload(pipeline, doc.nodes, doc.edges)
            } catch {
              return { ...pipeline, nodes: doc.nodes, edges: doc.edges }
            }
          })()}
          dirty={dirty}
          onClose={() => setShowCode(false)}
        />
      )}
      {branch && (
        <Modal
          title="Which branch is this?"
          description={`Rows continue along this connection when the condition on ${nodeNames[branch.from] ?? 'the If / Else node'} evaluates to the branch you choose.`}
          size="sm"
          onClose={() => setBranch(null)}
          footer={
            <Button variant="ghost" onClick={() => setBranch(null)}>
              Cancel
            </Button>
          }
        >
          <div className="ps-branch-choice">
            <button type="button" className="is-true" data-autofocus onClick={() => (addEdge({ ...branch, condition: true }), setBranch(null))}>
              <GitBranch size={16} aria-hidden="true" />
              <strong>True</strong>
              <span>Runs when the condition holds</span>
            </button>
            <button type="button" className="is-false" onClick={() => (addEdge({ ...branch, condition: false }), setBranch(null))}>
              <GitBranch size={16} aria-hidden="true" />
              <strong>False</strong>
              <span>Runs when it does not</span>
            </button>
          </div>
        </Modal>
      )}
      {confirmClone && (
        <ConfirmDialog
          title="Save before duplicating?"
          confirmLabel="Save and duplicate"
          onCancel={() => setConfirmClone(false)}
          onConfirm={async () => {
            setConfirmClone(false)
            await clone()
          }}
        >
          <p>The copy is made from the saved pipeline. Your unsaved changes are saved first so the copy includes them.</p>
        </ConfirmDialog>
      )}
      {blocker.state === 'blocked' && (
        <ConfirmDialog
          title="Leave without saving?"
          tone="danger"
          confirmLabel="Discard changes"
          cancelLabel="Stay on this page"
          onCancel={() => blocker.reset()}
          onConfirm={() => blocker.proceed()}
        >
          <p>{pipeline.name} has changes that are not saved. Leaving discards them.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}

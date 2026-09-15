import { useEffect, type ReactNode } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  useReactFlow,
  useStore,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { cx } from '@brokoli/ui'
import './graph.css'

export const CARD_WIDTH = 220
export const CARD_HEIGHT = 56

export type CardData = {
  title: string
  subtitle: string
  icon: ReactNode
  /** A token expression for the accent bar and icon, for example var(--bk-color-tax-source). */
  family: string
  /** Minimap fill class (ob-mini-*). */
  mini: string
  dashed?: boolean
  /** Set while another node is selected: part of its neighbourhood, or not. */
  emphasis?: 'related' | 'dim'
  hint?: string
}

export type CardNode = Node<CardData, 'card'>

function GraphCard({ data, selected }: NodeProps<CardNode>) {
  return (
    <div
      className={cx('ob-card', data.dashed && 'is-dashed', selected && 'is-selected', data.emphasis && `is-${data.emphasis}`)}
      style={{ ['--family' as string]: data.family }}
      title={data.hint ?? data.title}
    >
      <Handle type="target" position={Position.Left} className="ob-handle" isConnectable={false} />
      <span className="ob-card-icon">{data.icon}</span>
      <span className="ob-card-text">
        <strong>{data.title}</strong>
        <small>{data.subtitle}</small>
      </span>
      <Handle type="source" position={Position.Right} className="ob-handle" isConnectable={false} />
    </div>
  )
}

const nodeTypes = { card: GraphCard }

/*
 * Opening the details panel narrows the canvas, which can leave the node
 * that was just selected under the panel or outside the view. When the
 * selected node is not fully visible, the view pans to it at the current
 * zoom; a node already in view is left alone so clicking does not jolt the map.
 */
function KeepSelectionVisible() {
  const { setCenter } = useReactFlow()
  const selected = useStore((s) => s.nodes.find((n) => n.selected))
  const width = useStore((s) => s.width)
  const height = useStore((s) => s.height)
  const [tx, ty, zoom] = useStore((s) => s.transform)
  const id = selected?.id
  useEffect(() => {
    if (!selected || !width || !height) return
    const w = selected.measured?.width ?? CARD_WIDTH
    const h = selected.measured?.height ?? CARD_HEIGHT
    const left = selected.position.x * zoom + tx
    const top = selected.position.y * zoom + ty
    const visible = left >= 0 && top >= 0 && left + w * zoom <= width && top + h * zoom <= height
    if (!visible) void setCenter(selected.position.x + w / 2, selected.position.y + h / 2, { zoom, duration: 250 })
    // Only a new selection or a resized canvas should move the view, not the pan it causes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, width, height])
  return null
}

/*
 * Read-only React Flow surface shared by the lineage and dependency maps.
 * Selection is single (no box or modifier selection) and does not follow a
 * drag, so moving a node never changes what is selected.
 */
export function FlowSurface({ children, ...props }: ReactFlowProps<CardNode, Edge> & { children?: ReactNode }) {
  return (
    <div className="ob-flow">
      <ReactFlow<CardNode, Edge>
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
        minZoom={0.1}
        maxZoom={1.75}
        nodesConnectable={false}
        edgesFocusable={false}
        selectNodesOnDrag={false}
        selectionKeyCode={null}
        multiSelectionKeyCode={null}
        deleteKeyCode={null}
        {...props}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        <MiniMap<CardNode> pannable zoomable ariaLabel="Overview of the whole map" nodeClassName={(n) => n.data.mini} />
        <Controls showInteractive={false} position="bottom-left" />
        <KeepSelectionVisible />
        {children}
      </ReactFlow>
    </div>
  )
}

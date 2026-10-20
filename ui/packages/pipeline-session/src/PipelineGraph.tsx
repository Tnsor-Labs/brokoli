import { useMemo } from 'react'
import { Background, BackgroundVariant, Controls, ReactFlow, ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { PipelineEdge, PipelineNode } from '@brokoli/api'
import { arrangeStacked } from './document'
import { nodeTypes } from './NodeCard'
import { toFlowEdges, toFlowNodes } from './flow'
import './graph.css'

/*
 * Read-only rendering of a pipeline with optional runtime statuses. Used by
 * the runs page; the editor renders the same node cards interactively.
 */
export function PipelineGraph({
  nodes,
  edges,
  statuses,
  selected,
  onSelect,
  marks,
  edgeMarks,
}: {
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  statuses?: Record<string, string>
  selected?: string | null
  onSelect?: (nodeId: string) => void
  /** What a change request does to each node, for its review graph. */
  marks?: Record<string, 'added' | 'changed' | 'removed'>
  /** The same for edges, keyed "from->to". */
  edgeMarks?: Record<string, 'added' | 'removed'>
}) {
  // A pipeline stored without a layout (built from code before the server
  // laid such pipelines out) is drawn laid out rather than stacked.
  const placed = useMemo(() => arrangeStacked(nodes, edges), [nodes, edges])
  const flowNodes = useMemo(
    () => toFlowNodes(placed, (n) => ({ status: statuses?.[n.id], readonly: true, mark: marks?.[n.id] }), selected),
    [placed, statuses, selected, marks],
  )
  const flowEdges = useMemo(() => toFlowEdges(edges, statuses, undefined, edgeMarks), [edges, statuses, edgeMarks])
  return (
    <ReactFlowProvider>
      <div className="ps-graph">
        <ReactFlow
          nodes={flowNodes}
          edges={flowEdges}
          nodeTypes={nodeTypes}
          fitView
          fitViewOptions={{ padding: 0.2, maxZoom: 1.1 }}
          minZoom={0.2}
          maxZoom={1.6}
          nodesDraggable={false}
          nodesConnectable={false}
          edgesFocusable={false}
          elementsSelectable={Boolean(onSelect)}
          onNodeClick={(_, n) => onSelect?.(n.id)}
          zoomOnScroll={false}
          panOnScroll
          preventScrolling={false}
        >
          <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
          <Controls showInteractive={false} position="bottom-right" />
        </ReactFlow>
      </div>
    </ReactFlowProvider>
  )
}

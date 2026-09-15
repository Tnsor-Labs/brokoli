import { useMemo } from 'react'
import { Background, BackgroundVariant, Controls, ReactFlow, ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { PipelineEdge, PipelineNode } from '@brokoli/api'
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
}: {
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  statuses?: Record<string, string>
  selected?: string | null
  onSelect?: (nodeId: string) => void
}) {
  const flowNodes = useMemo(() => toFlowNodes(nodes, (n) => ({ status: statuses?.[n.id], readonly: true }), selected), [nodes, statuses, selected])
  const flowEdges = useMemo(() => toFlowEdges(edges, statuses), [edges, statuses])
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

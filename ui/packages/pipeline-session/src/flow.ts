import { MarkerType, type Edge } from '@xyflow/react'
import type { PipelineEdge, PipelineNode } from '@brokoli/api'
import type { FlowNode, NodeCardData } from './NodeCard'

/*
 * React Flow is a view of the document, never the document itself. These
 * helpers build view objects from domain objects; the reverse direction
 * only ever reads ids and positions back.
 */

export const edgeId = (e: PipelineEdge, index: number) => `${e.from}->${e.to}#${index}`

export function toFlowNodes(
  nodes: PipelineNode[],
  extra: (node: PipelineNode) => Partial<NodeCardData> = () => ({}),
  selected?: string | null,
): FlowNode[] {
  return nodes.map((node) => ({
    id: node.id,
    type: 'brokoli',
    position: node.position ?? { x: 0, y: 0 },
    data: { node, ...extra(node) },
    selected: selected === node.id,
  }))
}

export type FlowEdgeData = { index: number; condition?: boolean }

export function toFlowEdges(edges: PipelineEdge[], statuses?: Record<string, string>, selected?: string | null): Edge<FlowEdgeData>[] {
  return edges.map((e, index) => {
    const id = edgeId(e, index)
    const flowing = statuses && statuses[e.from] && ['success', 'completed', 'succeeded'].includes(statuses[e.from]) && statuses[e.to] === 'running'
    return {
      id,
      source: e.from,
      target: e.to,
      type: 'smoothstep',
      selected: selected === id,
      animated: Boolean(flowing),
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
      data: { index, condition: e.condition },
      label: e.condition === undefined ? undefined : e.condition ? 'true' : 'false',
      className: e.condition === false ? 'is-false-branch' : e.condition === true ? 'is-true-branch' : undefined,
      labelBgPadding: [6, 3] as [number, number],
      labelBgBorderRadius: 4,
    }
  })
}

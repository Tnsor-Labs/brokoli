import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { AlertTriangle } from 'lucide-react'
import type { PipelineNode } from '@brokoli/api'
import { cx, statusMeta } from '@brokoli/ui'
import { FAMILY_COLOR, catalogEntry, portsFor } from './catalog'

export type NodeCardData = {
  node: PipelineNode
  /** Runtime status (read-only graphs on the runs page). */
  status?: string
  /** Validation result from the server, or structural warnings from the editor. */
  issue?: { level: 'error' | 'warning'; messages: string[] }
  readonly?: boolean
}

export type FlowNode = Node<NodeCardData, 'brokoli'>

export function NodeCard({ data, selected }: NodeProps<FlowNode>) {
  const { node, status, issue } = data
  const entry = catalogEntry(node.type)
  const ports = portsFor(node)
  const meta = status ? statusMeta(status) : null
  return (
    <div
      className={cx('ps-node', selected && 'is-selected', issue && `has-${issue.level}`, meta && `bk-tone-${meta.tone}`, meta && 'has-status')}
      style={{ ['--family' as string]: FAMILY_COLOR[entry.family] }}
      title={issue ? issue.messages.join('\n') : undefined}
    >
      {ports.input && <Handle type="target" position={Position.Left} className="ps-handle" />}
      <span className="ps-node-badge" aria-hidden="true">
        <entry.glyph size={19} strokeWidth={1.75} />
      </span>
      <span className="ps-node-text">
        <strong>{node.name || entry.label}</strong>
        <small>{node.type === 'migrate' ? `${entry.label.toLowerCase()} · standalone` : entry.label.toLowerCase()}</small>
      </span>
      {meta && (
        <span className={cx('ps-node-status', meta.pulse && 'is-live')} title={meta.label} aria-label={meta.label}>
          <i />
        </span>
      )}
      {issue && (
        <span className="ps-node-issue" aria-label={issue.messages.join('. ')}>
          <AlertTriangle size={12} aria-hidden="true" />
        </span>
      )}
      {ports.output && <Handle type="source" position={Position.Right} className="ps-handle" />}
    </div>
  )
}

export const nodeTypes = { brokoli: NodeCard }

import { Settings2 } from 'lucide-react'
import type { Pipeline, PipelineEdge, PipelineNode } from '@brokoli/api'
import { Button, Kbd } from '@brokoli/ui'

const SHORTCUTS: [string[], string][] = [
  [['Ctrl', 'S'], 'Save'],
  [['Ctrl', 'Z'], 'Undo'],
  [['Ctrl', 'Shift', 'Z'], 'Redo'],
  [['Delete'], 'Remove the selected node or connection'],
  [['D'], 'Duplicate the selected node'],
  [['Esc'], 'Clear the selection'],
]

/** Right-hand panel when nothing is selected: what this pipeline is and how to work with the canvas. */
export function Overview({
  pipeline,
  nodes,
  edges,
  issues,
  onSettings,
}: {
  pipeline: Pipeline
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  issues: number
  onSettings: () => void
}) {
  return (
    <div className="ps-overview">
      <header>
        <h3>{pipeline.name}</h3>
        <p>{pipeline.description || 'No description yet.'}</p>
      </header>
      <dl className="ps-overview-facts">
        <div>
          <dt>State</dt>
          <dd>{pipeline.draft ? 'Draft: saved, not runnable until published' : pipeline.enabled === false ? 'Published, schedule paused' : 'Published'}</dd>
        </div>
        <div>
          <dt>Schedule</dt>
          <dd className="bk-mono">{pipeline.schedule || 'Manual only'}</dd>
        </div>
        <div>
          <dt>Graph</dt>
          <dd>
            {nodes.length} node{nodes.length === 1 ? '' : 's'}, {edges.length} connection{edges.length === 1 ? '' : 's'}
          </dd>
        </div>
        {issues > 0 && (
          <div>
            <dt>Validation</dt>
            <dd>
              {issues} node{issues === 1 ? '' : 's'} flagged
            </dd>
          </div>
        )}
        {pipeline.ir_version && (
          <div>
            <dt>IR version</dt>
            <dd className="bk-mono">{pipeline.ir_version}</dd>
          </div>
        )}
      </dl>
      <Button size="sm" icon={<Settings2 size={14} aria-hidden="true" />} onClick={onSettings}>
        Pipeline settings
      </Button>
      <section className="ps-overview-keys">
        <h4>Keyboard</h4>
        <ul>
          {SHORTCUTS.map(([combo, label]) => (
            <li key={label}>
              <span>
                {combo.map((k) => (
                  <Kbd key={k}>{k}</Kbd>
                ))}
              </span>
              {label}
            </li>
          ))}
        </ul>
      </section>
      <p className="ps-overview-hint">Select a node to configure it. Select a connection to remove it.</p>
    </div>
  )
}

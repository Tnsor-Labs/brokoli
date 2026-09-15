import { useEffect, useMemo, useState } from 'react'
import { Copy, Trash2 } from 'lucide-react'
import type { PipelineEdge, PipelineNode } from '@brokoli/api'
import { Button, Callout, Field, Input, Kbd, Textarea } from '@brokoli/ui'
import { FAMILY_COLOR, catalogEntry } from '../catalog'
import { patchConfig } from '../document'
import { NumberField, Section, SelectField, type FormCtx } from './fields'
import { OWNS_TIMEOUT, TYPE_FORMS } from './typeForms'
import './forms.css'

/*
 * Configuration for one node. Name, type-specific settings, and the
 * execution settings every node shares. Types the editor has no form for
 * (plugins, enterprise executors) get an honest raw-JSON editor instead of
 * an empty panel.
 */
export function NodeInspector({
  node,
  nodes,
  edges,
  issue,
  readonly,
  onChange,
  onDelete,
  onDuplicate,
}: {
  node: PipelineNode
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  issue?: { level: 'error' | 'warning'; messages: string[] }
  readonly: boolean
  onChange: (node: PipelineNode, historyKey?: string | boolean) => void
  onDelete: () => void
  onDuplicate: () => void
}) {
  const entry = catalogEntry(node.type)
  const ctx = useMemo<FormCtx>(
    () => ({
      node,
      readonly,
      get: (key) => node.config?.[key],
      set: (patch, historyKey) => onChange({ ...node, config: patchConfig(node.config ?? {}, patch) }, historyKey ?? true),
    }),
    [node, readonly, onChange],
  )
  const Form = TYPE_FORMS[node.type]

  return (
    <div className="ps-inspector" style={{ ['--family' as string]: FAMILY_COLOR[entry.family] }}>
      <header className="ps-inspector-head">
        <span className="ps-inspector-icon" aria-hidden="true">
          <entry.glyph size={19} strokeWidth={1.75} />
        </span>
        <div>
          <h3 className="panel-title">{entry.label}</h3>
          <p>{entry.description}</p>
        </div>
      </header>
      <div className="ps-inspector-body">
        <fieldset disabled={readonly} className="ps-inspector-fields">
          {issue && (
            <Callout tone={issue.level === 'error' ? 'danger' : 'warning'} title={issue.level === 'error' ? 'This node will not run as configured' : 'Worth checking'}>
              <ul className="ps-issue-list">
                {issue.messages.map((m, i) => (
                  <li key={i}>{m}</li>
                ))}
              </ul>
            </Callout>
          )}
          <Field label="Name" error={!node.name.trim() ? 'A node needs a name' : undefined}>
            <Input value={node.name} onChange={(e) => onChange({ ...node, name: e.target.value }, `name:${node.id}`)} />
          </Field>
          {Form ? <Form ctx={ctx} nodes={nodes} edges={edges} /> : <RawConfig node={node} readonly={readonly} onChange={onChange} />}
          <Section title="Execution" description="Applies to every attempt of this node.">
            <NumberField ctx={ctx} name="max_retries" label="Retries" defaultValue={0} min={0} max={10} />
            <NumberField ctx={ctx} name="retry_delay" label="Delay between retries (ms)" defaultValue={1000} min={0} step={500} />
            <SelectField
              ctx={ctx}
              name="retry_backoff"
              label="Backoff"
              defaultLabel="exponential"
              options={[
                { value: 'exponential', label: 'Exponential' },
                { value: 'linear', label: 'Linear' },
                { value: 'fixed', label: 'Fixed' },
              ]}
            />
            {!OWNS_TIMEOUT.has(node.type) && <NumberField ctx={ctx} name="timeout" label="Attempt timeout (seconds)" defaultValue="30 minutes" min={1} />}
          </Section>
        </fieldset>
      </div>
      {!readonly && (
        <footer className="ps-inspector-foot">
          <Button size="sm" icon={<Copy size={14} aria-hidden="true" />} onClick={onDuplicate} title="Duplicate (D)">
            Duplicate <Kbd>D</Kbd>
          </Button>
          <Button size="sm" variant="danger" icon={<Trash2 size={14} aria-hidden="true" />} onClick={onDelete}>
            Delete
          </Button>
        </footer>
      )}
    </div>
  )
}

function RawConfig({ node, readonly, onChange }: { node: PipelineNode; readonly: boolean; onChange: (node: PipelineNode, key?: string | boolean) => void }) {
  const [text, setText] = useState(() => JSON.stringify(node.config ?? {}, null, 2))
  const [error, setError] = useState('')
  useEffect(() => setText(JSON.stringify(node.config ?? {}, null, 2)), [node.id]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Section title="Configuration" description="This node type comes from a plugin or extension, so the editor shows its settings as JSON. The server validates them on save.">
      <Field label="Config (JSON object)" error={error || undefined}>
        <Textarea
          mono
          rows={12}
          value={text}
          readOnly={readonly}
          onChange={(e) => {
            setText(e.target.value)
            try {
              const parsed = JSON.parse(e.target.value)
              if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('The configuration must be a JSON object.')
              setError('')
              onChange({ ...node, config: parsed }, `raw:${node.id}`)
            } catch (err) {
              setError(err instanceof Error ? err.message : String(err))
            }
          }}
        />
      </Field>
    </Section>
  )
}

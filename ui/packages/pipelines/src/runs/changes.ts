import type { ChangeRequest, FieldChange, PipelineDiff } from '@brokoli/api'

/* Change requests (ADR-046), in words. */

/** Who proposed a change: a person's name, or "agent triage-bot, for Ada". */
export function changeAuthor(
  cr: Pick<ChangeRequest, 'created_by_kind' | 'created_by_name' | 'on_behalf_of'>,
): string {
  if (cr.created_by_kind !== 'agent') return cr.created_by_name || 'someone'
  const agent = `agent ${cr.created_by_name || 'unknown'}`
  return cr.on_behalf_of ? `${agent}, for ${cr.on_behalf_of}` : agent
}

const FIELD_LABEL: Record<string, string> = {
  name: 'Name',
  description: 'Description',
  schedule: 'Schedule',
  schedule_timezone: 'Timezone',
  enabled: 'Enabled',
  tags: 'Tags',
  params: 'Parameters',
  parameters: 'Parameters',
  sla_deadline: 'SLA deadline',
  ir_version: 'IR version',
}

/** A value, short: strings as they are, everything else as JSON, cut at 80 characters. */
export function shortValue(v: unknown): string {
  if (v === undefined || v === null || v === '') return '(none)'
  const text = typeof v === 'string' ? v : JSON.stringify(v)
  return text.length > 80 ? `${text.slice(0, 77)}...` : text
}

function fieldLine(c: FieldChange, label: string): string {
  return `${label}: ${shortValue(c.before)} → ${shortValue(c.after)}`
}

/** The diff as lines a person reads, in the order the change happens. */
export function diffLines(diff: PipelineDiff | undefined): string[] {
  if (!diff) return []
  const lines: string[] = []
  for (const c of diff.pipeline ?? []) lines.push(fieldLine(c, FIELD_LABEL[c.field] ?? c.field))
  for (const n of diff.nodes_removed ?? []) lines.push(`Removes node ${n.name} (${n.type})`)
  for (const n of diff.nodes_added ?? []) lines.push(`Adds node ${n.name} (${n.type})`)
  for (const n of diff.nodes_changed ?? []) {
    for (const c of n.changes) {
      const field = c.field.startsWith('config.') ? c.field.slice('config.'.length) : c.field
      lines.push(fieldLine(c, `${n.name}, ${field}`))
    }
  }
  for (const e of diff.edges_removed ?? []) lines.push(`Disconnects ${e.from} → ${e.to}`)
  for (const e of diff.edges_added ?? []) lines.push(`Connects ${e.from} → ${e.to}`)
  return lines
}

/** Why a change request cannot be applied now, or null when it can. */
export function cannotApply(
  cr: ChangeRequest,
  opts: { canEdit: boolean; fromGit: boolean },
): string | null {
  if (cr.status !== 'open') return `This change request is ${cr.status}.`
  if (opts.fromGit)
    return 'This pipeline is deployed from a repository: the change is applied by merging it there.'
  if (cr.stale)
    return 'The pipeline has changed since this was proposed; applying it would undo those changes.'
  if (!cr.validation.valid) return 'The proposed pipeline is not valid.'
  if (!opts.canEdit) return 'Applying a change needs permission to edit this pipeline.'
  return null
}

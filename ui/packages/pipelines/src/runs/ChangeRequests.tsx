import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Bot, ChevronRight, GitPullRequestArrow, User } from 'lucide-react'
import { pipelineApi, type ChangeRequest } from '@brokoli/api'
import { Badge, formatRelative } from '@brokoli/ui'
import { keys, paths } from '../keys'
import { diffCounts } from '../changes/review'
import { changeAuthor } from './changes'

/*
 * The pipeline's open change requests (ADR-046): proposed versions, often
 * from an agent, that change nothing until a person applies them. Each row
 * opens the change's review page, where it is read, tested and decided.
 */
export function ChangeRequests({ pipelineId }: { pipelineId: string; fromGit?: boolean }) {
  const open = useQuery({
    queryKey: [...keys.pipeline(pipelineId), 'changes', 'open'],
    queryFn: () => pipelineApi.changes(pipelineId, 'open'),
  })
  if (!open.data?.length) return null
  return (
    <section className="bk-panel-card bk-changes" aria-label="Proposed changes">
      <header className="bk-changes-head">
        <GitPullRequestArrow size={16} aria-hidden="true" />
        <strong>
          {open.data.length} proposed change{open.data.length === 1 ? '' : 's'}
        </strong>
        <small>Nothing changes until someone who can edit this pipeline applies it.</small>
      </header>
      {open.data.map((cr) => (
        <ChangeRow key={cr.id} pipelineId={pipelineId} cr={cr} />
      ))}
    </section>
  )
}

function ChangeRow({ pipelineId, cr }: { pipelineId: string; cr: ChangeRequest }) {
  const Icon = cr.created_by_kind === 'agent' ? Bot : User
  const c = diffCounts(cr.diff)
  const touched = c.added + c.changed + c.removed
  return (
    <Link className="bk-change bk-change-link" to={paths.change(pipelineId, cr.id)}>
      <Icon size={16} aria-hidden="true" className="bk-change-icon" />
      <span className="bk-change-text">
        <strong>{cr.title}</strong>
        <small>
          {changeAuthor(cr)} · {formatRelative(cr.created_at)}
          {touched > 0 && ` · ${touched} node${touched === 1 ? '' : 's'}`}
        </small>
      </span>
      <span className="bk-disclosure-meta">
        {cr.stale && <Badge tone="warning">Out of date</Badge>}
        {!cr.validation.valid && <Badge tone="danger">Not valid</Badge>}
        {cr.delivery?.url && <Badge tone="accent">Delivered</Badge>}
        <span className="bk-change-review">
          Review <ChevronRight size={14} aria-hidden="true" />
        </span>
      </span>
    </Link>
  )
}

import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, GitPullRequestArrow } from 'lucide-react'
import { pipelineApi, type ChangeRequest } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  Badge,
  Button,
  ConfirmDialog,
  Textarea,
  errorMessage,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import { keys } from '../keys'
import { cannotApply, changeAuthor, diffLines } from './changes'

/*
 * The pipeline's open change requests (ADR-046): proposed versions, often
 * from an agent, that change nothing until a person applies them. Each
 * shows who proposed it, its diff and validation, and Apply / Decline.
 */
export function ChangeRequests({ pipelineId, fromGit }: { pipelineId: string; fromGit: boolean }) {
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
        <ChangeRow key={cr.id} pipelineId={pipelineId} cr={cr} fromGit={fromGit} />
      ))}
    </section>
  )
}

function ChangeRow({
  pipelineId,
  cr,
  fromGit,
}: {
  pipelineId: string
  cr: ChangeRequest
  fromGit: boolean
}) {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const [expanded, setExpanded] = useState(false)
  const [confirm, setConfirm] = useState<null | 'apply' | 'decline'>(null)
  const [reason, setReason] = useState('')
  const full = useQuery({
    queryKey: [...keys.pipeline(pipelineId), 'changes', cr.id],
    queryFn: () => pipelineApi.change(pipelineId, cr.id),
    enabled: expanded,
  })
  const blocked = cannotApply(cr, { canEdit: session.can('pipelines.edit'), fromGit })
  const mayDecline = session.can('pipelines.edit') || session.user?.id === cr.created_by

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.pipeline(pipelineId) })
    void queryClient.invalidateQueries({ queryKey: ['pipelines', 'summary'] })
  }
  const apply = async () => {
    const res = await pipelineApi.applyChange(pipelineId, cr.id)
    toast.success('Change applied', `The pipeline is now version ${res.version}.`)
    refresh()
  }
  const decline = async () => {
    await pipelineApi.declineChange(pipelineId, cr.id, reason)
    toast.success('Change declined')
    refresh()
  }

  const lines = diffLines(full.data?.diff)
  return (
    <div className="bk-change">
      <button
        type="button"
        className="bk-disclosure"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
      >
        <ChevronRight size={16} aria-hidden="true" className="bk-disclosure-icon" />
        <span>
          <strong>{cr.title}</strong>
          <small>
            {changeAuthor(cr)} · {formatRelative(cr.created_at)}
          </small>
        </span>
        <span className="bk-disclosure-meta">
          {cr.stale && <Badge tone="warning">Out of date</Badge>}
          {!cr.validation.valid && <Badge tone="danger">Not valid</Badge>}
          {cr.delivery?.url && <Badge tone="accent">Delivered</Badge>}
        </span>
      </button>
      {expanded && (
        <div className="bk-change-body">
          {cr.description && <p>{cr.description}</p>}
          {full.isPending && <p className="bk-muted">Loading the change</p>}
          {full.isError && (
            <p className="bk-muted">The change could not be loaded: {errorMessage(full.error)}</p>
          )}
          {lines.length > 0 && (
            <ul className="bk-change-diff">
              {lines.map((l, i) => (
                <li key={i}>{l}</li>
              ))}
            </ul>
          )}
          {!cr.validation.valid && (
            <ul className="bk-change-errors">
              {(cr.validation.errors ?? []).map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          )}
          {cr.delivery?.url && (
            <p>
              Delivered for review: <a href={cr.delivery.url}>{cr.delivery.url}</a>
            </p>
          )}
          <div className="bk-change-actions">
            {blocked && <small className="bk-muted">{blocked}</small>}
            {mayDecline && (
              <Button onClick={() => setConfirm('decline')}>
                {session.user?.id === cr.created_by ? 'Withdraw' : 'Decline'}
              </Button>
            )}
            <Button
              variant="primary"
              disabled={Boolean(blocked)}
              title={blocked ?? 'Apply this change'}
              onClick={() => setConfirm('apply')}
            >
              Apply
            </Button>
          </div>
        </div>
      )}
      {confirm === 'apply' && (
        <ConfirmDialog
          title={`Apply "${cr.title}"?`}
          confirmLabel="Apply"
          onCancel={() => setConfirm(null)}
          onConfirm={async () => {
            try {
              await apply()
            } catch (e) {
              toast.error('Not applied', e)
            }
            setConfirm(null)
          }}
        >
          The pipeline becomes the proposed version, as if it were saved from the editor. The
          previous version stays in its history.
        </ConfirmDialog>
      )}
      {confirm === 'decline' && (
        <ConfirmDialog
          title={`Decline "${cr.title}"?`}
          confirmLabel="Decline"
          tone="danger"
          onCancel={() => setConfirm(null)}
          onConfirm={async () => {
            try {
              await decline()
            } catch (e) {
              toast.error('Not declined', e)
            }
            setConfirm(null)
          }}
        >
          <Textarea
            aria-label="Reason (optional)"
            placeholder="Why, for whoever proposed it (optional)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </ConfirmDialog>
      )}
    </div>
  )
}

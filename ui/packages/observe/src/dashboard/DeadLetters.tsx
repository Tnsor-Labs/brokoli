import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { observeApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { paths } from '@brokoli/pipelines'
import { Button, Callout, Skeleton, errorMessage, formatDateTime, formatRelative, useToast } from '@brokoli/ui'
import { Panel } from './Panel'
import { groupDeadLetters } from './model'

const LIMIT = 200

/*
 * Unresolved dead letters. One more than the display limit is requested so
 * the count can say "200+" instead of presenting a capped number as the
 * total. A failed load is shown as an error, never as an empty queue.
 */
export function DeadLetters({ now }: { now: number }) {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['observe', 'dlq'], queryFn: () => observeApi.deadLetters(LIMIT + 1) })
  const capped = (query.data?.length ?? 0) > LIMIT
  const groups = useMemo(() => groupDeadLetters((query.data ?? []).slice(0, LIMIT)), [query.data])
  const [busy, setBusy] = useState<string | null>(null)
  const canResolve = session.can('runs.resume')

  const resolve = async (key: string, pipelineId: string, ids: string[]) => {
    setBusy(key)
    let failed = 0
    // One at a time: the server has no bulk resolve, and a burst of parallel writes gains nothing here.
    for (const id of ids) {
      try {
        await observeApi.resolveDeadLetter(pipelineId, id)
      } catch {
        failed++
      }
    }
    setBusy(null)
    if (failed) toast.warning(`${failed} of ${ids.length} could not be marked resolved`)
    else toast.success(ids.length === 1 ? 'Marked resolved' : `Marked ${ids.length} resolved`)
    void queryClient.invalidateQueries({ queryKey: ['observe', 'dlq'] })
  }

  const count = query.data ? (capped ? `${LIMIT}+` : String(query.data.length)) : undefined
  return (
    <Panel title="Dead letters" subtitle="Records a node could not process" count={count} tone={query.data?.length ? 'warning' : 'neutral'}>
      {query.isError ? (
        <Callout tone="danger" title="Could not load dead letters" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : query.isPending ? (
        <Skeleton height={48} />
      ) : !groups.length ? (
        <p className="ob-empty-line">No unresolved dead letters.</p>
      ) : (
        <ul className="ob-rows">
          {groups.map((g) => (
            <li key={g.key} className="ob-rows-row ob-dlq">
              <div>
                <Link className="ob-link-strong" to={paths.runs(g.pipelineId)}>
                  {g.pipelineName}
                </Link>
                {g.ids.length > 1 && <span className="ob-count">x{g.ids.length}</span>}
                <span className="ob-small ob-quiet"> {g.nodeName || 'Run level'}</span>
              </div>
              <p className="ob-error-line" title={g.fullError}>
                {g.error || 'No error message'}
              </p>
              <div className="ob-dlq-foot">
                <span className="ob-small ob-quiet" title={formatDateTime(g.newest)}>
                  {formatRelative(g.newest, now)}
                </span>
                {canResolve && (
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={busy === g.key}
                    disabled={busy !== null}
                    title="Marks the records as handled. Nothing is re-run."
                    onClick={() => void resolve(g.key, g.pipelineId, g.ids)}
                  >
                    {g.ids.length === 1 ? 'Mark resolved' : `Mark ${g.ids.length} resolved`}
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

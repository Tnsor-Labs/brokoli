import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { triggersApi } from '@brokoli/api'
import { AgentAvatar } from '@brokoli/ui'
import { keys, paths } from '../keys'
import { noticeLabel } from './model'

/*
 * The agents a run was delivered to (ADR-047 section 7), and the change
 * request each then proposed, linking the run to what the agent did about
 * it. Nothing is shown when no agent was told, or the server has no
 * triggers.
 */
export function RunAgents({
  runId,
  pipelineId,
  finishedAt,
}: {
  runId: string
  pipelineId: string
  finishedAt?: string | null
}) {
  const recent = finishedAt ? Date.now() - Date.parse(finishedAt) < 120_000 : false
  const notices = useQuery({
    queryKey: keys.runAgents(runId),
    queryFn: () => triggersApi.runNotices(runId),
    // Deliveries are made a few seconds after a run ends, and settle after.
    refetchInterval: (q) =>
      q.state.data?.some((n) => n.status === 'pending') || (recent && !q.state.data?.length)
        ? 5_000
        : false,
  })
  const list = notices.data ?? []
  if (!list.length) return null
  return (
    <div className="is-wide">
      <dt>Agents told</dt>
      <dd className="bk-run-agents">
        {list.map((n) => (
          <span key={n.delivery_id} className="bk-run-agent">
            <AgentAvatar avatar={n.agent_avatar} size={20} />
            <span>
              <Link to={`/agents/${encodeURIComponent(n.agent_id)}`} className="bk-mono">
                {n.agent_name}
              </Link>{' '}
              <span className="bk-muted">
                {noticeLabel(n)} ({n.event})
              </span>
              {n.proposal && (
                <>
                  {', proposed '}
                  <Link to={paths.change(pipelineId, n.proposal.id)}>{n.proposal.title}</Link>
                  <span className="bk-muted"> ({n.proposal.status})</span>
                </>
              )}
            </span>
          </span>
        ))}
      </dd>
    </div>
  )
}

import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { triggersApi } from '@brokoli/api'
import { AgentAvatar, Badge } from '@brokoli/ui'

/*
 * The agents told about this pipeline's runs (ADR-047): the triggers on it
 * and on its whole workspace. They are made and changed on the agent's
 * page, which whoever manages the agent can open from here.
 */
export function PipelineAgents({ pipelineId }: { pipelineId: string }) {
  const triggers = useQuery({
    queryKey: ['pipeline', pipelineId, 'triggers'],
    queryFn: () => triggersApi.pipelineTriggers(pipelineId),
    enabled: Boolean(pipelineId),
  })
  // The server has no triggers: say nothing.
  if (triggers.data === null || triggers.isError) return null
  const list = triggers.data ?? []
  return (
    <section>
      <h4>Agents</h4>
      <p className="ps-form-note">
        Agents told when this pipeline's runs fail, are blocked or recover. A trigger is made on its
        agent's page, under Agents.
      </p>
      {triggers.isPending ? null : !list.length ? (
        <p className="ps-form-note">No agent is told about this pipeline.</p>
      ) : (
        <ul className="ps-agents">
          {list.map((t) => (
            <li key={t.id}>
              <AgentAvatar avatar={t.agent_avatar} size={20} />
              {t.may_manage ? (
                <Link to={`/agents/${encodeURIComponent(t.agent_id)}`} className="bk-mono">
                  {t.agent_name}
                </Link>
              ) : (
                <span className="bk-mono">{t.agent_name}</span>
              )}
              <span className="ps-agents-events">{t.events.join(', ')}</span>
              <span className="ps-form-note">
                {t.scope === 'pipeline' ? 'this pipeline' : 'every pipeline'},{' '}
                {t.delivery === 'feed' ? 'by its feed' : 'by webhook'}
              </span>
              {!t.enabled && <Badge tone="neutral">off</Badge>}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

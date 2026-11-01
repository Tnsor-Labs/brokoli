import { useState } from 'react'
import { agentsApi, type Agent } from '@brokoli/api'
import { AGENT_AVATARS, AgentAvatar, useToast } from '@brokoli/ui'
import '../workspace.css'
import './agents.css'

/**
 * Choosing an agent's avatar. Its owner, or whoever manages users, may
 * change it. Exported for a distribution's own agent page; onChanged is
 * where that page refreshes what it shows.
 */
export function AgentAvatarPicker({
  agent,
  onChanged,
}: {
  agent: Agent
  onChanged: () => unknown
}) {
  const toast = useToast()
  const [busy, setBusy] = useState<string | null>(null)
  const pick = async (avatar: string) => {
    if (avatar === agent.avatar || busy) return
    setBusy(avatar)
    try {
      await agentsApi.update(agent.id, { avatar })
      await onChanged()
    } catch (e) {
      toast.error('Avatar not changed', e)
    }
    setBusy(null)
  }
  return (
    <section className="ag-section" aria-labelledby="ag-avatar">
      <div className="ag-section-head">
        <div>
          <h2 id="ag-avatar">Avatar</h2>
          <p className="ws-muted">
            How {agent.name} appears in the agents list, on runs it was told about, and in pipeline
            settings.
          </p>
        </div>
      </div>
      <div className="ag-avatars" role="group" aria-label={`Avatar for ${agent.name}`}>
        {AGENT_AVATARS.map((v) => (
          <button
            key={v.name}
            type="button"
            className="ag-avatar-pick"
            aria-pressed={agent.avatar === v.name}
            disabled={busy !== null}
            onClick={() => void pick(v.name)}
          >
            <AgentAvatar avatar={v.name} size={44} />
            <span>{v.label}</span>
          </button>
        ))}
      </div>
    </section>
  )
}

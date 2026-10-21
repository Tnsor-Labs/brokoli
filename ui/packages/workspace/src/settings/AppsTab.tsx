import { ConnectedApps } from '@brokoli/auth'

export function AppsTab() {
  return (
    <div className="ws-tab">
      <section className="ws-card">
        <h3>Connected apps</h3>
        <p className="ws-muted">
          Applications you approved to reach Brokoli over MCP. Each acts as you, or as the agent you
          chose when approving it.
        </p>
        <ConnectedApps />
      </section>
    </div>
  )
}

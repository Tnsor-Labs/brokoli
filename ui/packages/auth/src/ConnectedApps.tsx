import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plug } from 'lucide-react'
import { oauthApi, type OAuthConnection } from '@brokoli/api'
import {
  Button,
  Callout,
  ConfirmDialog,
  EmptyState,
  Skeleton,
  errorMessage,
  formatRelative,
  useToast,
} from '@brokoli/ui'

/*
 * The applications the signed-in person connected with OAuth (ADR-046
 * phase 5): who each acts as, when it was last used, and a way to end it.
 * Disconnecting stops its tokens at once.
 */
export function ConnectedApps() {
  const queryClient = useQueryClient()
  const toast = useToast()
  const list = useQuery({ queryKey: ['oauth-connections'], queryFn: oauthApi.connections })
  const [ending, setEnding] = useState<OAuthConnection | null>(null)

  if (list.isPending) return <Skeleton height={120} />
  if (list.isError) {
    return (
      <Callout tone="danger" title="Could not load your connected apps">
        {errorMessage(list.error)}
      </Callout>
    )
  }
  if (list.data.length === 0) {
    return (
      <EmptyState icon={<Plug size={18} aria-hidden="true" />} title="No connected apps">
        An MCP client, such as a chat assistant's connector, appears here once you approve it. Point
        it at <span className="bk-mono">{window.location.origin}/api/mcp</span>.
      </EmptyState>
    )
  }
  return (
    <>
      <div className="bk-table-wrap">
        <table className="bk-table">
          <thead>
            <tr>
              <th>Application</th>
              <th>Acts as</th>
              <th>Connected</th>
              <th>Last used</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {list.data.map((c) => (
              <tr key={c.id}>
                <td>{c.client_name || c.client_id}</td>
                <td>{c.agent_name ? <span className="bk-mono">{c.agent_name}</span> : 'You'}</td>
                <td>{formatRelative(c.created_at)}</td>
                <td>{c.last_used_at ? formatRelative(c.last_used_at) : 'Not yet'}</td>
                <td style={{ textAlign: 'right' }}>
                  <Button size="sm" onClick={() => setEnding(c)}>
                    Disconnect
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {ending && (
        <ConfirmDialog
          title={`Disconnect ${ending.client_name || 'this app'}?`}
          confirmLabel="Disconnect"
          tone="danger"
          onCancel={() => setEnding(null)}
          onConfirm={async () => {
            await oauthApi.disconnect(ending.id)
            setEnding(null)
            toast.success('Disconnected')
            await queryClient.invalidateQueries({ queryKey: ['oauth-connections'] })
          }}
        >
          It stops working at once. To use it again, connect it again and approve it.
        </ConfirmDialog>
      )}
    </>
  )
}

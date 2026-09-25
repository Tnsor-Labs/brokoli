import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { pipelineApi, runApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { keys } from '@brokoli/pipelines'
import { useToast } from '@brokoli/ui'

/*
 * Start and cancel, shared by every dashboard section. Buttons are shown
 * only when the session has the permission; the previous dashboard showed
 * them to everyone and let the server answer 403. Neither request is
 * retried by the API client, so a slow response never starts a second run.
 */
export function useRunActions() {
  const toast = useToast()
  const session = useSession()
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState<string | null>(null)
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['observe', 'dashboard'] })
    void queryClient.invalidateQueries({ queryKey: keys.summary })
  }
  return {
    busy,
    canRun: session.can('pipelines.run'),
    canCancel: session.can('runs.cancel'),
    start: async (pipelineId: string, name: string) => {
      setBusy(`run:${pipelineId}`)
      try {
        await pipelineApi.run(pipelineId)
        toast.success(`Started ${name}`)
        refresh()
      } catch (e) {
        toast.error(`Could not start ${name}`, e)
      } finally {
        setBusy(null)
      }
    },
    cancel: async (runId: string, name: string) => {
      setBusy(`cancel:${runId}`)
      try {
        await runApi.cancel(runId)
        toast.success(`Cancelled the run of ${name}`)
        refresh()
      } catch (e) {
        toast.error(`Could not cancel the run of ${name}`, e)
      } finally {
        setBusy(null)
      }
    },
  }
}

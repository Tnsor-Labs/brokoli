import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { pipelineApi, type PipelineSummary, type WorkspaceChange } from '@brokoli/api'
import { useSession } from '@brokoli/auth'

/*
 * What needs a person (Home): pipelines whose latest run failed, and
 * changes waiting for a decision. The sidebar counts the same things, from
 * the same queries, so the number by Home is what Home shows.
 */

const FAILED = new Set(['failed', 'error', 'timeout'])

/** Pipelines whose latest run failed, newest failure first. */
export function failedPipelines(summary: PipelineSummary[]): PipelineSummary[] {
  return summary
    .filter((p) => FAILED.has(p.last_run_status))
    .sort((a, b) => (b.last_run_at ?? '').localeCompare(a.last_run_at ?? ''))
}

/** Open changes, the ones that can still be applied first. */
export function changesToReview(changes: WorkspaceChange[]): WorkspaceChange[] {
  return changes
    .filter((c) => c.status === 'open')
    .sort((a, b) => Number(a.stale) - Number(b.stale))
}

export function useNeedsYou() {
  const session = useSession()
  const enabled = session.status === 'authenticated'
  const summary = useQuery({
    queryKey: ['pipelines', 'summary'],
    queryFn: pipelineApi.summary,
    enabled,
    refetchInterval: 60_000,
  })
  const changes = useQuery({
    queryKey: ['changes', 'workspace', 'open'],
    queryFn: () => pipelineApi.workspaceChanges('open'),
    enabled,
    refetchInterval: 60_000,
    retry: false,
  })
  return useMemo(() => {
    const failed = failedPipelines(summary.data ?? [])
    const review = changesToReview(changes.data ?? [])
    return {
      failed,
      changes: review,
      ready: !summary.isPending && !changes.isPending,
      errors: [summary.isError && 'runs', changes.isError && 'changes'].filter(Boolean) as string[],
      total: failed.length + review.length,
    }
  }, [
    summary.data,
    summary.isPending,
    summary.isError,
    changes.data,
    changes.isPending,
    changes.isError,
  ])
}

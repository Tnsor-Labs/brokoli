import { describe, expect, it } from 'vitest'
import type { PipelineSummary, WorkspaceChange } from '@brokoli/api'
import { changesToReview, failedPipelines } from './needsYou'

const p = (id: string, status: string, at?: string) =>
  ({ id, name: id, last_run_status: status, last_run_at: at }) as PipelineSummary
const c = (id: string, status: WorkspaceChange['status'], stale = false) =>
  ({ id, status, stale }) as WorkspaceChange

describe('failedPipelines', () => {
  it('keeps pipelines whose latest run failed, newest failure first', () => {
    const got = failedPipelines([
      p('ok', 'success', '2026-10-20T10:00:00Z'),
      p('old', 'failed', '2026-10-19T10:00:00Z'),
      p('new', 'error', '2026-10-20T09:00:00Z'),
      p('never', ''),
    ])
    expect(got.map((x) => x.id)).toEqual(['new', 'old'])
  })
})

describe('changesToReview', () => {
  it('keeps open changes, the ones that can still be applied first', () => {
    expect(
      changesToReview([c('stale', 'open', true), c('done', 'applied'), c('fresh', 'open')]).map(
        (x) => x.id,
      ),
    ).toEqual(['fresh', 'stale'])
  })
})

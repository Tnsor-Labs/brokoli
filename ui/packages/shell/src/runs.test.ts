import { describe, expect, it } from 'vitest'
import { finishedRuns, remember, type LiveRun } from './runs'

const SINCE = Date.parse('2026-09-13T12:00:00Z')
const run = (run_id: string, status: string, finished_at?: string): LiveRun => ({ run_id, pipeline_id: 'p', pipeline_name: 'P', status, finished_at })

describe('finishedRuns', () => {
  it('reports a run seen running that is now terminal, including cancellations', () => {
    const seen = new Map([
      ['a', 'running'],
      ['b', 'running'],
      ['c', 'running'],
    ])
    const out = finishedRuns(seen, [run('a', 'success'), run('b', 'cancelled'), run('c', 'running')], SINCE)
    expect(out.map((r) => r.run_id)).toEqual(['a', 'b'])
  })

  it('does not report a run twice', () => {
    const seen = new Map<string, string>()
    remember(seen, [run('a', 'running')])
    const next = [run('a', 'failed', '2026-09-13T12:01:00Z')]
    expect(finishedRuns(seen, next, SINCE)).toHaveLength(1)
    remember(seen, next)
    expect(finishedRuns(seen, next, SINCE)).toHaveLength(0)
  })

  it('reports an unseen run only if it finished after the baseline', () => {
    const seen = new Map<string, string>()
    const out = finishedRuns(seen, [run('old', 'success', '2026-09-13T11:00:00Z'), run('quick', 'success', '2026-09-13T12:00:05Z'), run('nodate', 'failed')], SINCE)
    expect(out.map((r) => r.run_id)).toEqual(['quick'])
  })
})

describe('remember', () => {
  it('forgets runs no longer listed once the memory is large', () => {
    const seen = new Map<string, string>()
    for (let i = 0; i < 501; i++) seen.set(`old-${i}`, 'success')
    remember(seen, [run('now', 'running')])
    expect([...seen.keys()]).toEqual(['now'])
  })
})

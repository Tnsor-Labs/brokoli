import { describe, expect, it } from 'vitest'
import {
  canRedeliver,
  connectCommand,
  cooldownLabel,
  deliveryRun,
  deliveryStatus,
  lastOutcome,
  tokenState,
  triggerProblem,
} from './model'

const token = {
  id: 't',
  agent_id: 'a',
  name: 'laptop',
  prefix: 'bka_',
  on_behalf_of_owner: false,
  created_at: '2026-01-01T00:00:00Z',
}

describe('tokenState', () => {
  const now = Date.parse('2026-06-01T00:00:00Z')
  it('is revoked before anything else', () => {
    expect(
      tokenState(
        { ...token, expires_at: '2027-01-01T00:00:00Z', revoked_at: '2026-02-01T00:00:00Z' },
        now,
      ),
    ).toBe('revoked')
  })
  it('expires at its time', () => {
    expect(tokenState({ ...token, expires_at: '2026-06-01T00:00:00Z' }, now)).toBe('expired')
    expect(tokenState({ ...token, expires_at: '2026-06-01T00:00:01Z' }, now)).toBe('active')
  })
})

describe('connectCommand', () => {
  it('names the workspace only when there is one', () => {
    expect(connectCommand('https://b.example', 'ws1', 'tok')).toBe(
      'claude mcp add --transport http brokoli https://b.example/api/mcp --header "Authorization: Bearer tok" --header "X-Workspace-ID: ws1"',
    )
    expect(connectCommand('https://b.example', '', 'tok')).not.toContain('X-Workspace-ID')
  })
})

describe('cooldownLabel', () => {
  it('uses the largest whole unit', () => {
    expect(cooldownLabel(0)).toBe('none')
    expect(cooldownLabel(90)).toBe('90 seconds')
    expect(cooldownLabel(60)).toBe('1 minute')
    expect(cooldownLabel(1800)).toBe('30 minutes')
    expect(cooldownLabel(3600)).toBe('1 hour')
    expect(cooldownLabel(6 * 3600)).toBe('6 hours')
    expect(cooldownLabel(86400)).toBe('1 day')
  })
})

describe('deliveryStatus', () => {
  it('says a pending feed delivery waits for its agent', () => {
    expect(deliveryStatus({ status: 'pending' }, 'feed').label).toBe('Waiting to be read')
    expect(deliveryStatus({ status: 'delivered' }, 'feed').label).toBe('Read')
    expect(deliveryStatus({ status: 'pending' }, 'webhook').label).toBe('Pending')
    expect(deliveryStatus({ status: 'failed' }, 'webhook').tone).toBe('danger')
  })
})

describe('deliveryRun', () => {
  it('is the run, unless no run happened', () => {
    expect(deliveryRun({ run_id: 'r1' })).toBe('r1')
    expect(deliveryRun({ run_id: 'test-abc' })).toBeNull()
    expect(deliveryRun({ run_id: 'raised:orders:2026-11-01' })).toBeNull()
  })
})

describe('canRedeliver', () => {
  it('is for a finished webhook delivery', () => {
    expect(canRedeliver({ status: 'failed' }, 'webhook')).toBe(true)
    expect(canRedeliver({ status: 'delivered' }, 'webhook')).toBe(true)
    expect(canRedeliver({ status: 'pending' }, 'webhook')).toBe(false)
    expect(canRedeliver({ status: 'failed' }, 'feed')).toBe(false)
  })
})

describe('lastOutcome', () => {
  it('prefers the reason, then the error, then the status', () => {
    expect(
      lastOutcome({ reason: 'the trigger is disabled', last_error: 'x', last_status: 500 }),
    ).toBe('the trigger is disabled')
    expect(lastOutcome({ last_error: 'connection refused', last_status: 0 })).toBe(
      'connection refused',
    )
    expect(lastOutcome({ last_status: 502 })).toBe('HTTP 502')
    expect(lastOutcome({})).toBe('')
  })
})

describe('triggerProblem', () => {
  const ok = {
    delivery: 'webhook' as const,
    url: 'https://agents.example.com/brokoli',
    events: ['run.failed'],
  }
  it('accepts a complete trigger', () => {
    expect(triggerProblem(ok)).toBe('')
    expect(triggerProblem({ ...ok, delivery: 'feed', url: '' })).toBe('')
  })
  it('names what is missing', () => {
    expect(triggerProblem({ ...ok, events: [] })).toMatch(/event/)
    expect(triggerProblem({ ...ok, url: ' ' })).toMatch(/URL/)
    expect(triggerProblem({ ...ok, url: 'agents.example.com' })).toMatch(/not a URL/)
    expect(triggerProblem({ ...ok, url: 'ftp://agents.example.com' })).toMatch(/https/)
  })
})

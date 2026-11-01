import { describe, expect, it } from 'vitest'
import { firstLine, inboxCount, levelTone, linkLabel, linkPath, unreadNeedingYou } from './inbox'

describe('inbox', () => {
  it('sends each link to its page in the app', () => {
    expect(linkPath({ kind: 'pipeline', id: 'orders' })).toBe('/pipelines/orders/runs')
    expect(linkPath({ kind: 'run', id: 'r 1', pipeline_id: 'orders' })).toBe(
      '/pipelines/orders/runs?run=r%201',
    )
    expect(linkPath({ kind: 'change_request', id: 'cr1', pipeline_id: 'orders' })).toBe(
      '/pipelines/orders/changes/cr1',
    )
    expect(linkPath({ kind: 'run', id: 'r1' })).toBeNull()
  })
  it('labels a link by what the server named it', () => {
    expect(linkLabel({ kind: 'pipeline', id: 'p', label: 'orders_daily' })).toBe('orders_daily')
    expect(linkLabel({ kind: 'run', id: '01a1959b-74f3' })).toBe('Run 01a1959b')
  })
  it('colours the levels', () => {
    expect(levelTone('urgent')).toBe('danger')
    expect(levelTone('attention')).toBe('warning')
    expect(levelTone('info')).toBe('neutral')
  })
  it('counts alerts and what needs you on the bell', () => {
    expect(inboxCount(2, 3)).toBe(5)
    const m = {
      id: '',
      conversation_id: '',
      from: 'agent' as const,
      text: '',
      links: [],
      created_at: '',
      agent_id: '',
      agent_name: '',
      agent_avatar: '',
    }
    expect(
      unreadNeedingYou([
        { ...m, level: 'attention', shown_level: 'attention' },
        { ...m, level: 'urgent', shown_level: 'info', muted: true },
        { ...m, level: 'info', shown_level: 'info' },
        { ...m, level: 'urgent', shown_level: 'urgent', read_at: 'x' },
      ]),
    ).toBe(1)
  })
  it('shows the first line of a message', () => {
    expect(firstLine('one\ntwo')).toBe('one')
    expect(firstLine('x'.repeat(200), 10)).toBe('xxxxxxxxx…')
  })
})

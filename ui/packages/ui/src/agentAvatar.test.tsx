// @vitest-environment jsdom
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AGENT_AVATARS, AgentAvatar, isAgentAvatar } from './agentAvatar'

describe('AgentAvatar', () => {
  it('lists the family in the order the server draws defaults from', () => {
    // api.AgentAvatars: an agent with no choice gets AgentAvatars[fnv(id) % 8],
    // so this order is shared with the server and must not change.
    expect(AGENT_AVATARS.map((a) => a.name)).toEqual([
      'broccoli',
      'romanesco',
      'cauliflower',
      'purple',
      'broccolini',
      'sprout',
      'kohlrabi',
      'seedling',
    ])
    expect(new Set(AGENT_AVATARS.map((a) => a.mood)).size).toBe(8)
  })

  it('draws each on its own tile, with a face', () => {
    for (const { name } of AGENT_AVATARS) {
      const { container, unmount } = render(<AgentAvatar avatar={name} size={24} />)
      const svg = container.querySelector('svg')!
      expect(svg.getAttribute('width')).toBe('24')
      expect(svg.querySelector('rect')!.getAttribute('fill')).toBe(`var(--bk-color-avatar-${name})`)
      // The face is drawn in the eye ink.
      expect(svg.innerHTML).toContain('var(--bk-color-avatar-eye)')
      unmount()
    }
  })

  it('draws the broccoli for a name it does not know', () => {
    const { container } = render(<AgentAvatar avatar="carrot" />)
    expect(container.querySelector('rect')!.getAttribute('fill')).toBe(
      'var(--bk-color-avatar-broccoli)',
    )
    expect(isAgentAvatar('carrot')).toBe(false)
    expect(isAgentAvatar('toString')).toBe(false)
    expect(isAgentAvatar('kohlrabi')).toBe(true)
  })

  it('is hidden from assistive technology unless it is labelled', () => {
    const plain = render(<AgentAvatar avatar="seedling" />).container.querySelector('svg')!
    expect(plain.getAttribute('aria-hidden')).toBe('true')
    const named = render(
      <AgentAvatar avatar="seedling" label="triage-bot" />,
    ).container.querySelector('svg')!
    expect(named.getAttribute('role')).toBe('img')
    expect(named.getAttribute('aria-label')).toBe('triage-bot')
  })
})

// @vitest-environment jsdom
import { fireEvent, render } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { SidebarAccount } from './guards'

vi.mock('./session', () => ({
  useSession: () => ({
    user: { id: 'u1', username: 'ada', display_name: 'Ada Lovelace', role: 'admin' },
    logout: () => {},
  }),
}))

const renderAccount = (avatarSrc?: string | null) =>
  render(
    <MemoryRouter>
      <SidebarAccount profileHref="/profile" avatarSrc={avatarSrc} />
    </MemoryRouter>,
  )

describe('SidebarAccount', () => {
  it('shows the initials without a photo', () => {
    const { container } = renderAccount()
    const avatar = container.querySelector('.bk-account-avatar')!
    expect(avatar.querySelector('img')).toBeNull()
    expect(avatar.textContent).toBe('AL')
  })

  it('shows the photo when the application knows one', () => {
    const { container } = renderAccount('https://example.com/api/media/avatars/a.jpg')
    const img = container.querySelector('.bk-account-avatar img')
    expect(img?.getAttribute('src')).toBe('https://example.com/api/media/avatars/a.jpg')
  })

  // A photo that no longer loads (deleted, or the address changed) must not
  // leave an empty circle: the initials come back.
  it('falls back to the initials when the photo fails to load', () => {
    const { container } = renderAccount('https://example.com/missing.jpg')
    fireEvent.error(container.querySelector('.bk-account-avatar img')!)
    const avatar = container.querySelector('.bk-account-avatar')!
    expect(avatar.querySelector('img')).toBeNull()
    expect(avatar.textContent).toBe('AL')
  })
})

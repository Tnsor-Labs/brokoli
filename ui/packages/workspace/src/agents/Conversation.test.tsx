// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '@brokoli/ui'

const api = vi.hoisted(() => ({
  conversation: vi.fn(),
  reply: vi.fn(),
  mute: vi.fn(),
}))
vi.mock('@brokoli/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@brokoli/api')>()
  return { ...real, messagesApi: { ...real.messagesApi, ...api } }
})

import { Conversation } from './Conversation'

const thread = (extra: object[] = []) => ({
  conversation: { id: 'c1', muted: false },
  agent: { id: 'a1', name: 'triage-bot', avatar: 'kohlrabi', owner_name: 'ada' },
  messages: [
    { id: 'm1', conversation_id: 'c1', from: 'agent', level: 'attention', topic: 'orders', text: 'It failed twice.', links: [{ kind: 'run', id: 'r1', pipeline_id: 'p1' }], created_at: '2026-11-01T08:00:00Z' },
    ...extra,
  ],
})

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <MemoryRouter>
          <Conversation conversationId="c1" />
        </MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('Conversation', () => {
  it('shows the thread with the agent and the person apart, and the links', async () => {
    api.conversation.mockResolvedValue(
      thread([{ id: 'm2', conversation_id: 'c1', from: 'person', level: 'info', text: 'Leave billing alone.', links: [], created_at: '2026-11-01T08:05:00Z' }]),
    )
    mount()
    expect(await screen.findByText('It failed twice.')).toBeTruthy()
    expect(screen.getByText('Needs attention')).toBeTruthy()
    expect(screen.getByText('You')).toBeTruthy()
    expect(screen.getByText('Leave billing alone.').closest('li')?.className).toContain('is-person')
    expect(screen.getByRole('link', { name: 'Run r1' }).getAttribute('href')).toBe('/pipelines/p1/runs?run=r1')
  })

  it('sends a reply, and says when the agent is not told at once', async () => {
    api.conversation.mockResolvedValue(thread())
    api.reply.mockResolvedValue({ message: {}, told: 0, note: 'triage-bot has no trigger for messages, so it is not told at once; it reads them when it checks.' })
    mount()
    const box = await screen.findByLabelText('Write to triage-bot')
    const send = screen.getByRole('button', { name: 'Send' })
    expect((send as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(box, { target: { value: 'Look at billing.' } })
    fireEvent.click(send)
    await waitFor(() => expect(api.reply).toHaveBeenCalledWith('c1', 'Look at billing.'))
    expect(await screen.findByText(/no trigger for messages/)).toBeTruthy()
    expect((box as HTMLTextAreaElement).value).toBe('')
  })

  it('sends with Ctrl+Enter', async () => {
    api.conversation.mockResolvedValue(thread())
    api.reply.mockResolvedValue({ message: {}, told: 1 })
    mount()
    const box = await screen.findByLabelText('Write to triage-bot')
    fireEvent.change(box, { target: { value: 'Thanks.' } })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(api.reply).toHaveBeenCalledWith('c1', 'Thanks.'))
  })

  it('offers nothing to write to a disabled agent', async () => {
    api.conversation.mockResolvedValue({ ...thread(), agent: { id: 'a1', name: 'triage-bot', disabled: true } })
    mount()
    expect(await screen.findByText(/is disabled; nothing you write reaches it/)).toBeTruthy()
    expect(screen.queryByLabelText('Write to triage-bot')).toBeNull()
  })
})

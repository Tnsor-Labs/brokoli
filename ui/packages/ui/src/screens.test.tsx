// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ErrorScreen } from './screens'

afterEach(() => {
  cleanup()
  sessionStorage.clear()
})

const stale = new TypeError(
  'error loading dynamically imported module: https://x/assets/HomePage-old.js',
)

describe('ErrorScreen', () => {
  it('reloads once for a page of an earlier build', () => {
    const reload = vi.fn()
    render(<ErrorScreen error={stale} reload={reload} />)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(screen.getByText('Loading the new version of Brokoli')).toBeTruthy()
  })

  it('asks instead when it has just reloaded', () => {
    render(<ErrorScreen error={stale} reload={vi.fn()} />)
    cleanup()
    const reload = vi.fn()
    render(<ErrorScreen error={stale} reload={reload} />)
    expect(reload).not.toHaveBeenCalled()
    expect(screen.getByText('Brokoli has been updated')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /reload/i }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('says what failed otherwise, without reloading', () => {
    const reload = vi.fn()
    render(<ErrorScreen error={new Error('boom')} reload={reload} />)
    expect(reload).not.toHaveBeenCalled()
    expect(screen.getByText('This page could not be shown')).toBeTruthy()
    expect(screen.getByText('boom')).toBeTruthy()
    cleanup()
    render(<ErrorScreen error={{ status: 404, statusText: 'Not Found' }} reload={reload} />)
    expect(screen.getByText('404 Not Found')).toBeTruthy()
  })
})

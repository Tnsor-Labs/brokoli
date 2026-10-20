import { describe, expect, it } from 'vitest'
import { isStaleBuildError, shouldReloadForNewBuild } from './staleBuild'

describe('isStaleBuildError', () => {
  it('knows what each browser says about a module of an earlier build', () => {
    for (const message of [
      'error loading dynamically imported module: https://x/assets/HomePage-DR5tvTgO.js', // Firefox
      'Failed to fetch dynamically imported module: https://x/assets/HomePage-DR5tvTgO.js', // Chrome
      'Importing a module script failed.', // Safari
      "Failed to load module script: Expected a JavaScript module script but the server responded with a MIME type of 'text/html'.",
    ]) {
      expect(isStaleBuildError(new TypeError(message)), message).toBe(true)
    }
    const chunk = new Error('Loading chunk 42 failed.')
    chunk.name = 'ChunkLoadError'
    expect(isStaleBuildError(chunk)).toBe(true)
  })

  it('leaves other failures alone', () => {
    for (const error of [
      new Error('Cannot read properties of undefined'),
      new TypeError('Failed to fetch'),
      { status: 404, statusText: 'Not Found' },
      undefined,
    ]) {
      expect(isStaleBuildError(error)).toBe(false)
    }
  })
})

describe('shouldReloadForNewBuild', () => {
  const store = () => {
    const data = new Map<string, string>()
    return {
      getItem: (k: string) => data.get(k) ?? null,
      setItem: (k: string, v: string) => void data.set(k, v),
    }
  }

  it('reloads once, and again only after half a minute', () => {
    const s = store()
    expect(shouldReloadForNewBuild(1_000_000, s)).toBe(true)
    expect(shouldReloadForNewBuild(1_000_000 + 29_999, s)).toBe(false) // the new build fails too: no loop
    expect(shouldReloadForNewBuild(1_000_000 + 30_000, s)).toBe(true)
  })

  it('does not reload without storage to remember it in', () => {
    expect(shouldReloadForNewBuild(1, null)).toBe(false)
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => undefined,
    }
    expect(shouldReloadForNewBuild(1, broken)).toBe(false)
  })
})

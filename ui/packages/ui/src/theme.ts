import { useCallback, useEffect, useState } from 'react'

export type Theme = 'dark' | 'light'

/*
 * Same storage key and values as the Svelte UI, so a user's choice carries
 * over. index.html applies it before first paint; this hook keeps React in
 * sync and persists changes.
 */
export const THEME_KEY = 'brokoli-theme'

export function readTheme(): Theme {
  try {
    return localStorage.getItem(THEME_KEY) === 'dark' ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'light' ? '#f3f3f0' : '#08090a')
}

const THEME_EVENT = 'brokoli-theme-change'

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(readTheme)
  useEffect(() => applyTheme(theme), [theme])
  // Several components use this hook (the account block, the search palette); keep them in step.
  useEffect(() => {
    const onChange = (e: Event) => setThemeState((e as CustomEvent<Theme>).detail)
    window.addEventListener(THEME_EVENT, onChange)
    return () => window.removeEventListener(THEME_EVENT, onChange)
  }, [])
  const setTheme = useCallback((next: Theme) => {
    try {
      localStorage.setItem(THEME_KEY, next)
    } catch {
      /* Storage can be unavailable (private mode); the choice then lasts for this tab. */
    }
    setThemeState(next)
    window.dispatchEvent(new CustomEvent<Theme>(THEME_EVENT, { detail: next }))
  }, [])
  const toggle = useCallback(() => setTheme(theme === 'dark' ? 'light' : 'dark'), [theme, setTheme])
  return { theme, setTheme, toggle }
}

import { useSyncExternalStore } from 'react'

/*
 * Which global overlay is open. Only one can be: opening the search palette
 * closes the shortcut list and the other way round, so a single Escape never
 * has two overlays to close.
 */
export type Overlay = 'search' | 'help' | null

let current: Overlay = null
const listeners = new Set<() => void>()

export function openOverlay(next: Overlay) {
  if (next === current) return
  current = next
  listeners.forEach((listener) => listener())
}

export const currentOverlay = () => current

export function useOverlay(): Overlay {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    () => current,
  )
}

export const isMac = () => typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
export const modKey = () => (isMac() ? '⌘' : 'Ctrl')

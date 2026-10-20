/*
 * A tab opened before a deploy still runs the old build. The pages it has
 * not loaded yet are files of that build, which the server no longer has,
 * so opening one fails with a module error rather than anything a person
 * can act on. These helpers tell that failure from others, and reload the
 * page once to pick up the new build.
 */

// What browsers and bundlers say when a lazily imported module cannot load.
const STALE_BUILD = [
  /dynamically imported module/i, // Chrome, Firefox
  /importing a module script failed/i, // Safari
  /error loading dynamically imported module/i,
  /chunkloaderror|loading chunk .+ failed/i,
  /module script.*mime type/i,
]

export function isStaleBuildError(error: unknown): boolean {
  const text =
    error instanceof Error
      ? `${error.name}: ${error.message}`
      : typeof error === 'string'
        ? error
        : ''
  return STALE_BUILD.some((pattern) => pattern.test(text))
}

const RELOADED_AT = 'brokoli:reloaded-for-new-build'

/**
 * Whether to reload for a new build now: yes, unless this tab already did
 * in the last half minute (the new build is broken too, or the server is
 * still rolling out; reloading again would loop). Records the reload.
 */
export function shouldReloadForNewBuild(
  now = Date.now(),
  storage: Pick<Storage, 'getItem' | 'setItem'> | null = session(),
): boolean {
  if (!storage) return false
  try {
    const last = Number(storage.getItem(RELOADED_AT) ?? 0)
    if (now - last < 30_000) return false
    storage.setItem(RELOADED_AT, String(now))
    return true
  } catch {
    return false
  }
}

function session(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage
  } catch {
    return null
  }
}

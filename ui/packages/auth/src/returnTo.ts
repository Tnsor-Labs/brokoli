/*
 * Where to go after a sign-in that leaves the page.
 *
 * A password sign-in keeps the intended route in router state. A sign-in
 * through an identity provider cannot: the provider redirects to the
 * application's origin and a fragment in the redirect address is refused,
 * so a device-approval or invite link would be lost. The route is kept in
 * sessionStorage for the length of the round trip instead.
 */

const KEY = 'brokoli-return-to'

/** Only an in-app route: starts with one slash, no scheme, no backslash, not the sign-in page. */
export function isSafeReturn(path: string | null | undefined): path is string {
  if (!path || !path.startsWith('/') || path.startsWith('//')) return false
  if (path.includes('\\') || path.includes('://')) return false
  return !/^\/(login|auth-callback)(\/|\?|$)/.test(path)
}

export function rememberReturn(path: string | null | undefined) {
  try {
    if (isSafeReturn(path)) sessionStorage.setItem(KEY, path)
    else sessionStorage.removeItem(KEY)
  } catch {
    /* Storage can be unavailable; the sign-in then lands on the default page. */
  }
}

/** Reads and forgets the remembered route. */
export function takeReturn(): string | null {
  try {
    const path = sessionStorage.getItem(KEY)
    sessionStorage.removeItem(KEY)
    return isSafeReturn(path) ? path : null
  } catch {
    return null
  }
}

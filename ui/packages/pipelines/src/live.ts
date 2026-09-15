import { useEffect, useRef, useState } from 'react'
import { dashboardKey, onLiveConnection, watchKey } from '@brokoli/api'
import { useSession } from '@brokoli/auth'

/*
 * The dashboard key changes on every run and node lifecycle event in the
 * organisation. Pages use it as a tripwire to refetch over REST: the first
 * callback is the baseline (cached value or STATE_INIT) and is skipped;
 * every later one, including a fresh STATE_INIT after a long disconnect,
 * schedules a refetch after a short debounce so a burst of events costs one
 * request.
 */
export function useRunActivity(onActivity: () => void, enabled = true) {
  const { user } = useSession()
  const callback = useRef(onActivity)
  callback.current = onActivity
  const orgId = user?.org_id
  const signedIn = Boolean(user)
  useEffect(() => {
    if (!enabled || !signedIn) return
    let first = true
    let timer: ReturnType<typeof setTimeout> | undefined
    const off = watchKey(dashboardKey(orgId), () => {
      if (first) {
        first = false
        return
      }
      clearTimeout(timer)
      timer = setTimeout(() => callback.current(), 150)
    })
    return () => {
      clearTimeout(timer)
      off()
    }
  }, [enabled, signedIn, orgId])
}

export function useLiveConnected() {
  const [connected, setConnected] = useState(false)
  useEffect(() => onLiveConnection(setConnected), [])
  return connected
}

/** Re-render on an interval so relative times ("3 minutes ago") stay true. */
export function useNow(intervalMs = 30_000) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

/*
 * Run activity arrives in bursts: every node start and finish of every run
 * in the organisation changes the dashboard key. Some of the endpoints these
 * pages read are expensive on the server, so refreshes are throttled to one
 * per `gapMs`, with a trailing refresh so the last change is never missed.
 */
export function useThrottledActivity(onActivity: () => void, gapMs: number) {
  const last = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const callback = useRef(onActivity)
  callback.current = onActivity
  useEffect(() => () => clearTimeout(timer.current), [])
  useRunActivity(() => {
    const fire = () => {
      timer.current = undefined
      last.current = Date.now()
      callback.current()
    }
    const wait = last.current + gapMs - Date.now()
    if (wait <= 0) fire()
    else if (!timer.current) timer.current = setTimeout(fire, wait)
  })
}

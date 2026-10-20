import { useEffect, useState, type ReactNode } from 'react'
import { Construction, RefreshCw, TriangleAlert } from 'lucide-react'
import { Brand, Button, Spinner } from './primitives'
import { EmptyState, errorMessage } from './feedback'
import { isStaleBuildError, shouldReloadForNewBuild } from './staleBuild'

export function BootScreen({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="bk-boot" role="status" aria-live="polite">
      <Brand />
      <Spinner />
      <span>{label}</span>
    </div>
  )
}

export function PageLoading({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="bk-page-loading" role="status" aria-live="polite">
      <Spinner />
      <span>{label}</span>
    </div>
  )
}

/**
 * A route whose Svelte page has not been ported yet. Says so plainly rather
 * than rendering an empty frame.
 */
export function NotMigrated({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="bk-not-migrated">
      <EmptyState icon={<Construction size={20} aria-hidden="true" />} title={`${title} is not in the new interface yet`}>
        {children ?? 'This area is still being migrated from the previous interface. Pipelines, their editor and their runs are available now.'}
      </EmptyState>
    </div>
  )
}

/**
 * What a route shows when it fails to render, in place of the router's
 * developer screen. A page of an earlier build (the tab was opened before
 * a deploy) reloads once to get the new build; past that, and for any
 * other failure, it says what happened and offers a reload.
 */
export function ErrorScreen({ error, reload = () => window.location.reload() }: { error: unknown; reload?: () => void }) {
  const stale = isStaleBuildError(error)
  const [reloading] = useState(() => stale && shouldReloadForNewBuild())
  useEffect(() => {
    if (reloading) reload()
  }, [reloading, reload])
  if (reloading) return <BootScreen label="Loading the new version of Brokoli" />
  const action = (
    <Button variant="primary" icon={<RefreshCw size={14} aria-hidden="true" />} onClick={reload}>
      Reload
    </Button>
  )
  return (
    <div className="bk-not-migrated" role="alert">
      {stale ? (
        <EmptyState icon={<RefreshCw size={20} aria-hidden="true" />} title="Brokoli has been updated" action={action}>
          This page was opened before the update. Reload to get the new version; nothing you saved is lost.
        </EmptyState>
      ) : (
        <EmptyState icon={<TriangleAlert size={20} aria-hidden="true" />} title="This page could not be shown" action={action}>
          {describe(error)}
        </EmptyState>
      )}
    </div>
  )
}

// A router's error response ({status, statusText}) or any thrown value.
function describe(error: unknown): string {
  if (error && typeof error === 'object' && 'status' in error && 'statusText' in error) {
    const { status, statusText } = error as { status: unknown; statusText: unknown }
    return `${status} ${statusText}`.trim()
  }
  return errorMessage(error) || 'Something went wrong.'
}

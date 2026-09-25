import type { ReactNode } from 'react'
import { Construction } from 'lucide-react'
import { Brand, Spinner } from './primitives'
import { EmptyState } from './feedback'

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

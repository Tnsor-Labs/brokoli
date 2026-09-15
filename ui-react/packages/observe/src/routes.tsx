import { lazy, Suspense, type ReactNode } from 'react'
import type { RouteObject } from 'react-router-dom'
import { PageLoading } from '@brokoli/ui'

const DashboardPage = lazy(() => import('./dashboard/DashboardPage').then((m) => ({ default: m.DashboardPage })))
const CalendarPage = lazy(() => import('./calendar/CalendarPage').then((m) => ({ default: m.CalendarPage })))
const LineagePage = lazy(() => import('./lineage/LineagePage').then((m) => ({ default: m.LineagePage })))
const DependenciesPage = lazy(() => import('./dependencies/DependenciesPage').then((m) => ({ default: m.DependenciesPage })))

function Deferred({ children, label }: { children: ReactNode; label: string }) {
  return <Suspense fallback={<PageLoading label={label} />}>{children}</Suspense>
}

/**
 * Observability pages shared by Community and Enterprise. Enterprise passes
 * its own dashboard element (the shared dashboard plus an enterprise band)
 * through `dashboard`; Community uses the default.
 */
export function observeRoutes({ dashboard }: { dashboard?: ReactNode } = {}): RouteObject[] {
  return [
    { path: 'dashboard', element: <Deferred label="Loading dashboard">{dashboard ?? <DashboardPage />}</Deferred> },
    { path: 'calendar', element: <Deferred label="Loading calendar"><CalendarPage /></Deferred> },
    { path: 'lineage', element: <Deferred label="Loading lineage"><LineagePage /></Deferred> },
    { path: 'dependencies', element: <Deferred label="Loading dependencies"><DependenciesPage /></Deferred> },
  ]
}

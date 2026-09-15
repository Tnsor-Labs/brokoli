import { lazy, Suspense, type ReactNode } from 'react'
import type { RouteObject } from 'react-router-dom'
import { PageLoading } from '@brokoli/ui'

const ConnectionsPage = lazy(() => import('./connections/ConnectionsPage').then((m) => ({ default: m.ConnectionsPage })))
const VariablesPage = lazy(() => import('./variables/VariablesPage').then((m) => ({ default: m.VariablesPage })))
const PluginsPage = lazy(() => import('./plugins/PluginsPage').then((m) => ({ default: m.PluginsPage })))
const SettingsPage = lazy(() => import('./settings/SettingsPage').then((m) => ({ default: m.SettingsPage })))

function Deferred({ children, label }: { children: ReactNode; label: string }) {
  return <Suspense fallback={<PageLoading label={label} />}>{children}</Suspense>
}

/** Workspace resource and settings pages shared by Community and Enterprise. */
export function workspaceRoutes(): RouteObject[] {
  return [
    { path: 'connections', element: <Deferred label="Loading connections"><ConnectionsPage /></Deferred> },
    { path: 'variables', element: <Deferred label="Loading variables"><VariablesPage /></Deferred> },
    { path: 'plugins', element: <Deferred label="Loading plugins"><PluginsPage /></Deferred> },
    { path: 'settings', element: <Deferred label="Loading settings"><SettingsPage /></Deferred> },
    // The previous interface had a separate API page; its content is the API tab now.
    { path: 'api', element: <Deferred label="Loading settings"><SettingsPage initialTab="api" /></Deferred> },
  ]
}

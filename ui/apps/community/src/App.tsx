import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Navigate, Outlet, RouterProvider, createHashRouter, useRouteError } from 'react-router-dom'
import { HomePage } from './home/HomePage'
import { LoginRoute, OAuthConsentPage, RequireAuth, SessionProvider } from '@brokoli/auth'
import { observeRoutes } from '@brokoli/observe'
import { pipelineRoutes } from '@brokoli/pipelines'
import { ErrorScreen, NotMigrated, ToastProvider } from '@brokoli/ui'
import { agentRoutes, workspaceRoutes } from '@brokoli/workspace'
import { Shell } from './Shell'

/*
 * Community application. Hash routing keeps the URLs the Go server and the
 * SSO callback already produce (#/pipelines, #/auth-callback). A data router
 * is required for the editor's unsaved-changes guard (useBlocker).
 */
// A page that fails to render: inside the shell when the shell itself
// rendered, else in place of everything.
function RouteError() {
  return <ErrorScreen error={useRouteError()} />
}

const router = createHashRouter([
  {
    element: <Outlet />,
    errorElement: <RouteError />,
    children: [
      { path: '/login', element: <LoginRoute defaultPath="/home" /> },
      {
        element: <RequireAuth />,
        children: [
          // Approving an MCP client's connection (OAuth), outside the shell.
          { path: '/oauth/authorize', element: <OAuthConsentPage /> },
          {
            element: <Shell />,
            children: [
              {
                errorElement: <RouteError />,
                children: [
                  { index: true, element: <Navigate to="/home" replace /> },
                  { path: 'home', element: <HomePage /> },
                  ...pipelineRoutes(),
                  ...workspaceRoutes(),
                  ...agentRoutes(),
                  ...observeRoutes(),
                  {
                    path: '*',
                    element: (
                      <NotMigrated title="This page">There is no page at this address.</NotMigrated>
                    ),
                  },
                ],
              },
            ],
          },
        ],
      },
    ],
  },
])

export function App() {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          // The API client already retries idempotent reads on network and gateway errors.
          queries: { retry: false, staleTime: 10_000, refetchOnWindowFocus: true },
          mutations: { retry: false },
        },
      }),
  )
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <SessionProvider onSignedOut={() => queryClient.clear()}>
          <RouterProvider router={router} />
        </SessionProvider>
      </ToastProvider>
    </QueryClientProvider>
  )
}

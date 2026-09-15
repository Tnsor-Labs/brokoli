import { lazy, Suspense, type ReactNode } from 'react'
import { Navigate, useParams, type RouteObject } from 'react-router-dom'
import { PageLoading } from '@brokoli/ui'
import { PipelinesPage } from './list/PipelinesPage'

const RunsPage = lazy(() => import('./runs/RunsPage').then((m) => ({ default: m.RunsPage })))
const GridPage = lazy(() => import('./grid/GridPage').then((m) => ({ default: m.GridPage })))
const FullTimelinePage = lazy(() => import('./runs/FullTimelinePage').then((m) => ({ default: m.FullTimelinePage })))
const EditorPage = lazy(() => import('@brokoli/pipeline-session').then((m) => ({ default: m.PipelineEditorPage })))

function Deferred({ children, label }: { children: ReactNode; label: string }) {
  return <Suspense fallback={<PageLoading label={label} />}>{children}</Suspense>
}

/** Keys the page on the pipeline id so moving between two pipelines remounts it with fresh state. */
function Keyed({ render }: { render: (id: string) => ReactNode }) {
  const { id = '' } = useParams()
  return <>{render(id)}</>
}

function RunKeyed({ render }: { render: (id: string, runId: string) => ReactNode }) {
  const { id = '', runId = '' } = useParams()
  return <>{render(id, runId)}</>
}

/*
 * Routes for the pipelines area, shared by the Community and Enterprise
 * applications. Each application mounts them inside its own shell.
 *
 * `pipelineExtra` lets an application add its own content to the runs page
 * header for a pipeline (the Enterprise app puts the owner and team there);
 * the shared code only renders whatever node it is handed, so it stays
 * unaware of enterprise concepts.
 */
export function pipelineRoutes(opts: { pipelineExtra?: (pipelineId: string) => ReactNode } = {}): RouteObject[] {
  return [
    { path: 'pipelines', element: <PipelinesPage /> },
    { path: 'pipelines/:id', element: <Keyed render={(id) => <Navigate to={`/pipelines/${encodeURIComponent(id)}/runs`} replace />} /> },
    {
      path: 'pipelines/:id/runs',
      element: (
        <Keyed
          render={(id) => (
            <Deferred label="Loading runs">
              <RunsPage key={id} pipelineId={id} extra={opts.pipelineExtra?.(id)} />
            </Deferred>
          )}
        />
      ),
    },
    {
      path: 'pipelines/:id/runs/:runId/gantt',
      element: (
        <RunKeyed
          render={(id, runId) => (
            <Deferred label="Loading timeline">
              <FullTimelinePage key={`${id}/${runId}`} pipelineId={id} runId={runId} />
            </Deferred>
          )}
        />
      ),
    },
    {
      path: 'pipelines/:id/grid',
      element: (
        <Keyed
          render={(id) => (
            <Deferred label="Loading run grid">
              <GridPage key={id} pipelineId={id} />
            </Deferred>
          )}
        />
      ),
    },
    {
      path: 'pipelines/:id/edit',
      element: (
        <Keyed
          render={(id) => (
            <Deferred label="Loading editor">
              <EditorPage key={id} pipelineId={id} />
            </Deferred>
          )}
        />
      ),
    },
  ]
}

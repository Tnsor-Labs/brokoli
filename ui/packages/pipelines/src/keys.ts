export const keys = {
  summary: ['pipelines', 'summary'] as const,
  scheduler: ['scheduler', 'status'] as const,
  templates: ['templates'] as const,
  pipeline: (id: string) => ['pipeline', id] as const,
  runs: (id: string) => ['pipeline', id, 'runs'] as const,
  plan: (id: string) => ['pipeline', id, 'plan'] as const,
  grid: (id: string, runs: number) => ['pipeline', id, 'grid', runs] as const,
  run: (runId: string) => ['run', runId] as const,
  runLogs: (runId: string) => ['run', runId, 'logs'] as const,
  runEvents: (runId: string) => ['run', runId, 'events'] as const,
  runInstances: (runId: string) => ['run', runId, 'instances'] as const,
  nodePreview: (runId: string, nodeId: string) => ['run', runId, 'preview', nodeId] as const,
  nodeProfile: (runId: string, nodeId: string) => ['run', runId, 'profile', nodeId] as const,
  nodeStats: (id: string) => ['pipeline', id, 'node-stats'] as const,
}

export const paths = {
  list: '/pipelines',
  runs: (id: string, runId?: string) => `/pipelines/${encodeURIComponent(id)}/runs${runId ? `?run=${encodeURIComponent(runId)}` : ''}`,
  editor: (id: string) => `/pipelines/${encodeURIComponent(id)}/edit`,
  grid: (id: string) => `/pipelines/${encodeURIComponent(id)}/grid`,
  timeline: (id: string, runId: string) => `/pipelines/${encodeURIComponent(id)}/runs/${encodeURIComponent(runId)}/gantt`,
}

/** Status groups shared by the list, the runs page and the grid. */
export const SUCCESS = new Set(['success', 'succeeded', 'completed'])
export const FAILURE = new Set(['failed', 'error', 'timeout'])
export const ACTIVE = new Set(['running', 'pending', 'queued', 'waiting', 'retrying'])

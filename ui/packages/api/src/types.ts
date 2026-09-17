/*
 * Wire types for the Brokoli OSS API, checked against the Go models on
 * origin/main (models/pipeline.go, models/run.go, api/handlers_*.go).
 *
 * The pipeline document is decoded strictly by the server
 * (DisallowUnknownFields at every nesting level), so these interfaces must
 * never gain UI-only fields, and code that edits a pipeline must start from
 * the object the server returned rather than rebuilding it from these types.
 */

export type UserRole = 'superadmin' | 'admin' | 'editor' | 'operator' | 'viewer'

export interface AuthUser {
  id: string
  username: string
  display_name?: string
  email?: string
  /** Server roles plus anything an enterprise role table adds. */
  role: UserRole | (string & {})
  org_id?: string
}

/** Raw claims returned by GET /api/auth/me. */
export interface AuthClaims {
  sub: string
  username: string
  role: string
  exp?: number
  display_name?: string
  email?: string
  org_id?: string
}

export interface AuthMethods {
  password: boolean
  oauth: string[]
}

export interface Workspace {
  id: string
  name: string
  slug?: string
  description?: string
}

export const KNOWN_NODE_TYPES = [
  'source_file',
  'source_api',
  'source_db',
  'transform',
  'quality_check',
  'sql_generate',
  'code',
  'join',
  'sink_file',
  'sink_db',
  'sink_api',
  'migrate',
  'condition',
  'wait',
  'dbt',
  'notify',
  'union',
  'dataset_map',
  'dataset_filter',
  'task',
] as const

export type KnownNodeType = (typeof KNOWN_NODE_TYPES)[number]
/** Plugins and enterprise executors can add types the UI has never heard of. */
export type NodeType = KnownNodeType | (string & {})

export interface Position {
  x: number
  y: number
}

export interface PipelineNode {
  id: string
  type: NodeType
  name: string
  config: Record<string, unknown>
  position: Position
  capabilities?: string[]
  /** ADR-032 task interface. Opaque to the UI, but it must round-trip. */
  interface?: Record<string, unknown>
}

export interface PipelineEdge {
  from: string
  to: string
  condition?: boolean
  from_port?: string
  to_port?: string
}

export type DependencyState = 'succeeded' | 'completed' | 'failed'
export type DependencyMode = 'gate' | 'trigger'

export interface DependencyRule {
  pipeline_id: string
  state?: DependencyState
  within_seconds?: number
  mode?: DependencyMode
}

export interface Pipeline {
  id: string
  ir_version?: string
  name: string
  description: string
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  schedule: string
  catchup?: boolean
  draft?: boolean
  webhook_url?: string
  params?: Record<string, string>
  parameters?: Record<string, unknown>
  tags?: string[] | null
  hooks?: Record<string, unknown>
  schedule_timezone?: string
  sla_deadline?: string
  sla_timezone?: string
  depends_on?: string[] | null
  dependency_rules?: DependencyRule[] | null
  webhook_token?: string
  extensions?: Record<string, unknown>
  enabled: boolean
  pipeline_id?: string
  source?: 'ui' | 'git' | (string & {})
  workspace_id?: string
  org_id?: string
  created_at?: string
  updated_at?: string
}

/** Fields accepted by POST /api/pipelines. */
export type PipelineCreate = Pick<Pipeline, 'name'> &
  Partial<Pick<Pipeline, 'description' | 'enabled' | 'nodes' | 'edges' | 'draft' | 'schedule' | 'schedule_timezone' | 'tags'>>

/** One row of GET /api/pipelines/summary. */
export interface PipelineSummary {
  id: string
  name: string
  description: string
  schedule: string
  enabled: boolean
  draft?: boolean
  tags: string[] | null
  node_count: number
  edge_count: number
  sla_deadline?: string
  sla_timezone?: string
  depends_on: string[] | null
  webhook_token?: string
  pipeline_id?: string
  source?: string
  created_at: string
  updated_at: string
  last_run_status: string
  last_run_at?: string
  last_run_error?: string
  /** Counted over the newest 200 runs only, not all time. */
  runs_total: number
  runs_success: number
  runs_failed: number
  runs_running: number
  /** Newest first, at most 5; success is normalised to "succeeded". */
  run_history: string[] | null
}

export interface SchedulerEntry {
  pipeline_id: string
  pipeline_name: string
  schedule: string
  next_run: string
  last_run?: string
}

export interface PipelineTemplate {
  id: string
  name: string
  description: string
  icon: string
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  created_at?: string
  updated_at?: string
}

export interface PipelineVersion {
  version: number
  message: string
  created_at: string
}

export interface NodeIssue {
  node_id: string
  node_name: string
  errors: string[] | null
  warnings: string[] | null
}

export interface DryRunNodeResult {
  node_id: string
  name: string
  status: 'success' | 'skipped' | (string & {})
  columns: string[] | null
  rows: Record<string, unknown>[] | null
  error?: string
}

export interface DryRunResponse {
  results?: Record<string, DryRunNodeResult>
  /** Present (with HTTP 200) when the dry run failed part-way. */
  error?: string
}

export type SchedulePreview =
  | { valid: true; cron: string; description: string; timezone: string; next: string[] }
  | { valid: false; error: string; suggestion?: string }

export interface DeleteConflict {
  error: string
  dependents: { id: string; name: string }[]
  hint?: string
}

export type RunStatus =
  | 'pending'
  | 'running'
  | 'success'
  | 'failed'
  | 'cancelled'
  | 'blocked'
  | 'waiting'
  | 'skipped'
  | (string & {})

export interface NodeRun {
  id: string
  run_id: string
  node_id: string
  status: RunStatus
  row_count: number
  started_at: string | null
  duration_ms: number
  error?: string
  /** 0 is the first try. */
  attempt?: number
  ready_at?: string | null
  queue_ms?: number
  rows_per_sec?: number
  trace_id?: string
  span_id?: string
}

/** The closed set of things that start a run (core brokoli#617). */
export type RunTriggerKind = 'user' | 'schedule' | 'webhook' | 'dependency' | 'backfill' | 'api_token' | 'retry' | (string & {})

/** Who or what started a run, recorded when the run is triggered. */
export interface RunAttribution {
  kind: RunTriggerKind
  user_id?: string
  user_name?: string
  /** Names an API token or work-pool credential, never the secret itself. */
  token_name?: string
}

export interface Run {
  id: string
  pipeline_id: string
  status: RunStatus
  error?: string
  params?: Record<string, string>
  parameters?: Record<string, unknown>
  started_at: string | null
  finished_at: string | null
  trace_id?: string
  /** Null in list responses; only GET /runs/{id} fills it. */
  node_runs: NodeRun[] | null
  pipeline_version?: number
  trigger?: string
  /** Who started the run (core brokoli#617); absent on older runs and deployments. */
  triggered_by?: RunAttribution
  data_interval_start?: string
  data_interval_end?: string
  resumed_from_run_id?: string
  cancel_requested?: boolean
  org_id?: string
}

export interface RunTriggerResult {
  id: string
  pipeline_id: string
  status: RunStatus
}

export interface RunPage {
  items: Run[]
  has_next: boolean
  cursor?: string
  limit: number
}

export interface LogEntry {
  run_id: string
  node_id: string
  level: 'debug' | 'info' | 'warning' | 'error' | (string & {})
  message: string
  timestamp: string
  trace_id?: string
  span_id?: string
  attempt?: number
  metadata?: Record<string, string>
}

export interface RunEvent {
  id: number | string
  run_id: string
  node_id?: string
  attempt?: number
  event_type: string
  payload: Record<string, unknown> | null
  created_at: string
  schema_version?: number
}

export interface PhysicalInstance {
  logical_node_id: string
  kind: string
  instance_key: string
  index: number
  status: RunStatus
  row_count: number
  started_at?: string | null
  duration_ms: number
  error?: string
  attempt: number
}

export interface PhysicalWorkUnit {
  logical_node_id: string
  node_type: string
  kind: string
  instance_key_template: string
  static_instance_count: number
  runtime_resolved: boolean
  retry_scope: string
  concurrency_group?: string
  max_concurrency?: number
  explain: string
}

export interface PhysicalPlan {
  pipeline_id: string
  ir_version?: string
  stages: { index: number; work_units: PhysicalWorkUnit[] }[]
  static_instance_count: number
  dynamic_nodes: number
}

export interface BackfillPlan {
  pipeline_id: string
  intervals: number
  first_interval_start: string
  last_interval_end: string
  concurrency: number
  note: string
}

export interface NodePreview {
  columns: string[] | null
  rows: Record<string, unknown>[] | null
  /** True when the stored sample is not the full output. */
  truncated?: boolean
  /** Full output size when known; omitted/null when only the cap was hit. */
  total_rows?: number | null
}

export interface ColumnProfile {
  name: string
  type: string
  null_count: number
  null_pct: number
  unique_count: number
  unique_pct: number
  min_val?: string
  max_val?: string
  mean_val?: number
  is_numeric: boolean
  sample_values?: string[]
}

export interface DriftAlert {
  column: string
  type: 'column_added' | 'column_removed' | 'type_changed' | 'null_spike' | (string & {})
  previous: string
  current: string
  severity: string
}

export interface NodeProfile {
  profile: { row_count: number; column_count: number; columns: ColumnProfile[] | null; profiling_ms: number } | null
  schema?: unknown
  drift: DriftAlert[] | null
}

export interface PipelineGrid {
  nodes: { id: string; name: string; type: string }[]
  runs: {
    id: string
    status: string
    started_at: string | null
    trigger?: string
    data_interval_start?: string
    data_interval_end?: string
    pipeline_version: number
  }[]
  cells: Record<string, Record<string, { status: string; attempt: number; duration_ms: number; row_count: number; error?: string }>>
}

export interface Connection {
  id?: string
  conn_id: string
  type: string
  description?: string
  host?: string
  /** Omitted by the server when 0 (use the type's default). */
  port?: number
  schema?: string
  login?: string
  /** Never returned by the server; write-only. */
  password?: string
  /** Never returned by the server; write-only JSON string. */
  extra?: string
  /** "encrypted://********" when stored by Brokoli; env://, vault:// or k8s:// refs come back verbatim. */
  password_ref?: string
  extra_ref?: string
  /** 0 means unlimited. */
  max_concurrent?: number
  created_at?: string
  updated_at?: string
}

export interface ConnectionTypeMeta {
  type: string
  label: string
  category: 'database' | 'storage' | 'api' | 'other' | (string & {})
  icon?: string
  description?: string
  fields: string[]
  hints?: Record<string, string>
}

export interface User {
  id: string
  username: string
  display_name?: string
  email?: string
  role: string
  created_at: string
}

export interface SystemInfo {
  version: string
  active_runs: number
  max_concurrent_runs: number
}

export interface NotificationSettings {
  webhook_configured: boolean
  webhook_masked: string
  channel: string
  username: string
  teams_configured: boolean
  teams_webhook_masked: string
}

export type VariableType = 'string' | 'number' | 'json' | 'secret'

export interface Variable {
  key: string
  /** "********" for secrets; the server never returns a secret's value. */
  value: string
  type: VariableType | (string & {})
  description?: string
  workspace_id?: string
  created_at?: string
  updated_at?: string
}

export interface PluginNodeType {
  type: string
  kind: 'source' | 'sink' | 'transform' | (string & {})
  display_name?: string
}

export interface Plugin {
  name: string
  version: string
  description?: string
  node_types: PluginNodeType[] | null
  packaged: boolean
  archive_sha256?: string
  payloads?: { runtime: string; os?: string; arch?: string; path: string; entrypoint: string; sha256: string }[]
}

export interface PluginIndex {
  version: number
  plugins: { name: string; version: string; description?: string; archive_url: string; sha256: string }[] | null
}

/** Install responses: the plugin, or the plugin plus a warning when the server could not finish the install. */
export type PluginInstallResult = { plugin: Plugin; warning?: string }

export interface ConnectionUsage {
  pipeline_id: string
  pipeline_name: string
  node_id: string
  node_name: string
}

export interface ConnectionTestResult {
  success: boolean
  message?: string
  driver?: string
  error?: string
}

/*
 * GET /api/dashboard. Every counter is computed over the newest 200 runs of
 * each pipeline, so a very busy pipeline is undercounted. Days in `trends`
 * and `runs_today` use the server's local clock.
 */
export interface DashboardRun {
  pipeline_id: string
  pipeline_name: string
  run_id: string
  status: RunStatus
  error?: string
  started_at?: string
  finished_at?: string
}

export interface DashboardStats {
  runs_today: number
  runs_yesterday: number
  runs_24h_total: number
  runs_24h_success: number
  runs_24h_failed: number
  /** Runs that finished (succeeded or failed) in the window; the denominator of success_rate_24h. */
  runs_24h_finished: number
  /** Truncated percentage of finished runs that succeeded; null when nothing finished (core brokoli#606). */
  success_rate_24h: number | null
  runs_running: number
  running_run_ids: string[] | null
  /** Newest runs across the organisation, any age; recent_runs_sample_size says how many. */
  recent_runs: DashboardRun[] | null
  recent_runs_sample_size: number
  trends: { date: string; success: number; failed: number; total: number }[] | null
  /** Failed runs per pipeline in the last top_failing_window_hours hours (core brokoli#610). */
  top_failing: { pipeline_id: string; name: string; fail_count: number }[] | null
  top_failing_window_hours: number
  /** Only pipelines with at least one run in the last 24 hours. */
  pipeline_rollups:
    | {
        pipeline_id: string
        name: string
        total: number
        success: number
        failed: number
        running: number
        last_status?: string
        last_started_at?: string
      }[]
    | null
}

/** One day of GET /api/runs/calendar. Every day in the window is returned, oldest first; `date` is a UTC day (core brokoli#613). */
export interface CalendarDay {
  date: string
  total: number
  success: number
  failed: number
  running: number
}

export interface DeadLetter {
  id: string
  pipeline_id: string
  pipeline_name?: string
  run_id: string
  error: string
  node_id: string
  node_name: string
  payload: string
  created_at: string
  resolved: boolean
  resolved_at?: string
}

export interface Alert {
  id: string
  org_id: string
  kind: string
  severity: 'info' | 'warning' | 'critical' | (string & {})
  title: string
  body?: string
  pipeline_id?: string
  pipeline_name?: string
  run_id?: string
  created_at: string
  read_at?: string | null
  dismissed_at?: string | null
  /* Incident ownership (brokoli-ee#242): who owns the failure, and whether it is being handled. Ids, not names. */
  assignee_user_id?: string
  acknowledged_at?: string | null
  acknowledged_by?: string
  resolved_at?: string | null
  resolved_by?: string
}

export interface AlertList {
  alerts: Alert[] | null
  /** Unread across the whole organisation, which can exceed the rows returned. */
  unread_count: number
}

export type LineageNodeType = 'file' | 'table' | 'api' | 'processing' | (string & {})

export interface LineageColumn {
  name: string
  type: string
  null_pct: number
  unique_pct: number
  min_val?: string
  max_val?: string
}

export interface LineageNode {
  /** file:<path>, api:<url>, table:<name> or proc:<pipeline>:<node>. */
  id: string
  type: LineageNodeType
  name: string
  /** Processing nodes only: the pipeline node type. */
  sub_type?: string
  pipeline_id?: string
  pipeline?: string
  metadata?: {
    namespace?: string
    dataset?: string
    row_count?: number
    column_count?: number
    columns?: LineageColumn[] | null
    observed_at?: string
  }
  /** This step cannot say which output column came from which input, so no column edges are drawn through it (core ADR-039). */
  columns_opaque?: boolean
  /** Why the step is opaque, in terms a reader can act on. */
  opaque_reason?: string
}

export interface LineageEdge {
  from: string
  to: string
  pipeline_id: string
  pipeline: string
}

/** How a column edge was established (core ADR-039). "declared" and "attested" are facts; "inferred" is a name-match guess. */
export type EvidenceLevel = 'declared' | 'attested' | 'parsed' | 'inferred' | (string & {})

export interface ColumnEdge {
  from: string
  from_column: string
  to: string
  to_column: string
  /** Replaced the constant 0.7 confidence (core ADR-039). */
  evidence: EvidenceLevel
  /** The derivation in the pipeline's terms: "renamed from qty", "price * qty", "join key: id". */
  mapping_reason: string
}

export interface LineageGraph {
  nodes: LineageNode[] | null
  edges: LineageEdge[] | null
  /** Absent when there are no column mappings. */
  column_edges?: ColumnEdge[] | null
}

export interface DependencyGraph {
  nodes: { id: string; name: string }[] | null
  /** from is the upstream pipeline, to the one that depends on it. */
  edges: { from: string; to: string; state: DependencyState; mode: DependencyMode }[] | null
  /** True when the server stopped at its 2000-pipeline cap. */
  truncated?: boolean
}

/** GET /api/pipelines/{id}/deps: whether each upstream rule is satisfied right now. */
export interface DependencyStatus {
  satisfied: boolean
  reason: string
  deps:
    | {
        pipeline_id: string
        state: DependencyState
        mode: DependencyMode
        satisfied: boolean
        reason: string
        missing: boolean
        name?: string
        last_status?: string
        last_run_at?: string
      }[]
    | null
}

/** GET /api/pipelines/{id}/node-stats: durations of successful node runs over recent runs. */
export interface NodeStats {
  nodes: Record<string, { durations: number[] | null; avg: number; p95: number }> | null
}

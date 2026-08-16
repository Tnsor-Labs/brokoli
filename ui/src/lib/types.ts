export interface Pipeline {
  ir_version?: string;
  id: string;
  name: string;
  description: string;
  nodes: Node[];
  edges: Edge[];
  schedule: string;
  webhook_url?: string;
  params?: Record<string, string>;
  tags?: string[];
  hooks?: Record<string, Hook>;
  schedule_timezone?: string;
  sla_deadline?: string;
  sla_timezone?: string;
  depends_on?: string[];
  dependency_rules?: DependencyRule[];
  webhook_token?: string;
  node_count?: number;
  edge_count?: number;
  last_run_status?: string;
  last_run_at?: string;
  runs_total?: number;
  runs_success?: number;
  runs_failed?: number;
  runs_running?: number;
  run_history?: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export type DependencyState = "succeeded" | "completed" | "failed";
export type DependencyMode = "gate" | "trigger";

export interface DependencyRule {
  pipeline_id: string;
  state?: DependencyState;
  within_seconds?: number;
  mode?: DependencyMode;
}

export interface DependencyStatus {
  pipeline_id: string;
  name?: string;
  state?: DependencyState;
  mode?: DependencyMode;
  satisfied: boolean;
  reason?: string;
  missing?: boolean;
  last_status?: RunStatus;
  last_run_at?: string;
}

export interface DependencyGraph {
  nodes: { id: string; name: string }[];
  edges: { from: string; to: string; state: DependencyState; mode: DependencyMode }[];
}

export interface Hook {
  type: string;
  url: string;
  enabled: boolean;
  extra?: Record<string, string>;
}

export interface PipelineVersion {
  version: number;
  message: string;
  created_at: string;
}

export interface PhysicalWorkUnit {
  logical_node_id: string;
  node_type: string;
  kind: string;
  instance_key_template: string;
  static_instance_count: number;
  runtime_resolved: boolean;
  retry_scope: string;
  concurrency_group?: string;
  max_concurrency?: number;
  explain: string;
}

export interface PhysicalStage {
  index: number;
  work_units: PhysicalWorkUnit[];
}

export interface PhysicalPlan {
  pipeline_id: string;
  ir_version?: string;
  stages: PhysicalStage[];
  static_instance_count: number;
  dynamic_nodes: number;
}

export type NodeType =
  | "source_file"
  | "source_api"
  | "source_db"
  | "code"
  | "join"
  | "transform"
  | "quality_check"
  | "sql_generate"
  | "sink_file"
  | "sink_db"
  | "sink_api"
  | "migrate"
  | "condition"
  | "dbt"
  | "notify";

export interface Position {
  x: number;
  y: number;
}

export interface Node {
  id: string;
  type: NodeType;
  name: string;
  config: Record<string, unknown>;
  position: Position;
  capabilities?: string[];
}

export interface Edge {
  from: string;
  to: string;
  condition?: boolean;
}

// Matches models.Alert (Go) — served by GET /api/alerts. The persisted,
// readable counterpart to the product's outbound-only notifications.
export interface Alert {
  id: string;
  org_id: string;
  kind: string;
  severity: "info" | "warning" | "critical";
  title: string;
  body?: string;
  pipeline_id?: string;
  pipeline_name?: string;
  run_id?: string;
  created_at: string;
  read_at?: string | null;
  dismissed_at?: string | null;
}

export interface AlertsResponse {
  alerts: Alert[];
  unread_count: number;
}

// Matches store.DLQEntry (Go). pipeline_name is only populated by the
// org-wide GET /api/dlq, which joins through pipelines; the per-pipeline
// endpoint leaves it empty because the caller already knows the pipeline.
export interface DLQEntry {
  id: string;
  pipeline_id: string;
  pipeline_name?: string;
  run_id: string;
  error: string;
  node_id: string;
  node_name: string;
  payload: string;
  created_at: string;
  resolved: boolean;
  resolved_at?: string;
}

// Matches store.CalendarDay (Go) — served by GET /api/runs/calendar?days=N.
export interface CalendarDay {
  date: string; // YYYY-MM-DD
  total: number;
  success: number;
  failed: number;
  running: number;
}

// Matches models.RunEvent (Go) — served by GET /api/runs/{id}/events.
export interface RunEvent {
  id?: number;
  run_id: string;
  node_id?: string;
  attempt?: number;
  event_type: string;
  payload?: Record<string, unknown>;
  created_at: string;
}

// Per-pipeline 24h rollup from GET /api/dashboard. Counts are DB-backed,
// so they reflect every run in the window, not the recent_runs sample.
export interface PipelineRollup {
  pipeline_id: string;
  name: string;
  total: number;
  success: number;
  failed: number;
  running: number;
  last_status?: string;
  last_started_at?: string;
}

// Matches pkg/templates.Template (Go) — served by GET /api/templates.
export interface PipelineTemplate {
  id: string;
  name: string;
  description: string;
  icon: string;
  nodes: Node[];
  edges: Edge[];
}

export type RunStatus =
  | "pending"
  | "running"
  | "success"
  | "failed"
  | "cancelled"
  | "blocked"
  | "skipped";

export interface Run {
  id: string;
  pipeline_id: string;
  status: RunStatus;
  error?: string;
  started_at: string | null;
  finished_at: string | null;
  trace_id?: string;
  node_runs: NodeRun[];
}

export interface PhysicalInstance {
  logical_node_id: string;
  kind: "single" | "expansion" | string;
  instance_key: string;
  index: number;
  status: RunStatus;
  row_count: number;
  started_at?: string | null;
  duration_ms: number;
  error?: string;
  attempt: number;
}

export interface NodeRun {
  id: string;
  run_id: string;
  node_id: string;
  status: RunStatus;
  row_count: number;
  started_at: string | null;
  duration_ms: number;
  error?: string;
  attempt?: number;
  ready_at?: string | null;
  queue_ms?: number;
  rows_per_sec?: number;
  trace_id?: string;
  span_id?: string;
}

export interface LogEntry {
  run_id: string;
  node_id: string;
  level: "debug" | "info" | "warning" | "error";
  message: string;
  timestamp: string;
  trace_id?: string;
  span_id?: string;
  attempt?: number;
  metadata?: Record<string, string>;
}

export interface NodeStatEntry {
  durations: number[];
  avg: number;
  p95: number;
}

export type NodeStats = Record<string, NodeStatEntry>;

export type EventType =
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "node.started"
  | "node.completed"
  | "node.failed"
  | "log";

export interface WSEvent {
  type: EventType;
  run_id: string;
  pipeline_id?: string;
  node_id?: string;
  status?: RunStatus;
  row_count?: number;
  duration_ms?: number;
  error?: string;
  level?: string;
  message?: string;
  timestamp: string;
}

// ── Plugins (brokoli#110) ──────────────────────────────────────────
export interface PluginNodeType {
  type: string;
  kind: string;
  display_name?: string;
}

export interface Plugin {
  name: string;
  version: string;
  description?: string;
  node_types: PluginNodeType[];
  packaged: boolean;
  archive_sha256?: string;
}

export interface PluginIndexEntry {
  name: string;
  version: string;
  description?: string;
  archive_url: string;
  sha256: string;
}

export interface PluginIndex {
  version: number;
  plugins: PluginIndexEntry[];
}

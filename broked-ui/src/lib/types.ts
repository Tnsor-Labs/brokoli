export interface Pipeline {
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
  webhook_token?: string;
  node_count?: number;
  edge_count?: number;
  last_run_status?: string;
  last_run_at?: string;
  runs_total?: number;
  runs_success?: number;
  runs_failed?: number;
  enabled: boolean;
  created_at: string;
  updated_at: string;
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
  | "condition";

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
}

export interface Edge {
  from: string;
  to: string;
}

export type RunStatus =
  | "pending"
  | "running"
  | "success"
  | "failed"
  | "cancelled";

export interface Run {
  id: string;
  pipeline_id: string;
  status: RunStatus;
  error?: string;
  started_at: string | null;
  finished_at: string | null;
  node_runs: NodeRun[];
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
}

export interface LogEntry {
  run_id: string;
  node_id: string;
  level: "debug" | "info" | "warning" | "error";
  message: string;
  timestamp: string;
}

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

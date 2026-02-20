export interface Pipeline {
  id: string;
  name: string;
  description: string;
  nodes: Node[];
  edges: Edge[];
  schedule: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
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
  | "sink_db";

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

import type { Pipeline, Run, LogEntry } from "./types";
import { authHeaders, logout } from "./auth";

const BASE = "/api";

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...authHeaders(),
    ...(options?.headers as Record<string, string> || {}),
  };

  const res = await fetch(`${BASE}${path}`, {
    ...options,
    headers,
  });

  // Auto-logout on 401
  if (res.status === 401 && !path.startsWith("/auth/")) {
    logout();
    throw new Error("Session expired");
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

export const api = {
  pipelines: {
    list: () => request<Pipeline[]>("/pipelines"),
    get: (id: string) => request<Pipeline>(`/pipelines/${id}`),
    create: (data: Partial<Pipeline>) =>
      request<Pipeline>("/pipelines", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    update: (id: string, data: Partial<Pipeline>) =>
      request<Pipeline>(`/pipelines/${id}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    delete: (id: string) =>
      request<void>(`/pipelines/${id}`, { method: "DELETE" }),
  },
  runs: {
    trigger: (pipelineId: string, params?: Record<string, string>) =>
      request<Run>(`/pipelines/${pipelineId}/run`, {
        method: "POST",
        body: JSON.stringify({ params }),
      }),
    listByPipeline: (pipelineId: string) =>
      request<Run[]>(`/pipelines/${pipelineId}/runs`),
    get: (id: string) => request<Run>(`/runs/${id}`),
    getLogs: (id: string) => request<LogEntry[]>(`/runs/${id}/logs`),
    backfill: (pipelineId: string, startDate: string, endDate: string) =>
      request<{ runs: string[]; count: number }>(`/pipelines/${pipelineId}/backfill`, {
        method: "POST",
        body: JSON.stringify({ start_date: startDate, end_date: endDate }),
      }),
  },
};

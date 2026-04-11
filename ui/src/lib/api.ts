import type {
  Pipeline,
  PipelineVersion,
  Run,
  LogEntry,
  DependencyStatus,
  DependencyGraph,
} from "./types";
import { authHeaders, logout } from "./auth";

const BASE = "/api";

function getWorkspaceId(): string {
  return localStorage.getItem("brokoli-workspace") || "default";
}

interface RequestOptions extends RequestInit {
  timeout?: number;
  maxRetries?: number;
}

async function request<T>(path: string, options?: RequestOptions): Promise<T> {
  const {
    timeout = 15000,
    maxRetries = 2,
    ...fetchOpts
  } = options || {};

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "X-Workspace-ID": getWorkspaceId(),
    ...authHeaders(),
    ...(fetchOpts.headers as Record<string, string> || {}),
  };

  let lastErr: Error | null = null;

  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    try {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), timeout);

      const res = await fetch(`${BASE}${path}`, {
        ...fetchOpts,
        headers,
        signal: controller.signal,
      });

      clearTimeout(timeoutId);

      // Auto-logout on 401
      if (res.status === 401 && !path.startsWith("/auth/")) {
        logout();
        throw new Error("Session expired");
      }

      // Retry on 5xx (server error) — not on 4xx (client error)
      if (res.status >= 500 && attempt < maxRetries) {
        await new Promise(r => setTimeout(r, 1000 * Math.pow(2, attempt)));
        continue;
      }

      if (!res.ok) {
        let errMsg = `HTTP ${res.status}`;
        let errBody: any = null;
        try {
          errBody = await res.json();
          errMsg = errBody.error || errMsg;
        } catch {}
        const err: any = new Error(errMsg);
        err.status = res.status;
        err.body = errBody;
        throw err;
      }

      if (res.status === 204) return undefined as T;
      return res.json();
    } catch (err: any) {
      lastErr = err;

      // Don't retry on auth errors or client errors
      if (err.message === "Session expired") throw err;

      // Retry on network errors and timeouts
      if (attempt < maxRetries && (err.name === "AbortError" || err instanceof TypeError)) {
        await new Promise(r => setTimeout(r, 1000 * Math.pow(2, attempt)));
        continue;
      }

      if (attempt === maxRetries) break;
    }
  }

  throw lastErr || new Error("Request failed");
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
    delete: (id: string, resolve?: "abort" | "cascade" | "decouple") => {
      const qs = resolve ? `?resolve=${resolve}` : "";
      return request<void>(`/pipelines/${id}${qs}`, { method: "DELETE" });
    },
    deps: (id: string) =>
      request<{ satisfied: boolean; reason?: string; deps: DependencyStatus[] }>(
        `/pipelines/${id}/deps`,
      ),
    dependents: (id: string) =>
      request<{ id: string; name: string }[]>(`/pipelines/${id}/dependents`),
    dependencyGraph: () =>
      request<DependencyGraph>(`/pipelines/dependency-graph`),
    versions: (id: string) =>
      request<PipelineVersion[]>(`/pipelines/${id}/versions`),
    rollback: (id: string, version: number) =>
      request<Pipeline>(`/pipelines/${id}/rollback`, {
        method: "POST",
        body: JSON.stringify({ version }),
      }),
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

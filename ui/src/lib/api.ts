import type {
  Pipeline,
  PipelineVersion,
  PipelineTemplate,
  Run,
  RunEvent,
  LogEntry,
  AlertsResponse,
  DLQEntry,
  CalendarDay,
  DependencyStatus,
  DependencyGraph,
  Plugin,
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
    // cancel/resume previously existed only as raw fetch calls inside
    // PipelineRuns.svelte, which is why nothing else in the app could offer
    // them. Any surface that shows a run can now act on it.
    cancel: (id: string) =>
      request<{ status: string }>(`/runs/${id}/cancel`, { method: "POST" }),
    resume: (id: string) =>
      request<Run>(`/runs/${id}/resume`, { method: "POST" }),
    events: (id: string) => request<RunEvent[]>(`/runs/${id}/events`),
  },
  alerts: {
    list: (opts?: { unread?: boolean; limit?: number }) => {
      const qs = new URLSearchParams();
      if (opts?.unread) qs.set("unread", "true");
      if (opts?.limit) qs.set("limit", String(opts.limit));
      const q = qs.toString();
      return request<AlertsResponse>(`/alerts${q ? `?${q}` : ""}`);
    },
    markRead: (id: string) => request<void>(`/alerts/${id}/read`, { method: "POST" }),
    markAllRead: () => request<void>("/alerts/read-all", { method: "POST" }),
    dismiss: (id: string) => request<void>(`/alerts/${id}`, { method: "DELETE" }),
  },
  // Dead letter queue across every pipeline in the org. The per-pipeline
  // endpoint has existed for a while; this org-wide one is what makes a
  // single "records needing intervention" surface possible.
  dlq: {
    list: (opts?: { includeResolved?: boolean; limit?: number }) => {
      const qs = new URLSearchParams();
      if (opts?.includeResolved) qs.set("include_resolved", "true");
      if (opts?.limit) qs.set("limit", String(opts.limit));
      const q = qs.toString();
      return request<DLQEntry[]>(`/dlq${q ? `?${q}` : ""}`);
    },
    resolve: (pipelineId: string, dlqId: string) =>
      request<void>(`/pipelines/${pipelineId}/dlq/${dlqId}/resolve`, { method: "POST" }),
  },
  templates: {
    list: () => request<PipelineTemplate[]>("/templates"),
  },
  plugins: {
    list: () => request<Plugin[]>("/plugins"),
    remove: (name: string) =>
      request<void>(`/plugins/${encodeURIComponent(name)}`, { method: "DELETE" }),
    // Install uploads a .bkg archive as the raw request body. The JSON
    // `request` helper can't carry a binary body, so this posts directly;
    // the server reads the raw body (or a multipart "file" field).
    install: async (file: File): Promise<Plugin> => {
      const res = await fetch(`${BASE}/plugins`, {
        method: "POST",
        headers: { ...authHeaders(), "X-Workspace-ID": getWorkspaceId() },
        body: file,
      });
      if (res.status === 401) {
        logout();
        throw new Error("Session expired");
      }
      if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try {
          const body = await res.json();
          msg = body.error || msg;
        } catch {}
        throw new Error(msg);
      }
      return res.json();
    },
  },
  runsCalendar: (days = 90) => request<CalendarDay[]>(`/runs/calendar?days=${days}`),
};

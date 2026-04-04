/**
 * Brokoli icon system — clean line icons, uniform 24x24 viewBox.
 * No fills, no backgrounds. Stroke-only, 1.5px weight.
 * Purpose-built for a data orchestration platform.
 */

export interface IconDef {
  d: string;
}

export const icons: Record<string, IconDef> = {
  // ── Node types (data orchestration specific) ───────────────────

  // File source: document with small arrow out from center
  file: {
    d: `M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zM14 2v6h6M9 15h3m0 0h3m-3 0v-3m0 3v3`,
  },

  // API: two-way connection endpoint
  api: {
    d: `M12 2v4m0 12v4M2 12h4m12 0h4M6.34 6.34l2.83 2.83m5.66 5.66l2.83 2.83M17.66 6.34l-2.83 2.83m-5.66 5.66l-2.83 2.83M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z`,
  },

  // Database: cylinder with horizontal lines
  database: {
    d: `M21 5c0 1.66-4.03 3-9 3S3 6.66 3 5m18 0c0-1.66-4.03-3-9-3S3 3.34 3 5m18 0v14c0 1.66-4.03 3-9 3s-9-1.34-9-3V5m18 7c0 1.66-4.03 3-9 3s-9-1.34-9-3`,
  },

  // Transform: horizontal bars with offset dots (mixing/adjusting)
  transform: {
    d: `M3 6h7m4 0h7M3 12h3m4 0h11M3 18h11m4 0h3M12 4v4M8 10v4M16 16v4`,
  },

  // Quality check: shield with checkmark
  check: {
    d: `M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10zM9 12l2 2 4-4`,
  },

  // File output: document with arrow pointing down into it
  output: {
    d: `M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zM14 2v6h6M12 12v6m0 0l-3-3m3 3l3-3`,
  },

  // Code: terminal prompt
  code: {
    d: `M4 17l6-5-6-5M12 19h8M4 3h16a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z`,
  },

  // Join/merge: two paths converging
  merge: {
    d: `M6 4v16M18 4v16M6 8h12M6 16h12`,
  },

  // Sink (DB output): database with arrow going in
  sink: {
    d: `M21 5c0 1.66-4.03 3-9 3S3 6.66 3 5m18 0c0-1.66-4.03-3-9-3S3 3.34 3 5m18 0v14c0 1.66-4.03 3-9 3s-9-1.34-9-3V5m12 1l-3 3-3-3`,
  },

  // SQL generate: database with angle brackets
  sql: {
    d: `M21 5c0 1.66-4.03 3-9 3S3 6.66 3 5m18 0c0-1.66-4.03-3-9-3S3 3.34 3 5m18 0v14c0 1.66-4.03 3-9 3s-9-1.34-9-3V5m5 8l-2 2 2 2m6-4l2 2-2 2`,
  },

  // ── Navigation ─────────────────────────────────────────────────

  // Dashboard: grid of 4 cards
  dashboard: {
    d: `M3 3h8v10H3zM13 3h8v6h-8zM13 13h8v8h-8zM3 17h8v4H3z`,
  },

  // Pipelines: stacked horizontal lines with dots
  pipeline: {
    d: `M4 6h16M4 12h16M4 18h16M7 6v0M7 12v0M7 18v0`,
  },

  // Lineage: branching tree
  lineage: {
    d: `M12 3v6m0 0l-6 4m6-4l6 4M6 13v4m12-4v4M6 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM18 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM12 11a2 2 0 1 0 0-4 2 2 0 0 0 0 4z`,
  },

  // Settings: gear
  settings: {
    d: `M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z`,
  },

  // ── Actions ────────────────────────────────────────────────────

  play: {
    d: `M5 3l14 9-14 9V3z`,
  },
  plus: {
    d: `M12 5v14m-7-7h14`,
  },
  trash: {
    d: `M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6`,
  },
  chevronRight: {
    d: `M9 5l7 7-7 7`,
  },
  chevronDown: {
    d: `M6 9l6 6 6-6`,
  },
  arrowLeft: {
    d: `M19 12H5m7-7l-7 7 7 7`,
  },
  layout: {
    d: `M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z`,
  },
  download: {
    d: `M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3`,
  },
  refresh: {
    d: `M1 4v6h6M23 20v-6h-6M20.49 9A9 9 0 0 0 5.64 5.64L1 10m22 4l-4.64 4.36A9 9 0 0 1 3.51 15`,
  },
  clock: {
    d: `M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20zM12 6v6l4 2`,
  },
  search: {
    d: `M11 3a8 8 0 1 0 0 16 8 8 0 0 0 0-16zM21 21l-4.35-4.35`,
  },

  // ── Workspaces
  workspace: {
    d: `M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M9 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75`,
  },

  // ── Enterprise features
  audit: {
    d: `M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6zM14 2v6h6M16 13H8M16 17H8M10 9H8`,
  },
  gitbranch: {
    d: `M6 3v12M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 9a9 9 0 0 1-9 9`,
  },

  // ── New node types
  sinkapi: {
    d: `M12 2v6m0 0l-3-3m3 3l3-3M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8M7 12l5-5 5 5`,
  },
  migrate: {
    d: `M5 12h14m0 0l-4-4m4 4l-4 4M21 5c0 1.66-4.03 3-9 3S3 6.66 3 5m18 0c0-1.66-4.03-3-9-3S3 3.34 3 5`,
  },

  // ── Calendar ───────────────────────────────────────────────────
  calendar: {
    d: `M3 5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5zM16 2v4M8 2v4M3 10h18M8 14h.01M12 14h.01M16 14h.01M8 18h.01M12 18h.01`,
  },

  // ── Variables ──────────────────────────────────────────────────
  variable: {
    d: `M4 7V4h16v3M9 20h6M12 4v16`,
  },

  // ── Connections ─────────────────────────────────────────────────
  connection: {
    d: `M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71`,
  },

  // ── User & auth ────────────────────────────────────────────────

  user: {
    d: `M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z`,
  },
  logout: {
    d: `M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9`,
  },
  upload: {
    d: `M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12`,
  },
  history: {
    d: `M12 8v4l3 3M3.05 11a9 9 0 1 1 .5 4m-.5 5v-5h5`,
  },
  bell: {
    d: `M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9M13.73 21a2 2 0 0 1-3.46 0`,
  },
  shield: {
    d: `M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z`,
  },
  condition: {
    d: `M12 3l9 5v8l-9 5-9-5V8l9-5zM12 8v8M8 10l4 2 4-2`,
  },

  // Organization: building
  building: {
    d: `M3 21h18M5 21V7l7-4 7 4v14M9 21v-4h6v4M9 9h.01M15 9h.01M9 13h.01M15 13h.01`,
  },
  helpCircle: {
    d: `M12 22c5.523 0 10-4.477 10-10S17.523 2 12 2 2 6.477 2 12s4.477 10 10 10zM9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3M12 17h.01`,
  },
};

/** Get the icon key for a node type */
export function nodeTypeIcon(type: string): string {
  const map: Record<string, string> = {
    source_file: "file",
    source_api: "api",
    source_db: "database",
    code: "code",
    join: "merge",
    transform: "transform",
    quality_check: "check",
    sql_generate: "sql",
    sink_file: "output",
    sink_db: "sink",
    sink_api: "sinkapi",
    migrate: "migrate",
    condition: "condition",
  };
  return map[type] || "file";
}

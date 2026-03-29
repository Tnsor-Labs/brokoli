<script lang="ts">
  export let runId: string;
  export let nodeId: string;
  export let nodeName: string = "";

  let columns: string[] = [];
  let rows: Record<string, unknown>[] = [];
  let loading = true;
  let error = "";

  // Re-load whenever runId or nodeId changes
  $: if (runId && nodeId) {
    load(runId, nodeId);
  }

  async function load(rid: string, nid: string) {
    loading = true;
    error = "";
    try {
      const url = `/api/runs/${rid}/nodes/${nid}/preview`;
      const token = localStorage.getItem("broked-token");
      const headers: Record<string, string> = {};
      if (token) headers["Authorization"] = `Bearer ${token}`;
      const res = await fetch(url, { headers });
      if (!res.ok) throw new Error("No preview available");
      const data = await res.json();
      columns = data.columns || [];
      rows = data.rows || [];
    } catch (e: any) {
      error = e.message || "Failed to load preview";
    } finally {
      loading = false;
    }
  }

  function formatCell(value: unknown): string {
    if (value === null || value === undefined) return "";
    const s = String(value);
    if (s.length > 120) return s.slice(0, 120) + "...";
    return s;
  }
</script>

<div class="preview">
  <div class="preview-header">
    <span class="preview-title">Data Preview{nodeName ? ` — ${nodeName}` : ""}</span>
    <span class="preview-meta">
      {#if !loading && !error}
        {rows.length} row{rows.length !== 1 ? "s" : ""} / {columns.length} column{columns.length !== 1 ? "s" : ""}
      {/if}
    </span>
  </div>

  {#if loading}
    <div class="preview-empty">Loading...</div>
  {:else if error}
    <div class="preview-empty">{error}</div>
  {:else if rows.length === 0}
    <div class="preview-empty">No data</div>
  {:else}
    <div class="table-scroll">
      <table>
        <thead>
          <tr>
            <th class="row-num">#</th>
            {#each columns as col}
              <th>{col}</th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each rows as row, i}
            <tr>
              <td class="row-num">{i + 1}</td>
              {#each columns as col}
                <td class:null-val={row[col] === null || row[col] === undefined || row[col] === ""}>
                  {formatCell(row[col])}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .preview {
    background: var(--bg-code);
    border: 1px solid var(--border-sidebar);
    border-radius: 8px;
    overflow: hidden;
  }

  .preview-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-sidebar);
    background: var(--bg-sidebar);
  }
  .preview-title {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .preview-meta {
    font-family: 'JetBrains Mono', monospace;
    font-size: 10.5px;
    color: var(--text-dim);
  }

  .preview-empty {
    padding: 24px;
    text-align: center;
    color: var(--text-dim);
    font-size: 13px;
  }

  .table-scroll {
    overflow-x: auto;
    max-height: 320px;
    overflow-y: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    font-family: 'JetBrains Mono', monospace;
  }

  thead {
    position: sticky;
    top: 0;
    z-index: 1;
  }

  th {
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-weight: 600;
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 7px 12px;
    text-align: left;
    white-space: nowrap;
    border-bottom: 1px solid var(--border-subtle);
  }

  td {
    padding: 5px 12px;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-sidebar);
    white-space: nowrap;
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  tr:hover td {
    background: var(--bg-card-hover);
  }

  .row-num {
    color: var(--text-ghost);
    width: 40px;
    text-align: right;
    padding-right: 8px;
  }

  .null-val {
    color: var(--text-ghost);
    font-style: italic;
  }
  .null-val::after {
    content: "null";
  }
</style>

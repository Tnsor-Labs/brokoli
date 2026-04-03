<script lang="ts">
  export let runId: string;
  export let nodeId: string;
  export let nodeName: string = "";

  let columns: string[] = [];
  let rows: Record<string, unknown>[] = [];
  let loading = true;
  let error = "";

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
  {#if !loading && !error && rows.length > 0}
    <div class="preview-summary">
      <span class="summary-stat">{rows.length} rows</span>
      <span class="summary-stat">{columns.length} columns</span>
    </div>
  {/if}

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
            <th class="col-num">#</th>
            {#each columns as col}
              <th>{col}</th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each rows as row, i}
            <tr>
              <td class="col-num">{i + 1}</td>
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
    overflow: hidden;
  }

  .preview-summary {
    display: flex;
    gap: 16px;
    padding: 10px 0;
    border-bottom: 1px solid var(--border-subtle);
    margin-bottom: 4px;
  }
  .summary-stat {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }

  .preview-empty {
    padding: 24px;
    text-align: center;
    color: var(--text-dim);
    font-size: 12px;
  }

  .table-scroll {
    overflow-x: auto;
    max-height: 360px;
    overflow-y: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11px;
    font-family: var(--font-mono);
  }

  th {
    text-align: left;
    padding: 5px 8px;
    font-size: 9px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-ghost);
    border-bottom: 1px solid var(--border-subtle);
    white-space: nowrap;
    position: sticky;
    top: 0;
    background: var(--bg-primary);
    z-index: 1;
  }

  td {
    padding: 4px 8px;
    color: var(--text-secondary);
    border-bottom: 1px solid var(--border-subtle);
    white-space: nowrap;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  tr:hover td {
    background: var(--bg-tertiary);
  }

  .col-num {
    color: var(--text-ghost);
    width: 32px;
    text-align: right;
    padding-right: 6px;
    font-size: 10px;
  }

  td.null-val {
    color: var(--text-ghost);
    font-style: italic;
  }
  td.null-val:empty::after {
    content: "null";
  }
</style>

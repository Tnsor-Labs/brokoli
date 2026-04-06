<script lang="ts">
  import type { Node, NodeType } from "../lib/types";
  import { nodeTypeConfig } from "../lib/dag";
  import { icons, nodeTypeIcon } from "../lib/icons";
  import TransformRuleEditor from "./TransformRuleEditor.svelte";
  import CodeEditorModal from "./CodeEditorModal.svelte";
  import { createEventDispatcher } from "svelte";
  import { notify } from "../lib/toast";
  import Stepper from "./Stepper.svelte";
  import { authHeaders } from "../lib/auth";

  export let node: Node | null = null;

  const dispatch = createEventDispatcher();

  let testingConnection = false;
  let codeEditorVisible = false;

  async function testConnection() {
    if (!node) return;
    const uri = node.config["uri"] as string;
    if (!uri) { notify.warning("Enter a URI first"); return; }
    testingConnection = true;
    try {
      const res = await fetch("/api/test-connection", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ uri }),
      });
      const data = await res.json();
      if (data.success) {
        notify.success(`Connected (${data.driver})`);
      } else {
        notify.error(`Connection failed: ${data.error}`);
      }
    } catch {
      notify.error("Connection test failed");
    } finally {
      testingConnection = false;
    }
  }

  function updateConfig(key: string, value: unknown) {
    if (!node) return;
    node.config = { ...node.config, [key]: value };
    dispatch("update", node);
  }

  function updateName(name: string) {
    if (!node) return;
    node.name = name;
    dispatch("update", node);
  }

  function deleteNode() {
    if (!node) return;
    dispatch("delete", node.id);
  }

  // Quality check rules
  function getQualityRules(): any[] {
    if (!node) return [];
    return (node.config["rules"] as any[]) || [];
  }
  function addQualityRule() {
    const rules = getQualityRules();
    rules.push({ column: "", rule: "not_null", params: {}, on_failure: "block" });
    updateConfig("rules", [...rules]);
  }
  function removeQualityRule(index: number) {
    const rules = getQualityRules();
    rules.splice(index, 1);
    updateConfig("rules", [...rules]);
  }
  function updateQualityRule(index: number, key: string, value: any) {
    const rules = getQualityRules();
    rules[index] = { ...rules[index], [key]: value };
    updateConfig("rules", [...rules]);
  }
  function updateQualityRuleParam(index: number, paramKey: string, value: any) {
    const rules = getQualityRules();
    rules[index] = { ...rules[index], params: { ...rules[index].params, [paramKey]: value } };
    updateConfig("rules", [...rules]);
  }

  // API headers
  function getHeaders(): Record<string, string> {
    if (!node) return {};
    const h = node.config["headers"];
    if (typeof h === "object" && h !== null) return h as Record<string, string>;
    return {};
  }
  function updateHeader(key: string, value: string) {
    const headers = { ...getHeaders(), [key]: value };
    updateConfig("headers", headers);
  }
  function addHeader() {
    updateHeader("", "");
  }

  $: typeConfig = node ? nodeTypeConfig[node.type] : null;
  $: iconDef = node ? icons[nodeTypeIcon(node.type)] : null;

  // Load connections for conn_id selector
  interface ConnOption { conn_id: string; type: string; description: string; }
  let availableConnections: ConnOption[] = [];
  import { onMount } from "svelte";
  onMount(async () => {
    try {
      const res = await fetch("/api/connections", { headers: authHeaders() });
      if (res.ok) {
        availableConnections = await res.json();
      }
    } catch { /* ignore */ }
  });

  function connTypeFilter(nodeType: string): string[] {
    switch (nodeType) {
      case "source_db": case "sink_db": return ["postgres", "mysql", "sqlite", "snowflake", "redshift", "bigquery", "databricks", "oracle", "mssql", "generic"];
      case "source_api": return ["http", "generic"];
      default: return [];
    }
  }

  $: filteredConns = node ? availableConnections.filter(c => connTypeFilter(node!.type).includes(c.type)) : [];
  $: usingConnection = node?.config["conn_id"] ? true : false;

  const qualityRuleTypes = [
    { value: "not_null", label: "Not Null" },
    { value: "unique", label: "Unique" },
    { value: "min", label: "Min Value" },
    { value: "max", label: "Max Value" },
    { value: "range", label: "Range" },
    { value: "regex", label: "Regex Match" },
    { value: "row_count", label: "Row Count" },
  ];

  const rulesWithParams: Record<string, string[]> = {
    min: ["min"],
    max: ["max"],
    range: ["min", "max"],
    regex: ["pattern"],
    row_count: ["min", "max"],
  };

  const nodeDescriptions: Record<string, string> = {
    source_file: "Read data from CSV, JSON, or TSV files on disk.",
    source_api: "Fetch data from an HTTP/REST API endpoint.",
    source_db: "Query data from a database using SQL.",
    transform: "Apply transformations: filter, rename, sort, aggregate, and more.",
    code: "Run custom Python code to transform data.",
    join: "Combine two datasets by matching columns.",
    quality_check: "Validate data against rules before proceeding.",
    sql_generate: "Generate and execute SQL statements.",
    sink_file: "Write output data to a file.",
    sink_db: "Insert data into a database table.",
    sink_api: "Send data to an external API endpoint.",
    migrate: "Copy data between two databases.",
    condition: "Branch the pipeline based on a condition.",
    dbt: "Run dbt models to transform data in your warehouse. Requires dbt-core installed on the worker.",
    notify: "Send notifications when the pipeline reaches this point.",
  };

  let showAdvanced = false;
</script>

<div class="config-panel">
  {#if node}
    <div class="panel-header">
      <div class="panel-icon" style="--node-color: {typeConfig?.color || '#71717a'}">
        {#if iconDef}
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
            <path
              d={iconDef.d}
              stroke={typeConfig?.color || '#71717a'}
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        {/if}
      </div>
      <div class="panel-header-text">
        <span class="panel-title">{typeConfig?.label || node.type}</span>
        {#if nodeDescriptions[node.type]}
          <span class="panel-desc">{nodeDescriptions[node.type]}</span>
        {/if}
      </div>
    </div>

    <div class="field">
      <label for="node-name">Name</label>
      <input
        id="node-name"
        value={node.name}
        on:input={(e) => updateName(e.currentTarget.value)}
        placeholder={typeConfig?.label}
      />
    </div>

    <!-- ── Source File ── -->
    {#if node.type === "source_file"}
      <div class="field">
        <label>File Path</label>
        <input value={node.config["path"] || ""} on:input={(e) => updateConfig("path", e.currentTarget.value)} placeholder="/data/input.csv" />
      </div>
      <div class="field">
        <label>Format</label>
        <select value={node.config["format"] || "auto"} on:change={(e) => updateConfig("format", e.currentTarget.value)}>
          <option value="auto">Auto-detect</option>
          <option value="csv">CSV</option>
          <option value="json">JSON</option>
          <option value="tsv">TSV</option>
        </select>
      </div>
    {/if}

    <!-- ── Source API ── -->
    {#if node.type === "source_api"}
      {#if filteredConns.length > 0}
        <div class="field">
          <label>Connection</label>
          <select value={node.config["conn_id"] || ""} on:change={(e) => {
            const val = e.currentTarget.value;
            if (val) { updateConfig("conn_id", val); }
            else { updateConfig("conn_id", ""); }
          }}>
            <option value="">Manual URL</option>
            {#each filteredConns as c}
              <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
            {/each}
          </select>
        </div>
        {#if node.config["conn_id"]}
          <div class="field">
            <div class="conn-badge">Base URL from connection: <strong>{node.config["conn_id"]}</strong></div>
          </div>
        {/if}
      {/if}
      <div class="field">
        <label>URL</label>
        <input value={node.config["url"] || ""} on:input={(e) => updateConfig("url", e.currentTarget.value)} placeholder="https://api.example.com/data" />
      </div>
      <div class="field">
        <label>Method</label>
        <select value={node.config["method"] || "GET"} on:change={(e) => updateConfig("method", e.currentTarget.value)}>
          <option value="GET">GET</option>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="DELETE">DELETE</option>
        </select>
      </div>
      <div class="field">
        <label>Response Path (JSON path to data array)</label>
        <input value={node.config["response_path"] || ""} on:input={(e) => updateConfig("response_path", e.currentTarget.value)} placeholder="data.items" />
      </div>
      {#if node.config["method"] === "POST" || node.config["method"] === "PUT"}
        <div class="field">
          <label>Request Body (JSON)</label>
          <textarea class="code-input" rows="3" value={node.config["body"] || ""} on:input={(e) => updateConfig("body", e.currentTarget.value)} placeholder="JSON body"></textarea>
        </div>
      {/if}
      <div class="field-group">
        <span class="group-title">Headers</span>
        {#each Object.entries(getHeaders()) as [hKey, hVal], i}
          <div class="header-row">
            <input class="header-key" value={hKey} placeholder="Header name" on:input={(e) => {
              const h = getHeaders();
              delete h[hKey];
              h[e.currentTarget.value] = hVal;
              updateConfig("headers", h);
            }} />
            <input class="header-val" value={hVal} placeholder="Value" on:input={(e) => {
              updateHeader(hKey, e.currentTarget.value);
            }} />
          </div>
        {/each}
        <button class="btn-add-sm" on:click={addHeader}>+ Add Header</button>
      </div>
    {/if}

    <!-- ── Source DB ── -->
    {#if node.type === "source_db"}
      <div class="field">
        <label>Connection</label>
        <select value={node.config["conn_id"] || ""} on:change={(e) => {
          const val = e.currentTarget.value;
          if (val) { updateConfig("conn_id", val); updateConfig("uri", ""); }
          else { updateConfig("conn_id", ""); }
        }}>
          <option value="">Manual URI</option>
          {#each filteredConns as c}
            <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
          {/each}
          {#if node.config["conn_id"] && !filteredConns.find(c => c.conn_id === node.config["conn_id"])}
            <option value={node.config["conn_id"]}>{node.config["conn_id"]} (not found)</option>
          {/if}
        </select>
      </div>
      {#if !usingConnection}
        <div class="field">
          <label>Connection URI</label>
          <input value={node.config["uri"] || ""} on:input={(e) => updateConfig("uri", e.currentTarget.value)} placeholder="postgres://user:pass@host/db" />
        </div>
      {:else}
        <div class="field">
          <div class="conn-badge">Using connection: <strong>{node.config["conn_id"]}</strong></div>
        </div>
      {/if}
      <div class="field">
        <label>SQL Query</label>
        <textarea class="code-input" rows="4" value={node.config["query"] || ""} on:input={(e) => updateConfig("query", e.currentTarget.value)} placeholder="SELECT * FROM users WHERE active = true"></textarea>
      </div>
      <div class="field" style="padding-top: 0">
        <button class="btn-test-conn" on:click={testConnection} disabled={testingConnection}>
          {testingConnection ? "Testing..." : "Test Connection"}
        </button>
      </div>
    {/if}

    <!-- ── Transform ── -->
    {#if node.type === "transform"}
      <div class="field-group">
        <span class="group-title">Transform Rules</span>
        <TransformRuleEditor
          rules={node.config["rules"] || []}
          on:change={(e) => updateConfig("rules", e.detail)}
        />
      </div>
    {/if}

    <!-- ── Code (Python) ── -->
    {#if node.type === "code"}
      <div class="field">
        <label>Python Path</label>
        <input value={node.config["python_path"] || ""} on:input={(e) => updateConfig("python_path", e.currentTarget.value)} placeholder="python3 (default, or /path/to/venv/bin/python)" />
      </div>
      <div class="field">
        <label>Timeout (seconds)</label>
        <Stepper value={node.config["timeout"] || 30} min={1} max={600} step={5} on:change={(e) => updateConfig("timeout", e.detail)} />
      </div>
      <div class="field-group">
        <span class="group-title">Python Script</span>
        <button class="btn-open-editor" on:click={() => codeEditorVisible = true}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.code.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Open Full Editor
        </button>
        {#if node.config["script"]}
          <pre class="code-preview">{(node.config["script"] as string).split("\n").slice(0, 6).join("\n")}{(node.config["script"] as string).split("\n").length > 6 ? "\n..." : ""}</pre>
        {:else}
          <div class="code-empty">No script defined yet</div>
        {/if}
      </div>
      <CodeEditorModal
        script={node.config["script"] as string || ""}
        bind:visible={codeEditorVisible}
        on:save={(e) => updateConfig("script", e.detail)}
      />
    {/if}

    <!-- ── Join ── -->
    {#if node.type === "join"}
      <div class="field">
        <label>Join Type</label>
        <select value={node.config["join_type"] || "inner"} on:change={(e) => updateConfig("join_type", e.currentTarget.value)}>
          <option value="inner">Inner Join</option>
          <option value="left">Left Join</option>
          <option value="right">Right Join</option>
          <option value="full">Full Outer Join</option>
        </select>
      </div>
      <div class="field">
        <label>Left Key Column</label>
        <input value={node.config["left_key"] || ""} on:input={(e) => updateConfig("left_key", e.currentTarget.value)} placeholder="customer_id" />
      </div>
      <div class="field">
        <label>Right Key Column</label>
        <input value={node.config["right_key"] || ""} on:input={(e) => updateConfig("right_key", e.currentTarget.value)} placeholder="id" />
      </div>
    {/if}

    <!-- ── Quality Check ── -->
    {#if node.type === "quality_check"}
      <div class="field-group">
        <span class="group-title">Quality Rules</span>
        {#each getQualityRules() as rule, i}
          <div class="quality-rule">
            <div class="qr-header">
              <span class="qr-num">#{i + 1}</span>
              <button class="btn-remove" on:click={() => removeQualityRule(i)} title="Remove rule">&times;</button>
            </div>
            <div class="qr-fields">
              <div class="qr-field">
                <label>Column</label>
                <input value={rule.column || ""} on:input={(e) => updateQualityRule(i, "column", e.currentTarget.value)} placeholder="column_name" />
              </div>
              <div class="qr-field">
                <label>Check</label>
                <select value={rule.rule || "not_null"} on:change={(e) => updateQualityRule(i, "rule", e.currentTarget.value)}>
                  {#each qualityRuleTypes as rt}
                    <option value={rt.value}>{rt.label}</option>
                  {/each}
                </select>
              </div>
              <div class="qr-field">
                <label>On Failure</label>
                <select value={rule.on_failure || "block"} on:change={(e) => updateQualityRule(i, "on_failure", e.currentTarget.value)}>
                  <option value="block">Block (fail pipeline)</option>
                  <option value="warn">Warn (continue)</option>
                </select>
              </div>
              {#if rulesWithParams[rule.rule]}
                {#each rulesWithParams[rule.rule] as paramKey}
                  <div class="qr-field">
                    <label>{paramKey}</label>
                    <input value={rule.params?.[paramKey] || ""} on:input={(e) => updateQualityRuleParam(i, paramKey, e.currentTarget.value)} placeholder={paramKey} />
                  </div>
                {/each}
              {/if}
            </div>
          </div>
        {/each}
        <button class="btn-add-sm" on:click={addQualityRule}>+ Add Quality Rule</button>
      </div>
    {/if}

    <!-- ── SQL Generate ── -->
    {#if node.type === "sql_generate"}
      <div class="field">
        <label>Table Name</label>
        <input value={node.config["table"] || ""} on:input={(e) => updateConfig("table", e.currentTarget.value)} placeholder="my_table" />
      </div>
      <div class="field">
        <label>Dialect</label>
        <select value={node.config["dialect"] || "postgres"} on:change={(e) => updateConfig("dialect", e.currentTarget.value)}>
          <option value="postgres">PostgreSQL</option>
          <option value="mysql">MySQL</option>
          <option value="sqlite">SQLite</option>
          <option value="sqlserver">SQL Server</option>
          <option value="generic">Generic</option>
        </select>
      </div>
      <div class="field">
        <label>Batch Size</label>
        <Stepper value={node.config["batch_size"] || 100} min={1} max={10000} step={50} on:change={(e) => updateConfig("batch_size", e.detail)} />
      </div>
      <div class="field">
        <label class="toggle">
          <input type="checkbox" checked={!!node.config["create_table"]} on:change={(e) => updateConfig("create_table", e.currentTarget.checked)} />
          <span class="toggle-label">Create Table (CREATE TABLE IF NOT EXISTS)</span>
        </label>
      </div>
    {/if}

    <!-- ── Sink File ── -->
    {#if node.type === "sink_file"}
      <div class="field">
        <label>Output Path</label>
        <input value={node.config["path"] || ""} on:input={(e) => updateConfig("path", e.currentTarget.value)} placeholder="/output/result.csv" />
      </div>
      <div class="field">
        <label>Format</label>
        <select value={node.config["format"] || "csv"} on:change={(e) => updateConfig("format", e.currentTarget.value)}>
          <option value="csv">CSV</option>
          <option value="json">JSON</option>
          <option value="sql">SQL</option>
        </select>
      </div>
    {/if}

    <!-- ── Sink DB ── -->
    {#if node.type === "sink_db"}
      <div class="field">
        <label>Connection</label>
        <select value={node.config["conn_id"] || ""} on:change={(e) => {
          const val = e.currentTarget.value;
          if (val) { updateConfig("conn_id", val); updateConfig("uri", ""); }
          else { updateConfig("conn_id", ""); }
        }}>
          <option value="">Manual URI</option>
          {#each filteredConns as c}
            <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
          {/each}
          {#if node.config["conn_id"] && !filteredConns.find(c => c.conn_id === node.config["conn_id"])}
            <option value={node.config["conn_id"]}>{node.config["conn_id"]} (not found)</option>
          {/if}
        </select>
      </div>
      {#if !node.config["conn_id"]}
        <div class="field">
          <label>Connection URI</label>
          <input value={node.config["uri"] || ""} on:input={(e) => updateConfig("uri", e.currentTarget.value)} placeholder="postgres://user:pass@host/db" />
        </div>
      {:else}
        <div class="field">
          <div class="conn-badge">Using connection: <strong>{node.config["conn_id"]}</strong></div>
        </div>
      {/if}
      <div class="field" style="padding-top: 0">
        <button class="btn-test-conn" on:click={testConnection} disabled={testingConnection}>
          {testingConnection ? "Testing..." : "Test Connection"}
        </button>
      </div>
    {/if}

    <!-- ── Sink API ── -->
    {#if node.type === "sink_api"}
      {#if filteredConns.length > 0}
        <div class="field">
          <label>Connection</label>
          <select value={node.config["conn_id"] || ""} on:change={(e) => {
            const val = e.currentTarget.value;
            if (val) { updateConfig("conn_id", val); }
            else { updateConfig("conn_id", ""); }
          }}>
            <option value="">Manual URL</option>
            {#each filteredConns as c}
              <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
            {/each}
          </select>
        </div>
      {/if}
      <div class="field">
        <label>URL</label>
        <input value={node.config["url"] || ""} on:input={(e) => updateConfig("url", e.currentTarget.value)} placeholder="https://api.example.com/ingest" />
      </div>
      <div class="field">
        <label>Method</label>
        <select value={node.config["method"] || "POST"} on:change={(e) => updateConfig("method", e.currentTarget.value)}>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="PATCH">PATCH</option>
        </select>
      </div>
      <div class="field">
        <label>Batch Size</label>
        <Stepper value={node.config["batch_size"] || 100} min={1} max={10000} step={50} on:change={(e) => updateConfig("batch_size", e.detail)} />
      </div>
    {/if}

    <!-- ── DB Migration ── -->
    {#if node.type === "migrate"}
      <div class="field">
        <label>Source Connection</label>
        <select value={node.config["source_conn_id"] || ""} on:change={(e) => {
          const val = e.currentTarget.value;
          if (val) { updateConfig("source_conn_id", val); updateConfig("source_uri", ""); }
          else { updateConfig("source_conn_id", ""); }
        }}>
          <option value="">Manual URI</option>
          {#each availableConnections.filter(c => ["postgres","mysql","sqlite","generic"].includes(c.type)) as c}
            <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
          {/each}
        </select>
      </div>
      {#if !node.config["source_conn_id"]}
        <div class="field">
          <label>Source URI</label>
          <input value={node.config["source_uri"] || ""} on:input={(e) => updateConfig("source_uri", e.currentTarget.value)} placeholder="postgres://user:pass@host/source_db" />
        </div>
      {:else}
        <div class="field"><div class="conn-badge">Source: <strong>{node.config["source_conn_id"]}</strong></div></div>
      {/if}
      <div class="field">
        <label>Source Query</label>
        <textarea class="code-input" rows="3" value={node.config["source_query"] || ""} on:input={(e) => updateConfig("source_query", e.currentTarget.value)} placeholder="SELECT * FROM users"></textarea>
      </div>
      <div class="field">
        <label>Destination Connection</label>
        <select value={node.config["dest_conn_id"] || ""} on:change={(e) => {
          const val = e.currentTarget.value;
          if (val) { updateConfig("dest_conn_id", val); updateConfig("dest_uri", ""); }
          else { updateConfig("dest_conn_id", ""); }
        }}>
          <option value="">Manual URI</option>
          {#each availableConnections.filter(c => ["postgres","mysql","sqlite","generic"].includes(c.type)) as c}
            <option value={c.conn_id}>{c.conn_id} ({c.type})</option>
          {/each}
        </select>
      </div>
      {#if !node.config["dest_conn_id"]}
        <div class="field">
          <label>Destination URI</label>
          <input value={node.config["dest_uri"] || ""} on:input={(e) => updateConfig("dest_uri", e.currentTarget.value)} placeholder="postgres://user:pass@host/dest_db" />
        </div>
      {:else}
        <div class="field"><div class="conn-badge">Destination: <strong>{node.config["dest_conn_id"]}</strong></div></div>
      {/if}
      <div class="field">
        <label>Destination Table</label>
        <input value={node.config["dest_table"] || ""} on:input={(e) => updateConfig("dest_table", e.currentTarget.value)} placeholder="users_migrated" />
      </div>
      <div class="field">
        <label>Dialect</label>
        <select value={node.config["dialect"] || "postgres"} on:change={(e) => updateConfig("dialect", e.currentTarget.value)}>
          <option value="postgres">PostgreSQL</option>
          <option value="mysql">MySQL</option>
          <option value="sqlite">SQLite</option>
          <option value="generic">Generic</option>
        </select>
      </div>
      <div class="field">
        <label>Chunk Size</label>
        <Stepper value={node.config["chunk_size"] || 5000} min={100} max={100000} step={500} on:change={(e) => updateConfig("chunk_size", e.detail)} />
      </div>
      <div class="field">
        <label class="toggle">
          <input type="checkbox" checked={!!node.config["create_table"]} on:change={(e) => updateConfig("create_table", e.currentTarget.checked)} />
          <span class="toggle-label">Create Table</span>
        </label>
      </div>
    {/if}

    <!-- ── Condition (If/Else) ── -->
    {#if node.type === "condition"}
      <div class="field">
        <label>Condition Expression</label>
        <input
          value={node.config["expression"] || ""}
          on:input={(e) => updateConfig("expression", e.currentTarget.value)}
          placeholder='row_count > 0'
        />
      </div>
      <div class="field-hint">
        Expressions: <code>row_count &gt; N</code>, <code>column_exists("name")</code>,
        <code>null_pct("col") &lt; 10</code>, <code>min("col") &gt; 0</code>
      </div>
      <div class="field">
        <label>On True → continue downstream</label>
        <label>On False → skip downstream nodes</label>
      </div>
    {/if}

    <!-- ── dbt ── -->
    {#if node.type === "dbt"}
      <div class="field-group">
        <span class="group-title">What to run</span>
        <div class="field">
          <label>Command</label>
          <div class="select-cards">
            {#each [
              { val: "run", label: "Run", desc: "Execute SQL models" },
              { val: "test", label: "Test", desc: "Validate data" },
              { val: "build", label: "Build", desc: "Run + Test" },
              { val: "seed", label: "Seed", desc: "Load CSVs" },
            ] as cmd}
              <button class="select-card" class:active={( node.config["command"] || "run") === cmd.val}
                on:click={() => updateConfig("command", cmd.val)}>
                <strong>{cmd.label}</strong>
                <span>{cmd.desc}</span>
              </button>
            {/each}
          </div>
        </div>
        <div class="field">
          <label>Select Models</label>
          <input value={node.config["select"] || ""} on:input={(e) => updateConfig("select", e.currentTarget.value)} placeholder="model_name, +tag:daily, path:marts/*" />
          <span class="field-hint">Leave blank to run all models. Use dbt selection syntax.</span>
        </div>
      </div>

      <div class="field-group">
        <span class="group-title">Project</span>
        <div class="field">
          <label>Project Directory</label>
          <input value={node.config["project_dir"] || ""} on:input={(e) => updateConfig("project_dir", e.currentTarget.value)} placeholder="/path/to/dbt/project" />
          <span class="field-hint">Path to your dbt project root (where dbt_project.yml lives).</span>
        </div>
        <div class="field">
          <label>Target Environment</label>
          <input value={node.config["target"] || ""} on:input={(e) => updateConfig("target", e.currentTarget.value)} placeholder="dev" />
          <span class="field-hint">Which profile target to use (dev, staging, prod). Leave blank for default.</span>
        </div>
      </div>

      <button class="toggle-advanced" on:click={() => showAdvanced = !showAdvanced}>
        {showAdvanced ? "Hide" : "Show"} advanced options
      </button>
      {#if showAdvanced}
        <div class="field-group">
          <span class="group-title">Advanced</span>
          <div class="field">
            <label>Profiles Directory</label>
            <input value={node.config["profiles_dir"] || ""} on:input={(e) => updateConfig("profiles_dir", e.currentTarget.value)} placeholder="~/.dbt" />
          </div>
          <div class="field">
            <label>Variables</label>
            <textarea rows="2" class="code-input" value={node.config["vars"] || ""} on:input={(e) => updateConfig("vars", e.currentTarget.value)} placeholder="date: 2024-01-01"></textarea>
            <span class="field-hint">YAML format. Passed as --vars to dbt.</span>
          </div>
        </div>
      {/if}
    {/if}

    <!-- ── Notify ── -->
    {#if node.type === "notify"}
      <div class="field-group">
        <span class="group-title">Destination</span>
        <div class="field">
          <label>Send via</label>
          <div class="select-cards">
            {#each [
              { val: "slack", label: "Slack", desc: "Post to a channel" },
              { val: "webhook", label: "Webhook", desc: "HTTP POST to any URL" },
            ] as opt}
              <button class="select-card" class:active={(node.config["notify_type"] || "webhook") === opt.val}
                on:click={() => updateConfig("notify_type", opt.val)}>
                <strong>{opt.label}</strong>
                <span>{opt.desc}</span>
              </button>
            {/each}
          </div>
        </div>
        <div class="field">
          <label>{(node.config["notify_type"] || "webhook") === "slack" ? "Slack Webhook URL" : "Webhook URL"}</label>
          <input value={node.config["webhook_url"] || ""} on:input={(e) => updateConfig("webhook_url", e.currentTarget.value)}
            placeholder={(node.config["notify_type"] || "webhook") === "slack" ? "https://hooks.slack.com/services/T.../B.../..." : "https://api.example.com/webhook"} />
        </div>
        {#if (node.config["notify_type"] || "webhook") === "slack"}
          <div class="field">
            <label>Channel</label>
            <input value={node.config["channel"] || ""} on:input={(e) => updateConfig("channel", e.currentTarget.value)} placeholder="#data-alerts" />
            <span class="field-hint">Optional. Override the default channel set in Slack.</span>
          </div>
        {/if}
      </div>
      <div class="field-group">
        <span class="group-title">Message</span>
        <div class="field">
          <label>Template</label>
          <textarea rows="3" value={node.config["message"] || ""} on:input={(e) => updateConfig("message", e.currentTarget.value)}
            placeholder="Pipeline completed successfully with {{rows}} rows processed."></textarea>
          <span class="field-hint">
            Available variables: <code>{"{{pipeline}}"}</code> <code>{"{{run_id}}"}</code> <code>{"{{rows}}"}</code>
          </span>
        </div>
      </div>
    {/if}

    <!-- ── Common: Retry Config ── -->
    <div class="field-group">
      <span class="group-title">Retry</span>
      <div class="field-row">
        <div class="field compact">
          <label>Retries</label>
          <Stepper value={node.config["max_retries"] || 0} min={0} max={10} on:change={(e) => updateConfig("max_retries", e.detail)} />
        </div>
        <div class="field compact">
          <label>Delay (ms)</label>
          <Stepper value={node.config["retry_delay"] || 1000} min={0} max={60000} step={500} on:change={(e) => updateConfig("retry_delay", e.detail)} />
        </div>
      </div>
    </div>

    <div class="panel-footer">
      <div class="footer-actions">
        <button class="btn-duplicate" on:click={() => dispatch("duplicate", node.id)}>Duplicate (D)</button>
        <button class="btn-danger" on:click={deleteNode}>Delete</button>
      </div>
    </div>
  {:else}
    <div class="empty-panel">
      <p>Select a node to configure</p>
    </div>
  {/if}
</div>

<style>
  .config-panel {
    height: 100%;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }

  .panel-header {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: var(--space-md) var(--space-lg);
    border-bottom: 1px solid var(--border);
    background: var(--bg-tertiary);
  }
  .panel-icon {
    width: 28px; height: 28px;
    display: flex; align-items: center; justify-content: center;
    border-radius: 6px;
    background: color-mix(in srgb, var(--node-color) 10%, transparent);
    flex-shrink: 0;
  }
  .panel-header-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .panel-title { font-weight: 600; font-size: 0.875rem; }
  .panel-desc { font-size: 11px; color: var(--text-dim); line-height: 1.4; }

  /* Select cards — visual option picker */
  .select-cards { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
  .select-card {
    display: flex; flex-direction: column; gap: 2px;
    padding: 8px 10px; border: 1px solid var(--border);
    border-radius: 8px; background: none; cursor: pointer;
    text-align: left; color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .select-card:hover { border-color: var(--border-hover); }
  .select-card.active {
    border-color: var(--accent); background: var(--accent-glow);
    color: var(--text-primary);
  }
  .select-card strong { font-size: 12px; font-weight: 600; }
  .select-card span { font-size: 10px; color: var(--text-dim); }
  .select-card.active span { color: var(--text-muted); }

  /* Advanced toggle */
  .toggle-advanced {
    display: block; width: 100%; padding: 8px;
    font-size: 11px; color: var(--text-dim); background: none;
    border: none; border-top: 1px solid var(--border-subtle);
    cursor: pointer; text-align: center;
    transition: color 150ms ease;
  }
  .toggle-advanced:hover { color: var(--accent); }

  .field { padding: var(--space-sm) var(--space-lg); }
  .field label {
    display: block;
    font-size: 0.6875rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-xs);
  }
  .field input, .field select, .field textarea {
    width: 100%;
  }
  .field select {
    padding: var(--space-sm) var(--space-md);
    background: var(--bg-input);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    font-family: var(--font-ui);
    font-size: 0.875rem;
  }

  .code-input {
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.5;
    background: var(--bg-code);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 8px 10px;
    resize: vertical;
    tab-size: 4;
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    cursor: pointer;
    font-size: 0.875rem;
  }
  .toggle input[type="checkbox"] {
    width: 16px; height: 16px;
    accent-color: var(--accent);
  }
  .toggle-label {
    font-size: 0.75rem;
    color: var(--text-secondary);
    text-transform: none;
    letter-spacing: 0;
  }

  .field-hint {
    padding: 0 var(--space-lg) var(--space-sm);
    font-size: 10px; color: var(--text-ghost); line-height: 1.6;
  }
  .field-hint code {
    font-family: var(--font-mono); font-size: 10px; color: var(--accent);
    background: var(--bg-tertiary); padding: 0 3px; border-radius: 2px;
  }
  .field-group {
    padding: var(--space-sm) var(--space-lg);
    border-top: 1px solid var(--border);
    margin-top: var(--space-sm);
  }
  .group-title {
    display: block;
    font-size: 0.625rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 600;
    margin-bottom: var(--space-sm);
  }
  .field-row { display: flex; gap: var(--space-sm); }
  .field.compact { padding: 0; flex: 1; }
  .field.compact input { width: 100%; }

  .btn-open-editor {
    display: flex; align-items: center; gap: 6px;
    width: 100%; padding: 8px 12px; border-radius: 6px;
    font-size: 12px; font-weight: 500;
    background: rgba(234, 179, 8, 0.06);
    border: 1px solid rgba(234, 179, 8, 0.2);
    color: #eab308; transition: all 150ms ease;
    margin-bottom: 8px;
  }
  .btn-open-editor:hover {
    background: rgba(234, 179, 8, 0.12);
    border-color: rgba(234, 179, 8, 0.4);
  }

  .code-preview {
    font-family: var(--font-mono); font-size: 10px; line-height: 1.5;
    color: var(--text-dim); background: var(--bg-code-line);
    border: 1px solid var(--border-sidebar); border-radius: 6px;
    padding: 8px 10px; margin: 0; overflow: hidden;
    white-space: pre; max-height: 100px;
  }
  .code-empty {
    font-size: 11px; color: var(--text-ghost); padding: 12px;
    text-align: center; background: var(--bg-code-line);
    border: 1px dashed var(--border-sidebar); border-radius: 6px;
  }

  .btn-test-conn {
    width: 100%; padding: 5px;
    border-radius: var(--radius-md);
    font-size: 0.75rem; font-weight: 500;
    background: rgba(6, 182, 212, 0.08);
    border: 1px solid rgba(6, 182, 212, 0.3);
    color: #06b6d4; transition: all 150ms ease;
  }
  .btn-test-conn:hover { background: rgba(6, 182, 212, 0.15); }

  /* Quality rule editor */
  .quality-rule {
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    padding: 8px;
    margin-bottom: 8px;
  }
  .qr-header {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: 6px;
  }
  .qr-num {
    font-family: var(--font-mono); font-size: 10px; color: var(--text-dim); font-weight: 600;
  }
  .btn-remove {
    width: 20px; height: 20px; display: flex; align-items: center; justify-content: center;
    border-radius: 4px; font-size: 14px; color: var(--text-dim);
    transition: all 150ms ease;
  }
  .btn-remove:hover { color: var(--failed); background: var(--failed-bg); }

  .qr-fields { display: flex; flex-direction: column; gap: 4px; }
  .qr-field label {
    font-size: 9px; color: var(--text-dim); text-transform: uppercase;
    letter-spacing: 0.06em; margin-bottom: 1px; display: block;
  }
  .qr-field input, .qr-field select {
    width: 100%; font-size: 12px; padding: 4px 8px;
  }

  /* Header editor */
  .header-row {
    display: flex; gap: 4px; margin-bottom: 4px;
  }
  .header-key, .header-val {
    flex: 1; font-size: 11px; padding: 4px 6px;
    font-family: var(--font-mono);
  }
  .header-key { max-width: 40%; }

  .btn-add-sm {
    display: block; width: 100%; padding: 6px;
    border-radius: 4px; font-size: 11px; font-weight: 500;
    background: var(--accent-glow); border: 1px dashed var(--border);
    color: var(--accent-text); transition: all 150ms ease;
    margin-top: 4px;
  }
  .btn-add-sm:hover { background: var(--accent-glow-strong); border-color: var(--accent); }

  .panel-footer {
    margin-top: auto;
    padding: var(--space-md) var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .footer-actions { display: flex; gap: 8px; }
  .btn-duplicate {
    flex: 1; padding: var(--space-sm);
    border-radius: var(--radius-md);
    background: var(--bg-tertiary); color: var(--text-secondary);
    font-weight: 500; font-size: 12px;
    transition: all 150ms ease;
  }
  .btn-duplicate:hover { background: var(--accent-glow); color: var(--accent); }
  .btn-danger {
    flex: 1; padding: var(--space-sm);
    border-radius: var(--radius-md);
    background: var(--failed-bg); color: var(--failed);
    font-weight: 500; transition: background var(--transition-fast);
  }
  .btn-danger:hover { background: rgba(239, 68, 68, 0.2); }

  .conn-badge {
    font-size: 11px; color: var(--accent-text);
    background: var(--accent-glow); padding: 6px 10px;
    border-radius: 4px; border: 1px solid rgba(99,102,241,0.2);
  }

  .empty-panel {
    display: flex; align-items: center; justify-content: center;
    height: 100%; color: var(--text-muted); font-size: 0.875rem;
  }
</style>

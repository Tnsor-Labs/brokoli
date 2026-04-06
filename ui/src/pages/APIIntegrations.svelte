<script lang="ts">
  import { authHeaders } from "../lib/auth";
  import { notify } from "../lib/toast";
  import Skeleton from "../components/Skeleton.svelte";

  let baseUrl = window.location.origin;
  let copied = "";

  function copyText(text: string, label: string) {
    navigator.clipboard.writeText(text).then(() => {
      copied = label;
      setTimeout(() => copied = "", 2000);
    });
  }

  const endpoints = [
    { method: "GET", path: "/api/pipelines", desc: "List all pipelines" },
    { method: "POST", path: "/api/pipelines", desc: "Create pipeline (JSON)" },
    { method: "POST", path: "/api/pipelines/import", desc: "Import pipeline (YAML/JSON)" },
    { method: "GET", path: "/api/pipelines/:id/export", desc: "Export pipeline as YAML" },
    { method: "POST", path: "/api/pipelines/:id/run", desc: "Trigger a pipeline run" },
    { method: "POST", path: "/api/pipelines/:id/webhook", desc: "Webhook trigger (token auth)" },
    { method: "GET", path: "/api/runs/:id", desc: "Get run status + node results" },
    { method: "POST", path: "/api/runs/:id/cancel", desc: "Cancel a running pipeline" },
    { method: "GET", path: "/api/connections", desc: "List connections" },
    { method: "GET", path: "/api/variables", desc: "List variables" },
    { method: "GET", path: "/api/dashboard", desc: "Dashboard stats + recent runs" },
    { method: "GET", path: "/api/lineage", desc: "Full data lineage graph" },
    { method: "GET", path: "/api/scheduler/status", desc: "Scheduled pipelines + next runs" },
  ];

  const codeExamples = {
    python: `import requests

API = "${baseUrl}/api"
TOKEN = "brk_your_token_here"
HEADERS = {"Authorization": f"Bearer {TOKEN}"}

# Trigger a pipeline run
resp = requests.post(f"{API}/pipelines/PIPELINE_ID/run", headers=HEADERS)
run = resp.json()
print(f"Run started: {run['id']}")

# Check run status
status = requests.get(f"{API}/runs/{run['id']}", headers=HEADERS)
print(status.json()["status"])`,
    curl: `# List pipelines
curl -s ${baseUrl}/api/pipelines \\
  -H "Authorization: Bearer brk_your_token_here" | jq

# Trigger a run
curl -s -X POST ${baseUrl}/api/pipelines/PIPELINE_ID/run \\
  -H "Authorization: Bearer brk_your_token_here" | jq

# Import a pipeline from YAML
curl -s -X POST ${baseUrl}/api/pipelines/import \\
  -H "Authorization: Bearer brk_your_token_here" \\
  -H "Content-Type: application/x-yaml" \\
  --data-binary @my-pipeline.yaml | jq`,
    javascript: `const API = "${baseUrl}/api";
const TOKEN = "brk_your_token_here";

const headers = { Authorization: \`Bearer \${TOKEN}\` };

// Trigger a pipeline run
const res = await fetch(\`\${API}/pipelines/PIPELINE_ID/run\`, {
  method: "POST", headers
});
const run = await res.json();
console.log("Run:", run.id, run.status);

// Poll for completion
const status = await fetch(\`\${API}/runs/\${run.id}\`, { headers });
console.log(await status.json());`,
    webhook: `# GitHub Actions example
- name: Trigger Brokoli Pipeline
  run: |
    curl -s -X POST ${baseUrl}/api/pipelines/PIPELINE_ID/webhook \\
      -H "X-Webhook-Token: YOUR_PIPELINE_WEBHOOK_TOKEN"

# Generic webhook (no auth header needed — uses pipeline token)
curl -X POST ${baseUrl}/api/pipelines/PIPELINE_ID/webhook \\
  -H "X-Webhook-Token: \$WEBHOOK_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"ref": "main", "trigger": "ci"}'`
  };

  let activeTab: "python" | "curl" | "javascript" | "webhook" = "curl";
</script>

<div class="page animate-in">
  <header class="page-header">
    <div>
      <h1>API & Integrations</h1>
      <span class="page-sub">Connect your tools, CI/CD, and scripts to Brokoli</span>
    </div>
  </header>

  <!-- Quick connect cards -->
  <div class="connect-grid">
    <div class="connect-card">
      <div class="cc-icon">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l6-5-6-5M12 19h8"/></svg>
      </div>
      <div class="cc-body">
        <h3>REST API</h3>
        <p>Full CRUD access to pipelines, runs, connections, and variables.</p>
      </div>
    </div>
    <div class="connect-card">
      <div class="cc-icon">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
      </div>
      <div class="cc-body">
        <h3>Webhooks</h3>
        <p>Trigger pipelines from GitHub Actions, GitLab CI, or any HTTP client.</p>
      </div>
    </div>
    <div class="connect-card">
      <div class="cc-icon">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/></svg>
      </div>
      <div class="cc-body">
        <h3>WebSocket</h3>
        <p>Real-time events for run status, logs, and pipeline changes.</p>
      </div>
    </div>
  </div>

  <!-- Auth section -->
  <section class="section">
    <h2 class="section-title">Authentication</h2>
    <div class="auth-card">
      <p class="auth-desc">Include your token in the <code>Authorization</code> header:</p>
      <div class="auth-example">
        <code>Authorization: Bearer brk_your_token_here</code>
        <button class="copy-btn" on:click={() => copyText('Authorization: Bearer brk_your_token_here', 'auth')}>
          {copied === "auth" ? "Copied!" : "Copy"}
        </button>
      </div>
      <p class="auth-hint">Generate tokens in <a href="#/workspaces">Workspaces</a> &rarr; Token settings, or <a href="#/settings">Settings</a> &rarr; API & CLI.</p>
    </div>
  </section>

  <!-- Code examples -->
  <section class="section">
    <h2 class="section-title">Quick Start</h2>
    <div class="tabs">
      <button class="tab" class:active={activeTab === "curl"} on:click={() => activeTab = "curl"}>cURL</button>
      <button class="tab" class:active={activeTab === "python"} on:click={() => activeTab = "python"}>Python</button>
      <button class="tab" class:active={activeTab === "javascript"} on:click={() => activeTab = "javascript"}>JavaScript</button>
      <button class="tab" class:active={activeTab === "webhook"} on:click={() => activeTab = "webhook"}>Webhooks / CI</button>
    </div>
    <div class="code-card">
      <div class="code-header">
        <span class="code-lang">{activeTab}</span>
        <button class="copy-btn" on:click={() => copyText(codeExamples[activeTab], 'code')}>
          {copied === "code" ? "Copied!" : "Copy"}
        </button>
      </div>
      <pre class="code-block">{codeExamples[activeTab]}</pre>
    </div>
  </section>

  <!-- Endpoints reference -->
  <section class="section">
    <h2 class="section-title">Endpoints</h2>
    <div class="endpoint-table">
      <div class="ep-header">
        <span class="ep-method-col">Method</span>
        <span class="ep-path-col">Path</span>
        <span class="ep-desc-col">Description</span>
      </div>
      {#each endpoints as ep}
        <div class="ep-row">
          <span class="ep-method" class:get={ep.method === "GET"} class:post={ep.method === "POST"}>{ep.method}</span>
          <code class="ep-path">{ep.path}</code>
          <span class="ep-desc">{ep.desc}</span>
        </div>
      {/each}
    </div>
  </section>

  <section class="section">
    <h2 class="section-title">WebSocket Events</h2>
    <div class="auth-card">
      <p class="auth-desc">Connect to <code>{baseUrl}/api/ws?token=YOUR_TOKEN</code> for real-time events:</p>
      <div class="ws-events">
        <span class="ws-event"><code>run.started</code> — Pipeline run begins</span>
        <span class="ws-event"><code>run.completed</code> — Run finished successfully</span>
        <span class="ws-event"><code>run.failed</code> — Run failed with error</span>
        <span class="ws-event"><code>node.status</code> — Individual node status change</span>
        <span class="ws-event"><code>pipeline.updated</code> — Pipeline definition changed</span>
      </div>
    </div>
  </section>
</div>

<style>
  .page-header { margin-bottom: var(--space-xl); }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .page-sub { font-size: 13px; color: var(--text-muted); margin-top: 2px; display: block; }

  .section { margin-bottom: var(--space-xl); }
  .section-title {
    font-size: 11px; font-weight: 600; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em; margin-bottom: 10px;
  }

  /* Connect cards */
  .connect-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; margin-bottom: var(--space-xl); }
  .connect-card {
    display: flex; gap: 14px; padding: 20px;
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); box-shadow: var(--shadow-card);
    transition: border-color 200ms ease;
  }
  .connect-card:hover { border-color: var(--border); }
  .cc-icon {
    width: 40px; height: 40px; flex-shrink: 0; border-radius: 10px;
    background: var(--accent-glow); color: var(--accent);
    display: flex; align-items: center; justify-content: center;
  }
  .cc-body h3 { font-size: 14px; font-weight: 600; margin-bottom: 4px; }
  .cc-body p { font-size: 12px; color: var(--text-muted); line-height: 1.5; }

  /* Auth */
  .auth-card {
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); padding: 20px; box-shadow: var(--shadow-card);
  }
  .auth-desc { font-size: 13px; color: var(--text-secondary); margin-bottom: 12px; }
  .auth-desc code {
    font-family: var(--font-mono); color: var(--accent); font-size: 12px; font-weight: 500;
  }
  .auth-example {
    display: flex; align-items: center; justify-content: space-between;
    background: var(--bg-primary); border: 1px solid var(--border);
    border-radius: 8px; padding: 10px 14px; margin-bottom: 10px;
  }
  .auth-example code { font-family: var(--font-mono); font-size: 12px; color: var(--text-primary); }
  .auth-hint { font-size: 12px; color: var(--text-dim); }
  .auth-hint a { color: var(--accent); }

  .copy-btn {
    font-size: 11px; font-weight: 500; color: var(--accent);
    padding: 4px 10px; border-radius: 5px; border: 1px solid var(--accent);
    background: none; cursor: pointer; transition: all 150ms ease;
  }
  .copy-btn:hover { background: var(--accent-glow); }

  /* Tabs */
  .tabs { display: flex; gap: 2px; margin-bottom: -1px; position: relative; z-index: 1; }
  .tab {
    padding: 8px 16px; font-size: 12px; font-weight: 500;
    color: var(--text-muted); border: 1px solid transparent;
    border-bottom: none; border-radius: 8px 8px 0 0;
    background: none; cursor: pointer; transition: all 150ms ease;
  }
  .tab:hover { color: var(--text-secondary); }
  .tab.active {
    color: var(--text-primary); background: var(--bg-secondary);
    border-color: var(--border-subtle);
  }

  /* Code block */
  .code-card {
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: 0 var(--radius-xl, 14px) var(--radius-xl, 14px) var(--radius-xl, 14px);
    overflow: hidden; box-shadow: var(--shadow-card);
  }
  .code-header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 8px 16px; border-bottom: 1px solid var(--border-subtle);
  }
  .code-lang { font-size: 10px; font-weight: 600; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.06em; }
  .code-block {
    font-family: var(--font-mono); font-size: 12px; line-height: 1.7;
    padding: 16px 20px; margin: 0; color: var(--text-secondary);
    overflow-x: auto; white-space: pre;
  }

  /* Endpoint table */
  .endpoint-table {
    border: 1px solid var(--border-subtle); border-radius: var(--radius-xl, 14px);
    overflow: hidden; box-shadow: var(--shadow-card);
  }
  .ep-header, .ep-row {
    display: grid; grid-template-columns: 70px 1fr 1fr; padding: 0 16px; align-items: center; min-height: 40px;
  }
  .ep-header {
    background: transparent; font-size: 11px; font-weight: 600;
    color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em;
    border-bottom: 2px solid var(--border-subtle);
  }
  .ep-row { border-bottom: 1px solid var(--border-subtle); font-size: 13px; }
  .ep-row:last-child { border-bottom: none; }
  .ep-row:hover { background: rgba(255,255,255,0.02); }
  .ep-method {
    font-size: 10px; font-weight: 700; font-family: var(--font-mono);
    padding: 2px 8px; border-radius: 4px; text-align: center; width: fit-content;
  }
  .ep-method.get { color: var(--success); background: var(--success-bg); }
  .ep-method.post { color: var(--running); background: var(--running-bg); }
  .ep-path { font-family: var(--font-mono); font-size: 12px; color: var(--text-primary); }
  .ep-desc { color: var(--text-muted); font-size: 12px; }

  /* WebSocket events */
  .ws-events { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; }
  .ws-event { font-size: 12px; color: var(--text-secondary); }
  .ws-event code { font-family: var(--font-mono); color: var(--accent); font-size: 11px; font-weight: 500; }

  @media (max-width: 768px) {
    .connect-grid { grid-template-columns: 1fr; }
    .ep-header, .ep-row { grid-template-columns: 60px 1fr; }
    .ep-desc-col, .ep-desc { display: none; }
  }
</style>

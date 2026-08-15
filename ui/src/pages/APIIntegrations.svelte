<script lang="ts">
  let baseUrl = window.location.origin;
  let copied = "";

  function copyText(text: string, label: string) {
    navigator.clipboard.writeText(text).then(() => {
      copied = label;
      setTimeout(() => (copied = ""), 2000);
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

  const websocketEvents = [
    "run.started",
    "run.completed",
    "run.failed",
    "node.status",
    "pipeline.updated",
  ];

  function navigateTo(id: string) {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  }

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
  -d '{"ref": "main", "trigger": "ci"}'`,
  };

  let activeTab: "python" | "curl" | "javascript" | "webhook" = "curl";
</script>

<div class="page animate-in">
  <header class="identity-hero">
    <div class="hero-main">
      <div class="hero-mark" aria-hidden="true">API</div>
      <div class="hero-copy">
        <span class="eyebrow">Organization control center / Developer platform</span>
        <h1>API & Integrations</h1>
        <span class="page-sub">Connect your tools, CI/CD, and scripts to Brokoli.</span>
      </div>
      <div class="hero-endpoint">
        <span>Organization API base</span><code>{baseUrl}/api</code>
        <button class="copy-btn" on:click={() => copyText(`${baseUrl}/api`, "base")}
          >{copied === "base" ? "Copied!" : "Copy base URL"}</button
        >
      </div>
    </div>
    <div class="status-summary" aria-label="API capability summary">
      <div class="status-segment accent">
        <span>Documented routes</span><strong>{endpoints.length}</strong><small
          >reference endpoints</small
        >
      </div>
      <div class="status-segment get">
        <span>GET operations</span><strong
          >{endpoints.filter((endpoint) => endpoint.method === "GET").length}</strong
        ><small>read and observe</small>
      </div>
      <div class="status-segment post">
        <span>POST operations</span><strong
          >{endpoints.filter((endpoint) => endpoint.method === "POST").length}</strong
        ><small>create and trigger</small>
      </div>
      <div class="status-segment live">
        <span>Realtime events</span><strong>{websocketEvents.length}</strong><small
          >documented WebSocket topics</small
        >
      </div>
    </div>
  </header>

  <!-- Quick connect cards -->
  <div class="connect-grid">
    <button class="connect-card" on:click={() => navigateTo("endpoints")}>
      <span class="card-index">01 / REFERENCE</span>
      <div class="cc-icon">
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"><path d="M4 17l6-5-6-5M12 19h8" /></svg
        >
      </div>
      <div class="cc-body">
        <h3>REST API</h3>
        <p>Documented access to pipelines, runs, connections, and variables.</p>
        <span class="card-context"
          >{endpoints.length} documented routes <b>View reference →</b></span
        >
      </div>
    </button>
    <button class="connect-card" on:click={() => navigateTo("quick-start")}>
      <span class="card-index">02 / AUTOMATION</span>
      <div class="cc-icon">
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><path
            d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"
          /></svg
        >
      </div>
      <div class="cc-body">
        <h3>Webhooks</h3>
        <p>Trigger pipelines from GitHub Actions, GitLab CI, or any HTTP client.</p>
        <span class="card-context">Token-authenticated triggers <b>Open examples →</b></span>
      </div>
    </button>
    <button class="connect-card" on:click={() => navigateTo("websocket-events")}>
      <span class="card-index">03 / REALTIME</span>
      <div class="cc-icon">
        <svg
          width="20"
          height="20"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          ><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7z" /><circle
            cx="12"
            cy="12"
            r="3"
          /></svg
        >
      </div>
      <div class="cc-body">
        <h3>WebSocket</h3>
        <p>Real-time events for run, node, and pipeline status changes.</p>
        <span class="card-context"
          >{websocketEvents.length} documented topics <b>Review events →</b></span
        >
      </div>
    </button>
  </div>

  <!-- Auth section -->
  <section class="section panel-section" id="authentication">
    <div class="panel-heading">
      <div>
        <span>Access control</span>
        <h2 class="section-title">Authentication</h2>
      </div>
      <strong>Bearer token</strong>
    </div>
    <div class="auth-card">
      <p class="auth-desc">Include your token in the <code>Authorization</code> header:</p>
      <div class="auth-example">
        <code>Authorization: Bearer brk_your_token_here</code>
        <button
          class="copy-btn"
          on:click={() => copyText("Authorization: Bearer brk_your_token_here", "auth")}
        >
          {copied === "auth" ? "Copied!" : "Copy"}
        </button>
      </div>
      <p class="auth-hint">
        In <a href="#/settings">Settings</a>, open the Users tab and use API Key Authentication.
      </p>
    </div>
  </section>

  <!-- Code examples -->
  <section class="section panel-section" id="quick-start">
    <div class="panel-heading">
      <div>
        <span>Implementation guide</span>
        <h2 class="section-title">Quick Start</h2>
      </div>
      <strong>{activeTab}</strong>
    </div>
    <div class="tabs">
      <button class="tab" class:active={activeTab === "curl"} on:click={() => (activeTab = "curl")}
        >cURL</button
      >
      <button
        class="tab"
        class:active={activeTab === "python"}
        on:click={() => (activeTab = "python")}>Python</button
      >
      <button
        class="tab"
        class:active={activeTab === "javascript"}
        on:click={() => (activeTab = "javascript")}>JavaScript</button
      >
      <button
        class="tab"
        class:active={activeTab === "webhook"}
        on:click={() => (activeTab = "webhook")}>Webhooks / CI</button
      >
    </div>
    <div class="code-card">
      <div class="code-header">
        <span class="code-lang">{activeTab}</span>
        <button class="copy-btn" on:click={() => copyText(codeExamples[activeTab], "code")}>
          {copied === "code" ? "Copied!" : "Copy"}
        </button>
      </div>
      <pre class="code-block">{codeExamples[activeTab]}</pre>
    </div>
  </section>

  <!-- Endpoints reference -->
  <section class="section panel-section" id="endpoints">
    <div class="panel-heading">
      <div>
        <span>REST inventory</span>
        <h2 class="section-title">Endpoints</h2>
      </div>
      <strong>{endpoints.length} routes</strong>
    </div>
    <div class="endpoint-scroll">
      <div class="endpoint-table">
        <div class="ep-header">
          <span class="ep-method-col">Method</span>
          <span class="ep-path-col">Path</span>
          <span class="ep-desc-col">Description</span>
        </div>
        {#each endpoints as ep}
          <div class="ep-row">
            <span
              class="ep-method"
              class:get={ep.method === "GET"}
              class:post={ep.method === "POST"}>{ep.method}</span
            >
            <code class="ep-path">{ep.path}</code>
            <span class="ep-desc">{ep.desc}</span>
          </div>
        {/each}
      </div>
    </div>
  </section>

  <section class="section panel-section" id="websocket-events">
    <div class="panel-heading">
      <div>
        <span>Realtime inventory</span>
        <h2 class="section-title">WebSocket Events</h2>
      </div>
      <strong>{websocketEvents.length} topics</strong>
    </div>
    <div class="auth-card">
      <p class="auth-desc">
        Connect to <code>{baseUrl}/api/ws?token=YOUR_TOKEN</code> for real-time events:
      </p>
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
padding: 7px 10px 0; border-inline: 1px solid var(--border-subtle); background: var(--bg-secondary);

<style>
  .page {
    width: 100%;
    min-width: 0;
  }
  .identity-hero {
    margin-bottom: 12px;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 11px;
    background:
      linear-gradient(
        120deg,
        color-mix(in srgb, var(--accent-glow), transparent 22%),
        transparent 52%
      ),
      var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .hero-main {
    display: flex;
    min-height: 124px;
    align-items: center;
    gap: 15px;
    padding: 20px;
  }
  .hero-mark {
    display: grid;
    width: 50px;
    height: 50px;
    flex: none;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 60%);
    border-radius: 11px;
    background: var(--accent-glow);
    color: var(--accent);
    font: 700 10px var(--font-mono);
    letter-spacing: 0.08em;
  }
  .hero-copy {
    min-width: 0;
    flex: 1;
  }
  .hero-endpoint {
    display: grid;
    max-width: 310px;
    justify-items: end;
    gap: 5px;
  }
  .hero-endpoint span {
    color: var(--text-dim);
    font: 600 8px var(--font-mono);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .hero-endpoint code {
    max-width: 100%;
    overflow: hidden;
    padding: 0;
    background: transparent;
    color: var(--text-secondary);
    font: 10px var(--font-mono);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .status-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    border-top: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 38%);
  }
  .status-segment {
    position: relative;
    display: grid;
    min-height: 72px;
    align-content: center;
    gap: 2px;
    padding: 11px 18px;
    border-right: 1px solid var(--border-subtle);
  }
  .status-segment:last-child {
    border-right: 0;
  }
  .status-segment::before {
    content: "";
    position: absolute;
    inset: 14px auto 14px 0;
    width: 2px;
    background: var(--border);
  }
  .status-segment.accent::before {
    background: var(--accent);
  }
  .status-segment.get::before {
    background: var(--success);
  }
  .status-segment.post::before {
    background: var(--running);
  }
  .status-segment.live::before {
    background: var(--node-transform);
  }
  .status-segment span {
    color: var(--text-muted);
    font: 600 8.5px var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .status-segment strong {
    color: var(--text-primary);
    font-size: 17px;
    font-weight: 650;
  }
  .status-segment small {
    color: var(--text-dim);
    font-size: 9px;
  }
  .eyebrow {
    display: block;
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .identity-hero h1 {
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.035em;
  }
  .page-sub {
    font-size: 13px;
    color: var(--text-muted);
    margin-top: 2px;
    display: block;
  }

  .section {
    margin-bottom: 18px;
    scroll-margin-top: 18px;
  }
  .section-title {
    margin-top: 3px;
    color: var(--text-primary);
    font-size: 13px;
    font-weight: 650;
  }
  .panel-heading {
    display: flex;
    min-height: 58px;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 10px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 9px 9px 0 0;
    background: linear-gradient(
      90deg,
      color-mix(in srgb, var(--bg-tertiary), transparent 35%),
      var(--bg-secondary)
    );
  }
  .panel-heading span {
    color: var(--accent);
    font: 600 8px var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .panel-heading strong {
    padding: 5px 8px;
    border: 1px solid var(--border-subtle);
    border-radius: 5px;
    background: var(--bg-primary);
    color: var(--text-muted);
    font: 600 9px var(--font-mono);
    text-transform: capitalize;
  }

  /* Connect cards */
  .connect-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px;
    margin-bottom: 18px;
  }
  .connect-card {
    position: relative;
    display: flex;
    width: 100%;
    min-height: 154px;
    align-items: flex-start;
    gap: 12px;
    padding: 36px 15px 15px;
    color: inherit;
    text-align: left;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 9px;
    box-shadow: var(--shadow-card);
    transition: border-color 200ms ease;
  }
  .connect-card:hover {
    border-color: color-mix(in srgb, var(--accent), transparent 45%);
    background: linear-gradient(145deg, var(--accent-glow), transparent 55%), var(--bg-secondary);
    transform: translateY(-1px);
  }
  .card-index {
    position: absolute;
    top: 13px;
    left: 15px;
    color: var(--text-dim);
    font: 600 8px var(--font-mono);
    letter-spacing: 0.08em;
  }
  .cc-icon {
    width: 36px;
    height: 36px;
    flex-shrink: 0;
    border-radius: 8px;
    background: var(--accent-glow);
    color: var(--accent);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .cc-body h3 {
    font-size: 14px;
    font-weight: 600;
    margin-bottom: 4px;
  }
  .cc-body p {
    font-size: 12px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .card-context {
    display: flex;
    margin-top: 14px;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    color: var(--text-dim);
    font: 9px var(--font-mono);
  }
  .card-context b {
    color: var(--accent);
    font: 600 9px var(--font-ui);
    white-space: nowrap;
  }

  /* Auth */
  .auth-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-top: 0;
    border-radius: 0 0 9px 9px;
    padding: 16px;
    box-shadow: var(--shadow-card);
  }
  .auth-desc {
    font-size: 13px;
    color: var(--text-secondary);
    margin-bottom: 12px;
  }
  .auth-desc code {
    font-family: var(--font-mono);
    color: var(--accent);
    font-size: 12px;
    font-weight: 500;
  }
  .auth-example {
    display: flex;
    align-items: center;
    justify-content: space-between;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 14px;
    margin-bottom: 10px;
  }
  .auth-example code {
    min-width: 0;
    overflow-wrap: anywhere;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-primary);
  }
  .auth-hint {
    font-size: 12px;
    color: var(--text-dim);
  }
  .auth-hint a {
    color: var(--accent);
  }

  .copy-btn {
    font-size: 11px;
    font-weight: 500;
    color: var(--accent);
    padding: 4px 10px;
    border-radius: 5px;
    border: 1px solid var(--accent);
    background: none;
    cursor: pointer;
    transition: all 150ms ease;
  }
  .copy-btn:hover {
    background: var(--accent-glow);
  }

  /* Tabs */
  .tabs {
    display: flex;
    gap: 2px;
    margin-bottom: 0;
    position: relative;
    z-index: 1;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    padding: 7px 10px 0;
    border-inline: 1px solid var(--border-subtle);
    background: var(--bg-secondary);
    scrollbar-width: none;
  }
  .tabs::-webkit-scrollbar {
    display: none;
  }
  .tab {
    padding: 8px 16px;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-muted);
    border: 1px solid transparent;
    border-bottom: none;
    border-radius: 8px 8px 0 0;
    background: none;
    cursor: pointer;
    transition: all 150ms ease;
    white-space: nowrap;
    flex: none;
  }
  .tab:hover {
    color: var(--text-secondary);
  }
  .tab.active {
    color: var(--text-primary);
    background: var(--bg-secondary);
    border-color: var(--border-subtle);
  }

  /* Code block */
  .code-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-top: 0;
    border-radius: 0 0 9px 9px;
    overflow: hidden;
    box-shadow: var(--shadow-card);
  }
  .code-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .code-lang {
    font-size: 10px;
    font-weight: 600;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .code-block {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.7;
    padding: 16px 20px;
    margin: 0;
    color: var(--text-secondary);
    max-width: 100%;
    overflow-x: auto;
    white-space: pre;
  }

  /* Endpoint table */
  .endpoint-scroll {
    overflow-x: auto;
    border: 1px solid var(--border-subtle);
    border-top: 0;
    border-radius: 0 0 9px 9px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .endpoint-table {
    min-width: 620px;
  }
  .ep-header,
  .ep-row {
    display: grid;
    grid-template-columns: 70px 1fr 1fr;
    padding: 0 16px;
    align-items: center;
    min-height: 40px;
  }
  .ep-header {
    background: transparent;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 2px solid var(--border-subtle);
  }
  .ep-row {
    border-bottom: 1px solid var(--border-subtle);
    font-size: 13px;
  }
  .ep-row:last-child {
    border-bottom: none;
  }
  .ep-row:hover {
    background: rgba(255, 255, 255, 0.02);
  }
  .ep-method {
    font-size: 10px;
    font-weight: 700;
    font-family: var(--font-mono);
    padding: 2px 8px;
    border-radius: 4px;
    text-align: center;
    width: fit-content;
  }
  .ep-method.get {
    color: var(--success);
    background: var(--success-bg);
  }
  .ep-method.post {
    color: var(--running);
    background: var(--running-bg);
  }
  .ep-path {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-primary);
  }
  .ep-desc {
    color: var(--text-muted);
    font-size: 12px;
  }

  /* WebSocket events */
  .ws-events {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 8px;
  }
  .ws-event {
    font-size: 12px;
    color: var(--text-secondary);
  }
  .ws-event code {
    font-family: var(--font-mono);
    color: var(--accent);
    font-size: 11px;
    font-weight: 500;
  }

  @media (max-width: 768px) {
    .hero-main {
      align-items: flex-start;
    }
    .hero-endpoint {
      display: none;
    }
    .status-summary {
      grid-template-columns: repeat(2, 1fr);
    }
    .status-segment:nth-child(2) {
      border-right: 0;
    }
    .status-segment:nth-child(-n + 2) {
      border-bottom: 1px solid var(--border-subtle);
    }
    .connect-grid {
      grid-template-columns: 1fr;
    }
    .connect-card {
      padding: 13px;
    }
    .tabs {
      margin-right: calc(var(--space-md) * -1);
      padding-right: var(--space-md);
    }
    .auth-example {
      align-items: flex-start;
      gap: 10px;
    }
    .endpoint-table {
      min-width: 560px;
    }
  }
  @media (max-width: 520px) {
    .hero-main {
      padding: 16px;
    }
    .hero-mark {
      width: 40px;
      height: 40px;
    }
    .identity-hero h1 {
      font-size: 22px;
    }
    .status-summary {
      grid-template-columns: 1fr;
    }
    .status-segment,
    .status-segment:nth-child(2) {
      min-height: 58px;
      border-right: 0;
      border-bottom: 1px solid var(--border-subtle);
    }
    .status-segment:last-child {
      border-bottom: 0;
    }
    .panel-heading strong {
      display: none;
    }
    .auth-card {
      padding: 14px;
    }
    .auth-example {
      flex-direction: column;
    }
    .auth-example .copy-btn {
      align-self: flex-end;
    }
    .code-header {
      padding-inline: 12px;
    }
    .code-block {
      padding: 14px;
      font-size: 11px;
    }
  }
</style>

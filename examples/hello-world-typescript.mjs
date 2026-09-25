/*
 * Run this dependency-free example with Node.js >= 20:
 *
 *   node examples/hello-world-typescript.mjs
 *
 * The `code` value below is the actual TypeScript code-node script. The
 * runner uses only Node's built-in fetch so it can deploy and run that node
 * against a local Brokoli server without an SDK or npm install.
 */
import os from "node:os";
import path from "node:path";

const server = (process.env.BROKOLI_SERVER ?? "http://localhost:8080").replace(/\/+$/, "");
let authToken = process.env.BROKOLI_TOKEN;
const username = process.env.BROKOLI_USERNAME;
const password = process.env.BROKOLI_PASSWORD;
const outputPath =
  process.env.BROKOLI_OUTPUT ?? path.join(os.tmpdir(), "brokoli-hello-world-typescript.csv");
const pipelineId = "hello-world-typescript";

const code = [
  "// `rows` is a mutable, materialized array in the TypeScript contract.",
  "// `rowsStream()` is the read-only streaming alternative; this demo uses rows.",
  "for (const row of rows) {",
  "  row.greeting = `Hello, ${row.name}`;",
  "}",
  "",
  "output_data = {",
  "  columns: [...columns, \"greeting\"],",
  "  rows,",
  "};",
].join("\n");

const pipeline = {
  ir_version: "2.0",
  pipeline_id: pipelineId,
  name: pipelineId,
  description: "Fetch employees and greet them with a TypeScript code node",
  enabled: true,
  nodes: [
    {
      id: "source",
      type: "source_api",
      name: "Fetch Employees",
      config: {
        url: "/api/samples/data/employees.json",
        method: "GET",
      },
      capabilities: ["source", "dataset-output"],
      position: { x: 40, y: 120 },
    },
    {
      id: "greet",
      type: "code",
      name: "Add Greeting",
      config: {
        language: "typescript",
        script: code,
      },
      capabilities: ["compute", "dataset-output"],
      position: { x: 360, y: 120 },
    },
    {
      id: "sink",
      type: "sink_file",
      name: "Save Result",
      config: {
        path: outputPath,
        format: "csv",
      },
      capabilities: ["sink"],
      position: { x: 680, y: 120 },
    },
  ],
  edges: [
    { from: "source", to: "greet" },
    { from: "greet", to: "sink" },
  ],
};

async function request(endpoint, options = {}) {
  const response = await fetch(`${server}${endpoint}`, {
    ...options,
    headers: {
      Accept: "application/json",
      ...(options.body === undefined ? {} : { "Content-Type": "application/json" }),
      ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
      ...(options.headers ?? {}),
    },
  });
  const text = await response.text();
  let body = text;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // Keep a non-JSON error response readable below.
  }
  if (!response.ok) {
    const detail = body && typeof body === "object" && "error" in body ? body.error : text;
    throw new Error(`Brokoli API ${response.status}: ${detail || response.statusText}`);
  }
  return body;
}

if (!authToken && (username || password)) {
  if (!username || !password) {
    throw new Error("set both BROKOLI_USERNAME and BROKOLI_PASSWORD, or use BROKOLI_TOKEN");
  }
  const login = await request("/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  if (!login?.token) throw new Error("Brokoli login response did not include a token");
  authToken = login.token;
}

const listing = await request("/api/pipelines?limit=100");
const pipelines = Array.isArray(listing) ? listing : listing?.items ?? [];
const existing = pipelines.find((item) => item.pipeline_id === pipelineId);
const saved = existing
  ? await request(`/api/pipelines/${encodeURIComponent(existing.id)}`, {
      method: "PUT",
      body: JSON.stringify(pipeline),
    })
  : await request("/api/pipelines", {
      method: "POST",
      body: JSON.stringify(pipeline),
    });

const run = await request(`/api/pipelines/${encodeURIComponent(saved.id)}/run`, {
  method: "POST",
  body: "{}",
});

const terminalStatuses = new Set(["success", "failed", "cancelled", "blocked"]);
const deadline = Date.now() + 60_000;
let result;
while (true) {
  try {
    result = await request(`/api/runs/${encodeURIComponent(run.id)}`);
  } catch (error) {
    // The trigger is asynchronous: the run row can appear just after the
    // 202 response. Treat that short 404 window as still pending.
    if (!(error instanceof Error) || !error.message.includes("Brokoli API 404: run not found")) {
      throw error;
    }
    result = undefined;
  }
  if (result && terminalStatuses.has(result.status)) break;
  if (Date.now() >= deadline) throw new Error(`timed out waiting for run ${run.id}`);
  await new Promise((resolve) => setTimeout(resolve, 250));
}

if (result.status !== "success") {
  throw new Error(`run ${run.id} ended with ${result.status}: ${result.error ?? "no error detail"}`);
}

console.log(`Brokoli TypeScript example succeeded (run ${run.id}).`);
console.log(`Output: ${outputPath}`);

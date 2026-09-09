# Brokoli Examples

These examples run against a local Brokoli server and use the built-in sample
data, so no external service is needed.

## Hello World (Python)

Start the server and install the Python SDK:

```bash
brokoli serve
pip install brokoli
```

Deploy and run the pipeline:

```bash
brokoli deploy examples/hello-world.py --server http://localhost:8080
brokoli run hello-world --server http://localhost:8080
```

Then open `http://localhost:8080` and inspect the run. The pipeline reads the
built-in employee dataset, adds a greeting column, and writes
`/tmp/brokoli-hello-world.csv`.

## Hello World (TypeScript code node)

[`hello-world.ts`](hello-world.ts) is a dependency-free TypeScript code-node
example. It uses Node's built-in `fetch` to create or update a pipeline, run it,
and poll the result; no npm package or TypeScript SDK is required.

TypeScript code nodes require **Node.js >= 20 on the machine running the
Brokoli server**. Node is optional for Brokoli and is never downloaded by the
installer. If the server has JWT authentication enabled, either provide a
pre-issued bearer token or let the example log in with the local admin account:

```bash
BROKOLI_USERNAME=admin \
BROKOLI_PASSWORD='your-admin-password' \
node --input-type=module < examples/hello-world.ts
```

A fresh server may still be in first-run setup; create the admin account in
the UI (or complete the installer's setup prompt) before running the example.
`BROKOLI_TOKEN` is also supported for an already-issued bearer token.

Run the example from the repository root (on the same machine as this local
server):

```bash
node --input-type=module < examples/hello-world.ts
```

The command uses stdin so it works with Node 20 without relying on a `.ts`
loader or an npm-installed transpiler. Override the server or output location
when needed:

```bash
BROKOLI_SERVER=http://localhost:8080 \
BROKOLI_OUTPUT=/tmp/my-hello-world.csv \
node --input-type=module < examples/hello-world.ts
```

The code node reads the built-in employee dataset, mutates the materialized
`rows` array in place to add `greeting`, and assigns `output_data` with the
updated `columns` and `rows`. That is intentional: `rows` is mutable in the
TypeScript worker contract. `rowsStream()` is the read-only streaming
alternative. The worker v1 executes JavaScript emitted by the authoring side,
so this small inline script is JavaScript-compatible rather than using
TypeScript-only syntax that would require on-worker transpilation.

The run detail shows the execution plan and node evidence. The dashboard and
Lineage view make the same run useful after the first demo: you can see what
was planned, what ran, and how the output relates to the input.

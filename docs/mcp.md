# Brokoli for AI agents (MCP)

Brokoli serves the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP), so an AI agent (a desktop assistant, an IDE, a hosted agent) can
work with your pipelines directly: list them, read their definitions,
validate a change, read runs and logs, follow lineage, and find out why a
run failed.

The agent acts **as you** (or as the account whose credential it has), and
sees exactly what that account may see. It never sees a credential.

This is [ADR-046](adr/046-agents-mcp-server-and-agent-identities.md):
the tools read, validate, and propose changes that a person applies
([change-requests.md](change-requests.md)). Agents have accounts of their
own ([agents.md](agents.md)). Tools that run pipelines come in a later
phase.

## Two ways to connect

| | For | Where it runs | Credential |
|---|---|---|---|
| **stdio**: `brokoli mcp` | an agent client on your machine | a process your client starts | what `brokoli login` stored, or `--api-key` / `BROKOLI_TOKEN` |
| **HTTP**: `POST /api/mcp` | a hosted agent, or a client that speaks Streamable HTTP | the Brokoli server itself | a bearer token, like any API request |

Both serve the same tools with the same rules.

### stdio, from a desktop client

Sign in once:

```bash
brokoli login --server https://brokoli.example.com
```

Then tell your client to run `brokoli mcp`. Most clients take a JSON
configuration like this one:

```json
{
  "mcpServers": {
    "brokoli": {
      "command": "brokoli",
      "args": ["mcp"]
    }
  }
}
```

To use another server or credential than the stored one, add
`"--server", "https://..."` and `"--api-key", "<token>"`, or set
`BROKOLI_TOKEN` in the client's environment for the server. `--workspace
<id>` acts in a workspace other than your default.

When it starts, `brokoli mcp` writes one line to standard error saying
which server it serves and as whom (`brokoli mcp: serving
https://brokoli.example.com as ada`); standard output carries only the
protocol. If the credential is missing or expired it exits with
`not signed in to ...: run 'brokoli login', or pass --api-key`.

### HTTP, from a hosted agent

```
POST https://brokoli.example.com/api/mcp
Authorization: Bearer <token>
Content-Type: application/json
Accept: application/json, text/event-stream
X-Workspace-ID: <workspace id>      (optional)
```

The endpoint is the MCP Streamable HTTP transport in its stateless form:
every request stands alone (no `Mcp-Session-Id`), answers are JSON, and
any API instance can answer any request. `GET` and `DELETE` answer 405.
Without a valid credential the request is refused with 401 before any MCP
processing, like every `/api` path.

## The tools

| Tool | What it returns | Listed to |
|---|---|---|
| `list_pipelines` | pipelines in the workspace: id, name, schedule, enabled, draft, source (`ui` or `git`), node count. Paginated | `pipelines.view` |
| `get_pipeline` | a pipeline's full definition (the pipeline IR): nodes with type and settings, edges, schedule, parameters. Canvas positions are left out | `pipelines.view` |
| `validate_pipeline` | whether a definition is valid and what is wrong, for a saved pipeline (`pipeline`) or a definition that is not saved (`definition`). Nothing is saved | `pipelines.view` |
| `get_lineage` | what a pipeline reads and writes, the pipelines upstream (writing what it reads) and downstream (reading what it writes) | `pipelines.view` |
| `list_runs` | a pipeline's runs, newest first: status, times, pipeline version, error. Paginated | `runs.view` |
| `get_run` | one run with every node's status, rows, duration, attempt and error | `runs.view` |
| `get_run_logs` | a run's log lines, for one node or level if asked; the last lines unless an `offset` is given | `runs.view` |
| `explain_run` | what happened and why, in one answer (below) | `runs.view` |
| `list_change_requests` | a pipeline's change requests: title, status, whether an open one is out of date, its validation, who proposed and who decided | `pipelines.view` |
| `propose_change` | proposes a new version of a pipeline as a change request a person applies ([change-requests.md](change-requests.md)) | `pipelines.propose` |

Every tool but `propose_change` is marked read only. `propose_change`
changes nothing that runs, but records a change request, and is marked
accordingly, so a client that asks before changing anything asks there.

### `explain_run`

The tool to start with when a run failed. It gathers what a person reads
across the run page, its logs, the pipeline and its history:

- **summary**: a few sentences, for example *Run 01a1... of orders:
  failed. Node "Fetch orders" (fetch, source_api) failed after 2 retries:
  GET https://shop.example.com/v2/orders: 404 Not Found. The last
  successful run was 01a0... (version 1); the pipeline changed 1 time(s)
  since.*
- **failures**: each failed node with its name, type, error, retries and
  its last 30 log lines;
- **error_logs**: the run's last 20 error lines;
- **last_successful_run**: the latest successful run that started before
  this one (from the pipeline's last 50 runs);
- **changes_since_last_success**: the pipeline versions saved between that
  run and this one, with their messages, when the versions differ;
- **next_steps**: what to look at next;
- **could_not_check**: anything it could not read, and why (a deleted
  pipeline, a missing permission), rather than failing as a whole.

When the last successful run had the same pipeline version, the summary
says the cause is likely outside the definition: its data, a source, a
connection or the worker.

## What an agent can and cannot see

**Your permissions, no more.** Each tool call is a request to Brokoli's own
REST API, sent as the MCP request's caller: on the server it goes through
the same router, authentication and permission checks as any API call; in
`brokoli mcp` it is an HTTP request with your credential. There is no
separate authorization for MCP. The tool list itself is filtered: a tool
whose permission the caller lacks is not offered, and calling it anyway is
refused.

**No credentials.** Every result is redacted before it leaves:

- values under names such as `password`, `secret`, `token` (a pipeline's
  `webhook_token` included), `api_key`, `authorization`, `private_key`,
  `credentials`, `cookie`, `signature`, in any case and anywhere in a
  definition (a node's headers, nested settings);
- the password in a URL (`postgres://app:[redacted]@db/x`);
- `Bearer ...` and `Basic ...` credentials written into text, log lines
  included.

What is kept: references to secrets (`{{ secret.db }}`, `${DB_PASSWORD}`),
so an agent can see how a pipeline is wired; empty values; true/false
flags.

**Bounded answers.** Lists are pages (50 by default, at most 100) with a
`next_cursor`. Logs return at most 1,000 lines per call (200 by default),
with `total` and a `more` note saying how to get the rest. A single error
or log line is cut at 2,000 characters, saying how much was cut. An agent
never receives a result too large for its context by accident.

**Errors say what to do.** A refusal names the cause: `not signed in to
Brokoli`, `not permitted: ...`, `not found: run not found`, or the
validation errors of a definition.

## Credentials for agents

Give an agent an **agent identity** of its own ([agents.md](agents.md)):
an account with a role (by default `agent`: see pipelines, runs and
lineage, and run pipelines), an owner it is never above, and its own
tokens that expire and can be revoked. Pass its token as `--api-key` or
`BROKOLI_TOKEN` to `brokoli mcp`, or as the bearer token to `/api/mcp`.
The agent is told who it acts as ("You act in Brokoli as agent
triage-bot, on behalf of ada"), and `brokoli mcp` prints the same on
standard error.

Do not give an agent the server's static API key (`brk_...`,
`--api-key` on the server): it is an administrator. When it is used,
the MCP server's instructions start with a warning to the agent, and
`brokoli mcp` warns on standard error.

## Troubleshooting

| Seen | Cause |
|---|---|
| `not signed in to ...: run 'brokoli login'` from `brokoli mcp` | no stored credential, or it expired; sign in again or pass `--api-key` |
| The client lists no tools | the account holds neither `pipelines.view` nor `runs.view` in that workspace |
| `list_pipelines` is empty | the pipelines are in another workspace; pass `--workspace` or the `X-Workspace-ID` header |
| 401 from `/api/mcp` | the request has no bearer token, or it is invalid |
| 405 from `/api/mcp` | a `GET` or `DELETE`; the endpoint is stateless and takes `POST` only |

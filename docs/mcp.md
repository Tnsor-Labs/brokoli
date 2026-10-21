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
| **HTTP**: `POST /api/mcp` | a hosted agent, or a client that speaks Streamable HTTP | the Brokoli server itself | a bearer token, like any API request, or OAuth (below) |

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
processing, like every `/api` path; the 401 carries
`WWW-Authenticate: Bearer resource_metadata="<server>/.well-known/oauth-protected-resource"`,
which is how an OAuth-capable client finds where to sign in.

### HTTP with OAuth, from a hosted client

A client that supports the MCP authorization specification (a chat
assistant's connector, say) needs only the endpoint's URL: it discovers
the rest, registers itself, and sends you to Brokoli to approve it. No
token is pasted anywhere.

1. Add `https://brokoli.example.com/api/mcp` to the client as a remote MCP
   server.
2. The client opens Brokoli's approval page. Sign in if you are not, read
   what is asking and where it will send you back, and **Approve**.
3. The client receives a one-time code and trades it for its tokens.

What it gets:

- **It acts as you**, with your permissions as they are on each request,
  in your default workspace (or the one it names with `X-Workspace-ID`).
  What it does is recorded under your name. Only a person can approve a
  connection; an agent's token cannot.
- **Short-lived access, renewed.** An access token (`bko_...`) lasts an
  hour; the client renews it with a refresh token (`bkr_...`), which lasts
  30 days and is replaced each time it is used. Presenting a refresh token
  that was already used ends the connection, since only a stolen copy
  would do that.
- **Revocable.** The client can revoke it (`POST /oauth/revoke`); a person
  who is disabled or removed takes all their connections with them.

The client's name on the approval page is the one it registered with: it
is the client's own claim, so check where it sends you back (shown on the
page) before approving.

For a deployment behind a proxy, set `BROKOLI_PUBLIC_URL` to the address
clients use (`https://brokoli.example.com`) so the discovery documents
name it rather than the address the request arrived on.

The endpoints, for reference:

| | |
|---|---|
| `GET /.well-known/oauth-protected-resource` | the resource (`/api/mcp`) and its authorization server (RFC 9728) |
| `GET /.well-known/oauth-authorization-server` | the endpoints below (RFC 8414) |
| `POST /oauth/register` | dynamic client registration (RFC 7591); public clients only (`token_endpoint_auth_method: none`), redirect URIs https or http to localhost |
| `GET /oauth/authorize` | `response_type=code`, PKCE with `code_challenge_method=S256` required; sends the browser to the approval page |
| `POST /oauth/token` | `grant_type=authorization_code` (with `code_verifier`) or `refresh_token`, form-encoded |
| `POST /oauth/revoke` | ends the connection a token belongs to (RFC 7009) |

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
| `dry_run_pipeline` | a preview of a saved pipeline: each step's columns and first 5 rows, its error, or why it was not run. Nothing is written and no run is recorded | `pipelines.run` |
| `trigger_run` | runs a saved pipeline now, with `params` and declared `parameters`; with `wait_seconds` (up to 45) the run's outcome, otherwise its id | `pipelines.run` |
| `cancel_run` | cancels a run that has not finished | `runs.cancel` |

`propose_change`, `trigger_run` and `cancel_run` act; every other tool is
marked read only. `propose_change` changes nothing that runs but records
a change request; `trigger_run` runs a pipeline, which reads and writes
what the pipeline does; `cancel_run` stops a run. They are marked so in
their annotations (`trigger_run` and `cancel_run` as destructive), so a
client that asks before acting asks the person there.

### Running a pipeline

An agent with the built-in `agent` role may preview and run pipelines
but not cancel runs. The loop it closes: it proposes a change, a person
applies it, and the agent runs the pipeline and checks the outcome:

```
dry_run_pipeline {"pipeline": "<id>"}                        what each step would produce, writing nothing
trigger_run      {"pipeline": "<id>", "wait_seconds": 30}    {"run_id": "...", "finished": true, "run": {"status": "success", "node_runs": [...]}}
explain_run      {"run": "<run_id>"}                         when it did not succeed
```

A run started this way is the agent's, recorded with whom it acts for,
like one started from its token over the API. `dry_run_pipeline` previews
the pipeline as it is saved: previewing a definition that is not saved
would run code nobody has reviewed, which only a person starts (a change
request's test).

Its `description` is Markdown, rendered on the change's review page: the
tool asks the agent for why the change is needed, what it does step by
step, what to expect in the data, and what the person should check, in
short sections. The diff is shown next to it, so the description does
not repeat it. The person can then test the change on a few rows before
applying it; an agent cannot start a test, since a test runs the
proposed code.

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

A distribution can add tools and add to `explain_run`
(`mcpserver.Register`): its tools follow the same rules (listed by
permission, calls through the same routes, redacted, bounded), and what it
adds to an explanation appears under `context`, for instance where the
pipeline was deployed from or the change behind the failing version.

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

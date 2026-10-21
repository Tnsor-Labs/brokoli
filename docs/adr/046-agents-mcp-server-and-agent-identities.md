# ADR-046: Agents as users of the runtime: an MCP server, agent identities and change requests

**Status:** accepted (approved by the product owner, 2026-10-20)
**Date:** 2026-10-20

## Context

Brokoli is the runtime data professionals build, run and explain
pipelines on. More and more of that work is done with an AI agent: one
that reads a failing run, works out why, and changes the pipeline. Agents
already reach Brokoli the only way they can, through the REST API or the
CLI with a credential, and today that is unsafe in three ways.

**There is no agent-shaped way in.** An agent has to be taught the REST
API from documentation, endpoint by endpoint. The protocol agent clients
actually speak, the Model Context Protocol (MCP), is not served. Every
agent integration is bespoke, and most never get built.

**The only non-human credential is an administrator.** A static API key
(`brk_...`, `AuthConfig.Keys`) is "a complete identity on its own": the
middleware stamps it with the `admin` role (`api/auth.go`, `APIKeyAuth`).
An agent given a key can delete pipelines, read secrets
(`variables.view_secrets`), manage users and change settings. Giving it a
person's session instead is worse: what it does is recorded as that
person, and the session's lifetime and scope are the person's. Data teams
will not, and should not, hand either to an agent.

**A change takes effect when it is saved.** Saving a published pipeline
replaces what runs (`PUT /api/pipelines/{id}`); versions and rollback
exist, but only after the fact. For a person in the editor that is the
expected behavior. For an agent it means an unreviewed edit is live on
the next schedule. Drafts (`draft: true`) cover a pipeline that has never
been published, not a change to one that has.

What already exists and an agent needs is most of the runtime: the
pipeline IR (JSON, validated, with typed task contracts, ADR-032), static
validation (`/pipelines/validate`, `/pipelines/{id}/validate-nodes`),
dry runs (`/pipelines/{id}/dry-run`), runs and their logs, events, plans,
node previews and profiles (`/runs/{id}/...`), provenance
(`/runs/{id}/provenance`) and lineage (`/lineage`), versions and rollback,
roles and permissions (`models/role.go`), and the audit log. The decision
below adds the three missing pieces around them rather than a second
system.

## Decision

Brokoli serves **MCP** as a first-class interface; an agent connects as
an **agent identity** with a role, never as an administrator key or a
person's session; and an agent's change to a published pipeline is a
**change request** that a person applies, never a direct save.

### 1. The MCP server

`brokoli` speaks MCP two ways, over one implementation:

- **stdio**, `brokoli mcp`: for agent clients on a person's machine
  (desktop assistants, IDEs). It talks to a Brokoli server with the
  credential `brokoli auth login` already stores (ADR-021), or an agent
  token (section 2) from `BROKOLI_TOKEN`.
- **Streamable HTTP**, `POST /api/mcp` on the API server: for hosted agents.
  Authenticated like every other API request, by an agent token or a
  session; unauthenticated requests get a 401 with the protected-resource
  metadata MCP clients use to find how to sign in.

Each tool is a thin wrapper over the handler or store call the REST API
already uses, **behind the same permission check**: a tool needs the
permission its REST route needs, evaluated for the calling identity in
the workspace the tool names. There is no second authorization path, no
tool an identity can call that the REST API would refuse it, and the
tools are listed per caller: a tool the identity lacks the permission for
is not offered at all.

The first tools:

| Tool | Does | Needs |
|---|---|---|
| `list_pipelines` | pipelines in the caller's workspaces, with state and last run | `pipelines.view` |
| `get_pipeline` | a pipeline's IR, version and source | `pipelines.view` |
| `validate_pipeline` | validate an IR (or a pipeline by id) without saving | `pipelines.view` |
| `dry_run_pipeline` | the dry run, with node previews bounded in rows | `pipelines.run` |
| `list_runs` / `get_run` | runs and one run's state, nodes, timings | `runs.view` |
| `get_run_logs` | a run's logs, paginated and bounded | `runs.view` |
| `explain_run` | one answer to "what happened and why": status, the failing node and its error, the relevant log lines, the plan, provenance, and what changed since the last good run | `runs.view` |
| `get_lineage` | upstream and downstream of a pipeline or dataset | `pipelines.view` |
| `propose_change` | a change request (section 3) | `pipelines.edit` |
| `trigger_run` | run a pipeline now, with parameters | `pipelines.run` |
| `cancel_run` | cancel a run | `runs.cancel` |

The rules every tool follows:

- **Bounded output.** Lists are paginated; logs and previews are cut at a
  size an agent's context can hold, saying so and how to get more. An
  agent asking for a million rows gets a page and a count.
- **No secrets, ever.** Connection credentials and secret variables are
  redacted in every tool result, whatever the role. `variables.view_secrets`
  does not apply over MCP.
- **Read tools are read only.** Only `propose_change`, `trigger_run` and
  `cancel_run` change anything, and they are marked as such in the tool
  annotations so clients can ask the person first.
- **Errors say what to do.** A refusal names the permission missing or the
  validation error, in words an agent can act on, as the REST API does.

A distribution can register further tools through the extension
registry, under the same rules: the permission each needs, bounded
output, no secrets. Core does not know what they do.

`explain_run` is the one tool that is more than a wrapper. It composes
what a person reads across four pages into one bounded, structured
answer, because explaining a run is what an agent is asked to do most,
and is what Brokoli knows best.

### 2. Agent identities

An **agent** is a kind of user: it has a name, an owner (the person who
created it and answers for it), a role, and membership of the
workspaces it may act in. It has no password and cannot sign in to the
UI.

- **Tokens.** An agent authenticates with tokens issued to it by its
  owner or an administrator, through the session and token model of
  ADR-021: each token is recorded, has an expiry, can be revoked on its
  own, and shows when it was last used. Tokens are shown once.
- **A role like anyone's.** The agent's permissions are its role's, in
  its workspaces, exactly as for a person. A built-in **`agent`** role is
  the default: view pipelines and runs, run pipelines, propose changes;
  no delete, no secrets, no settings, no users, no publishing. An
  administrator can give an agent another role, and that is visible
  wherever agents are listed.
- **Never above its owner.** An agent's effective permissions are its
  role's intersected with its owner's. Removing or demoting the owner
  narrows or stops the agent.
- **Attributed.** Every audit entry, run, version and change request
  records the agent, and the person it acts for when the client says
  (a token can be issued "on behalf of" its owner). The UI shows
  "Fix the orders schedule, proposed by `triage-bot` for Ada".

Static API keys stay, for bootstrapping and automation that genuinely
needs everything, and keep their administrator meaning; the
documentation says plainly that they are not for agents, and the MCP
server warns when it is reached with one.

### 3. Change requests

A **change request** is a proposed new version of a pipeline that does
not run until a person applies it.

- **Made** by `propose_change` (or `POST /api/pipelines/{id}/changes`)
  with the edited IR and a description. The server validates it, works
  out the diff against the current version in IR terms, and records it
  with who proposed it. Nothing that runs changes.
- **Checked.** The request carries its validation result and, when asked
  and permitted, a dry run of the proposed version, so the person
  deciding sees what it would do.
- **Applied** by a person with `pipelines.edit` (and, for an agent's
  request, never by the agent that made it): the proposed version becomes
  the pipeline's next version, exactly as a save would, and the request
  records who applied it. **Declined** requests are kept, with the reason.
- **Stale** when the pipeline changes after the request was made: it can
  be refreshed against the new version or declined, never applied over a
  version its author did not see.
- **Shown** in the pipeline's page and the editor: the diff, the checks,
  apply and decline.

A pipeline whose definition lives elsewhere (one deployed from a
repository) cannot be changed by applying a request in place. A
distribution can deliver its change requests instead, for instance as a
pull request against that repository, through the same extension point:
core records and shows the request; the distribution says where it went.

People keep saving directly, as today. A change request is how a change
arrives from an identity that should not, or chooses not to, save
directly; an agent's role does not include saving a published pipeline,
so for an agent it is the only way.

## Consequences

### Positive

- Any MCP client (desktop assistants, IDEs, hosted agent frameworks) can
  use Brokoli with no Brokoli-specific integration.
- An agent's power is a role, chosen and visible, and bounded by a person
  who answers for it; a leaked agent token is revocable on its own and
  cannot read secrets.
- An agent cannot change what runs; it can only propose, with the
  validation and dry run attached, and a person decides. That is the
  property that makes agents acceptable in a data team.
- Change requests are useful without agents: a person can propose a
  change to a pipeline they may not edit, and a reviewer can apply it.
- `explain_run` makes Brokoli's lineage, provenance and run history the
  thing agents come to it for.

### Negative

- A second interface to keep in step with the REST API. Mitigated by
  tools being wrappers over the same handlers, and by a test that every
  tool's permission matches its route's.
- A new kind of user and a new role touch authentication, the role
  model, the audit log and the users page.
- Change requests add a stored object with its own lifecycle (open,
  stale, applied, declined) and UI.
- MCP is young and still changing; the HTTP transport and its
  authorization in particular. Pinning a protocol version and following
  it is ongoing work.

### Deferred

- **Agents writing new pipelines from nothing** beyond what
  `propose_change` on a draft allows, and any agent built into the UI.
  This ADR makes Brokoli a good place for agents to work; it does not
  ship an agent.
- **MCP resources and prompts.** Tools first; pipelines and runs as
  resources, and prompts for common tasks, when clients use them.
- **OAuth sign-in for remote MCP clients.** Phase 1 uses tokens; the
  authorization flow MCP specifies for HTTP comes with a later phase.
- **Rate limits and quotas per agent**, beyond the server's existing ones.
- **Approval policies** (two approvers, auto-apply for some changes).

## Alternatives considered

- **Document the REST API for agents and stop there.** Every agent
  framework would need its own integration, and the credential problem
  stays: the REST API's only non-human key is an administrator.
- **Agents use a person's session or API key.** Fastest, and the reason
  teams refuse agents: no scope, no separate revocation, and the audit
  log says the person did it.
- **Agents save directly, with rollback as the safety net.** Rollback is
  after the fact: an unreviewed change has already run, possibly written
  data. Review before running is the point.
- **A separate MCP gateway process.** Another thing to deploy, and a
  second place to get authorization wrong. Serving MCP from the server
  and the CLI keeps one implementation and one permission check.
- **Change requests only in a distribution.** Proposing is how agents
  should change anything, everywhere Brokoli runs, including a single
  binary on a laptop.

## Follow-ups

Phases, each usable on its own:

1. **Read-only MCP.** `brokoli mcp` (stdio) and `POST /api/mcp`; the read tools
   (`list_pipelines`, `get_pipeline`, `validate_pipeline`, `list_runs`,
   `get_run`, `get_run_logs`, `get_lineage`) and `explain_run`; bounded
   output and redaction; the permission-parity test; docs with a
   walk-through from a desktop client.
2. **Agent identities.** The agent user kind, owner, the `agent` role, tokens
   on the ADR-021 model, "on behalf of", attribution in audit and runs,
   the agents section of the users page, the warning for static keys.
3. **Change requests.** The stored request, diff, validation and optional
   dry run, apply and decline, staleness, the pipeline page and editor
   view, and `propose_change`; the extension point for delivering a
   request elsewhere.
4. **Acting tools.** `dry_run_pipeline`, `trigger_run`, `cancel_run`, with
   tool annotations marking them as changing things; the tool-registration
   extension point for distributions.
5. **Remote sign-in.** OAuth for the HTTP transport, so a hosted client can
   connect without a pasted token.

## Implementation notes

- **Phase 1** (`pkg/mcpserver`, `cmd/mcp.go`, docs/mcp.md): the endpoint is
  `/api/mcp` rather than `/mcp`, so the API's authentication middleware
  refuses an unauthenticated request before any MCP processing, as for
  every `/api` path. Tools reach the REST API through a `Caller`: in
  process through the server's router for `/api/mcp` (a fresh context per
  sub-request, so it is authenticated from the forwarded headers alone and
  the router does not resume the outer request's routing state), over HTTP
  for `brokoli mcp`. Stateless Streamable HTTP; tools listed by
  `/api/auth/me/permissions`. The 401 carries no protected-resource
  metadata yet: that comes with OAuth in phase 5.
- **Phase 2** (`api/agents.go`, `api/agents_handlers.go`, `cmd/agents.go`,
  docs/agents.md): agents are rows of the users table with kind `agent`,
  owner and a password hash nothing matches; tokens are opaque (`bka_` +
  32 random bytes), stored as SHA-256 in `agent_tokens`, and resolved in
  `JWTAuth` before JWT parsing. ADR-021's session model is not built, so
  agent tokens have their own table rather than sharing one with people's
  sessions. The owner's role travels in the claims and is read on every
  request, so the intersection follows the owner. Core decides route
  permissions two ways: `PermissionChecker` reads the roles table, while
  the open-source route fallback (`roleAllows`) knows only admin, editor
  and viewer by name and refuses every other role. An agent is decided
  by the former, through the fallback too, since its permissions are a
  computed set no role name describes. (The same split leaves a person
  with the operator or a custom role refused by routes that
  `/api/auth/me/permissions` says they may use; that is a separate,
  pre-existing issue.) Core has no users page, so agents are managed with
  the API and the CLI; a UI comes with the distribution that has one.
- **Phase 3** (`api/change_requests.go`, `pkg/mcpserver/changes.go`, the
  pipeline page's `ChangeRequests`, docs/change-requests.md): requests are
  stored whole in `change_requests` beside the pipelines (created under
  the migration lock on Postgres); stale is computed (open and the base
  version is no longer the latest), not stored. Applying runs the edit's
  own save (`saveUpdate`, extracted from the update handler), so a request
  is exactly an edit. Simplifications against the decision above: agents
  never apply, rather than only the agent that proposed; an out-of-date
  request cannot be refreshed, only declined and proposed again; the dry
  run of a proposed version and opening a proposal in the editor are not
  built (the dry run endpoint works on saved pipelines). The new
  `pipelines.propose` permission is in the admin, editor, operator and
  agent roles. A proposal made without canvas positions (as over MCP)
  keeps the current positions of the nodes it keeps; found live, since
  `get_pipeline` leaves positions out.
- **Phase 4** (`pkg/mcpserver/acting.go`): `dry_run_pipeline`,
  `trigger_run` and `cancel_run`, through the REST routes with their
  permissions. `trigger_run` takes an optional `wait_seconds` (up to 45,
  under the API's request timeout) and answers the run's outcome, so an
  agent runs a pipeline and reads how it went in one call.
  `dry_run_pipeline` previews saved pipelines only: a preview of an
  unsaved definition runs code no person has reviewed, and a change
  request's test, which does, is started by a person. Previews run in a
  scratch store, write nothing and stop their sources at the rows they
  keep. The tool-registration extension point shipped with phase 1.
- **Phase 5a** (`api/oauth.go`, `OAuthConsentPage` in `@brokoli/auth`,
  docs/mcp.md): Brokoli is the authorization server and the protected
  resource. Discovery (RFC 9728, RFC 8414), the 401's `WWW-Authenticate`
  on `/api/mcp`, dynamic registration of public clients only (RFC 7591;
  https or loopback redirects), the authorization code grant with PKCE
  S256 required, refresh with rotation (a replayed refresh token ends the
  grant), revocation (RFC 7009). Tokens are opaque and stored hashed:
  `bko_` access for an hour, `bkr_` refresh for 30 days. A grant acts as
  the person who approved it, resolved on each request like an agent
  token, and stops when they are disabled. The approval is a JSON-only
  authenticated call, so a cross-site form cannot make a signed-in browser
  approve; agents cannot approve.
- **Phase 5b** (`api/oauth.go`, `ConnectedApps` in `@brokoli/auth`,
  Settings, Connected apps): the approval can name one of the approver's
  enabled agents (`agent_id`); the code and the grant carry it, and the
  access token then resolves to that agent's claims, built by the same
  `agentClaims` as its own tokens, with the approver as owner. Every
  request and every refresh re-checks that the agent is enabled and still
  the approver's; disabling it also revokes its grants. A person lists
  their live grants and revokes one (`/api/oauth/connections`); an agent
  is refused both, so a connection cannot see or end the others. Each
  guard was observed failing under mutation, including the two that back
  each other up (revocation on disable, re-check on resolve).


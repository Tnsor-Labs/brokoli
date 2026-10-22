Warning: truncated output (original token count: 79942)
Total output lines: 6078

# Changelog

All notable changes to Brokoli are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project loosely follows [Semantic Versioning](https://semver.org/).

Every entry names its owner. With more than one contributor in the
codebase, "who shipped this" is part of the record, not something to
reconstruct from git archaeology.

## [Unreleased]

### Fixed

- **Vulnerability preflight distinguishes tool failures from findings** --
  @arin30. Preflight reuses only the pinned `govulncheck` version, reports a
  missing or unusable scanner as a non-green `SKIP`, and keeps actual
  vulnerability findings as failures. Empty or unparseable scanner output is
  treated as unavailable; the earlier build stage remains responsible for
  reporting source compilation failures.

### Added

- **Connect an MCP client with OAuth** (ADR-046 phase 5) -- @hc12r. A
  hosted client that speaks the MCP authorization specification connects
  to `/api/mcp` with only its URL: it discovers the authorization server
  (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`),
  registers itself, and sends the person to Brokoli's approval page; the
  person approves and the client gets tokens. PKCE (S256) is required;
  access tokens last an hour and are renewed with rotating refresh tokens;
  a connection acts as the person who approved it and can be revoked.
  `BROKOLI_PUBLIC_URL` names the server's public address for discovery.
- **Connect an MCP client as one of your agents, and see your connected
  apps** (ADR-046 phase 5b) -- @hc12r. The approval page offers the
  person's enabled agents besides themselves; a connection made as an
  agent acts exactly as one of its tokens (its role, capped by the
  owner's, attributed to the agent), and ends when the agent is disabled
  or changes owner. **Settings, Connected apps** lists each connection
  (application, identity, connected, last used) and disconnects it at
  once (`GET /api/oauth/connections`, `DELETE /api/oauth/connections/{id}`;
  persons only). The `ConnectedApps` component is exported from
  `@brokoli/auth`.
- **`api.SetChangeRequestDelivery`** -- @hc12r. A distribution that
  delivers a change request elsewhere (a pull request opened after the
  request is recorded) records where it went once it knows; only an open
  request's delivery changes. With `SetChangeRequestOutcome`, a delivered
  request shows its link on the review page and settles when it is merged
  or closed there.
- **A shorter sidebar, and Home** -- @hc12r. The community sidebar goes
  from eleven items to seven, grouped by what a person is doing: Home and
  Pipelines; Observe (Dashboard, Lineage); Connect (Connections, Secrets &
  variables, Integrations); with Settings and Help at the bottom. Pages
  that belong together share one item and a tab strip (Dashboard and
  Schedule; Lineage and Pipeline dependencies; Variables and Secret
  stores; Plugins and Drivers), each keeping its own address, so links
  keep working. Home, where sign-in now lands, lists what needs you: the
  pipelines whose latest run failed and the changes waiting for a
  decision, counted next to Home in the sidebar. Search still finds every
  page by its own name, with the shortcut letters it had. The tab strip
  is a shared component (`SectionTabs` in `@brokoli/shell`).
- **`GET /api/changes`: the workspace's change requests** -- @hc12r.
  Across its pipelines, newest first (at most 100), each with
  `pipeline_name`, filtered by `status`; only pipelines the caller may see.
  For a person's list of what to review, wherever it was proposed.
- **Agents run pipelines over MCP** (ADR-046 phase 4) -- @hc12r.
  `trigger_run` runs a saved pipeline, with params and declared
  parameters, and with `wait_seconds` (up to 45) answers the run's
  outcome; `dry_run_pipeline` previews one, writing nothing; `cancel_run`
  stops a run (`runs.cancel`, which the `agent` role does not have). The
  acting tools are marked as such in their annotations, so a client asks
  the person first, and a run started this way is recorded as the agent's.
- **A review page for each change request, and testing a change before
  applying it** -- @hc12r. A proposed change opens on its own page
  (`/pipelines/{id}/changes/{changeId}`, linked from the pipeline's page
  and from the editor): its description rendered as Markdown; a card per
  node it adds, changes or removes, with scripts and SQL as line diffs;
  the pipeline's graph with what changes marked; and a **Test** that
  previews the current and proposed pipelines side by side on 10 rows,
  showing step by step the columns, row counts and values that differ.
  `POST /api/pipelines/{id}/changes/{changeId}/test` answers both
  previews. A test runs the proposed code, so only a person with
  `pipelines.run` starts one; an agent is refused. `propose_change` tells
  agents the description is Markdown and what to put in it.
- **`/api/auth/me` names the workspace** -- @hc12r. The answer adds
  `workspace_id`, and `workspace_name` when it is known: the workspace
  the request acts in. The MCP server says it in its instructions ("You
  act in Brokoli as agent triage-bot, on behalf of ada, in the workspace
  "Data Platform" (id ...)"), since an agent acts in one workspace and
  may not list workspaces, and could not tell which one it was in.
- **`api.AgentHooks`** -- @hc12r. A distribution takes part in agent
  identities: `Created` (an error disables the agent again and is the
  answer, with `AgentHookError`'s status), `Updated`, `Disabled`, and
  `Visible`, which hides agents the caller may not see from the list and
  every per-agent route. Where a distribution resolves a request's
  permissions (`RequestIdentityFunc`), an agent's stand as resolved, less
  what no agent may hold: the distribution binds them to the owner in its
  own roles.

- **MCP extensions for distributions** -- @hc12r. `mcpserver.Register`
  adds a distribution's tools (listed by the permission each needs,
  answering through the same caller, redacted and bounded like core's,
  with `AddReadTool`, `Get` and `Esc`) and facts to `explain_run`, which
  appear under `context`; a failing one is reported in `could_not_check`
  instead of failing the explanation.

- **`api.AgentIdentitiesEnabled`** -- @hc12r. A distribution whose
  tenancy agents do not take part in yet sets it to false: `/api/agents`
  then answers 404 and no agent token authenticates. Agents belong to no
  organization in core.

- **Change requests** (ADR-046 phase 3) -- @hc12r. A proposed new
  version of a pipeline that does not run until a person applies it:
  `POST /api/pipelines/{id}/changes` records the whole definition, the
  version it was made against and its validation; the diff (pipeline
  fields, nodes added, removed and changed, connections) is computed on
  read. Applying (`pipelines.edit`, people only) is exactly a save, with
  the version message `change request: <title>`; a request whose pipeline
  changed since is out of date and refused; declining takes a reason, and
  the author can withdraw. New permission `pipelines.propose` (admin,
  editor, operator and agent roles). The MCP tools `propose_change` and
  `list_change_requests`; the pipeline page shows open requests with the
  diff and Apply / Decline. Pipelines deployed from a repository take
  requests only through a distribution's `api.ChangeRequestDeliverFunc`.
  See `docs/change-requests.md`.

- **Agent identities** (ADR-046 phase 2) -- @hc12r. An agent is an
  account for an AI agent: a role (the new built-in `agent` role by
  default: view pipelines and runs, run pipelines; never `admin`), an
  owner, and tokens of its own (`bka_...`, stored as a hash, 90 days by
  default and at most 365, revocable one by one, last use recorded). Its
  permissions are its role's intersected with its owner's on every
  request, never including reading secrets or managing users, roles,
  members or tokens. It cannot sign in or manage agents. Runs it starts
  are attributed to it (`triggered_by.kind` `agent`, with `on_behalf_of`
  when its owner issued the token for themselves) and audit entries carry
  `actor_kind`, `owner_id`, `on_behalf_of` and `token_name`. Managed with
  `/api/agents` and `brokoli agents`; agents are not listed among users.
  Over MCP the agent is told who it acts as, and is warned (as is
  `brokoli mcp`) when it uses the server's static API key. See
  `docs/agents.md`.

- **Brokoli for AI agents over MCP** (ADR-046 phase 1) -- @hc12r.
  `brokoli mcp` serves the Model Context Protocol on stdio for an agent
  client on your machine, acting on a server with the credential `brokoli
  login` stored (or `--api-key` / `BROKOLI_TOKEN`); `POST /api/mcp` serves
  it over Streamable HTTP (stateless, JSON) from the server. Eight read-only
  tools: `list_pipelines`, `get_pipeline`, `validate_pipeline`,
  `get_lineage`, `list_runs`, `get_run`, `get_run_logs` and `explain_run`,
  which answers why a run failed in one result (the failing nodes with
  their errors and last log lines, the run's error lines, the last
  successful run before it and the pipeline versions saved since). Every
  tool is a call to the REST API as the caller, through the same router
  and permission checks, so an agent sees what its account sees; tools
  the caller lacks the permission for are not listed. Every result is
  redacted (credentials by name, URL passwords, bearer and basic
  credentials in text) and bounded (pages of at most 100, at most 1,000
  log lines, long text cut). See `docs/mcp.md`.

- **Signed webhook paths a distribution can register** -- @hc12r.
  `api.RegisterSignedWebhookPath(path)` exempts one exact POST path from
  session and API-key authentication, for a route whose handler verifies
  the request itself (a webhook signed with a shared secret). Matched like
  the built-in webhook exemptions: the exact string the router dispatches
  on, so no prefix, trailing segment, encoded separator or other method
  passes.

### Changed

- **`pipeline_id` is unique within a workspace, not an organization**
  -- @hc12r. Two workspaces of one organization can each have an
  `orders`, so a staging workspace can hold the same pipelines as
  production. The per-workspace index is created before the
  per-organization one is dropped, so uniqueness never lapses, and
  existing data is valid under it. `GetPipelineByPipelineIDInWorkspace`
  is the lookup to use; `GetPipelineByPipelineIDInOrg` now answers
  `ErrAmbiguousPipelineID` when more than one workspace has the id,
  rather than returning either. The git push route looks in the caller's
  workspace first.

### Added

- **Queued jobs that are not pipeline runs** -- @hc12r. `RunJob` has a
  `Kind`; empty is a pipeline run, as every job was before. A distribution
  registers a handler per kind in `Registry.JobHandlers`, and a worker
  runs a job of that kind with it, in a worker slot, renewing its claim
  as a run does: acknowledged when the handler returns nil, returned to
  the queue when it returns an error. A job whose kind the worker has no
  handler for goes back to the queue for one that does.

### Fixed

- **A run exists when its id is returned** -- @hc12r. On the in-process
  path, `POST /api/pipelines/{id}/run` answered 202 with the run's id
  before the run was recorded: the runner wrote it from its goroutine, so
  a client asking for the run at once (an MCP client's `trigger_run`
  waiting for the outcome, say) could get 404. The run is now recorded
  before the id is handed out, as the job-queue path already did.

- **An agent's name is unique where it is seen, not across the server**
  -- @hc12r. Agent names were usernames, unique server-wide, so a
  distribution with organizations refused a second organization's
  `triage-bot` ("an account named triage-bot exists already"), telling it
  that another tenant had one. A new agent's name is now its display name,
  unique within a scope a distribution sets (`AgentHooks.Scope`; the whole
  server by default, where people's usernames are taken too), and its
  username is an internal `agent-<id>`. Existing agents keep their names.
  An agent refused by `AgentHooks.Created` is deleted instead of disabled,
  so its name is not lost for good.

- **A preview read every row of its sources to keep ten** -- @hc12r. The
  editor's Preview and a change request's test held a whole source in the
  server's memory: previewing a pipeline over millions of rows took as
  long as a run and could exhaust the server. Sources now stop at the
  rows a preview keeps: a CSV is read until it has them, a database query
  is cancelled once it has them, a paginated API stops fetching pages, and
  a Python step that `emit()`s stops once it has emitted them. Such a step
  could not be previewed at all before ("emit() requires file-mode
  output"); it now runs as a streamed run does.
- **Preview wrote through output nodes and recorded a run** (#595) --
  @hc12r. The editor's Preview (`POST /api/pipelines/{id}/dry-run`) ran
  the pipeline as a run: a `sink_file` overwrote its file with the
  preview's rows, other outputs and notifications acted, hooks were
  called, and a run was left in the pipeline's history. A preview now
  runs in a scratch store that is thrown away, holds back every step that
  may act outside Brokoli (outputs, `notify`, `migrate`, `bash`, `dbt`,
  `task`, `wait`, and nodes declaring the `sink` capability) and what
  follows it, reporting them as `held` and `not_run` with the reason, and
  calls no hook. Steps run in the server's own process, and a preview
  stops after 90 seconds.
- **A worker waits out an unreachable native driver catalog instead of
  exiting** (#869) -- @hc12r. Installing the managed driver set tried each
  download once, so a DNS or network blip while a worker started made it
  exit and restart. A failure to reach the catalog is now retried with
  backoff (2 seconds, doubling to 30) until the install's own deadline;
  a digest mismatch or a release not in the catalog still stops it at
  once, and a worker missing a declared driver still takes no jobs.

- **Pipelines built from code are laid out instead of stacked** -- @hc12r.
  The canonical IR a repository deploys leaves node positions out on
  purpose (layout is not a change to the pipeline), so such pipelines
  were stored with every node at the origin and drawn on top of each
  other everywhere but the editor. A pipeline whose nodes all sit at one
  position is now laid out when created or saved
  (`engine.ArrangeStacked`, for distributions that store pipelines
  themselves too), and the runs page's graph lays out one already stored
  that way. Positions no longer count as a graph change, so pausing or
  renaming an old pipeline still skips full validation when only its
  layout moved.

- **A tab opened before an upgrade gets the new version instead of a
  developer error** -- @hc12r. Pages are loaded on demand, and a tab
  opened before an upgrade asks for files of the old build. The server
  answered a missing file under `/assets/` with the application page
  (200, `text/html`), so the browser failed with "error loading
  dynamically imported module" and React Router showed its developer
  screen. Missing assets now get a 404 (never cached), and a page that
  fails to render shows `ErrorScreen` from `@brokoli/ui`: for a module of
  an earlier build it reloads once to get the new one (not again within
  30 seconds, so a broken build cannot loop), otherwise it says what
  failed and offers a reload. The community application uses it on every
  route, inside the shell where the shell rendered.

- **The editor draws a pipeline compiled from code laid out, and moving
  nodes no longer leaves a Git-managed pipeline "unsaved"** -- @hc12r. A
  pipeline deployed from code arrives with every node at one position;
  the editor drew them on top of each other, and laying them out to see
  them counted as an edit, so after saving through `gitSave` the editor
  still said "Unsaved changes". Stacked nodes are now laid out as part of
  loading, and for a pipeline saved through `gitSave` (whose layout is
  not saved anywhere) moving or laying out nodes does not count as a
  change.

- **Rollback refuses Git-managed pipelines** (#854) -- @hc12r. Editing a
  pipeline whose source is Git was already refused, but rolling it back to
  an earlier version was not: the pipeline then ran something its
  repository did not say, until the next sync silently undid it. A
  rollback now returns 403 with the same advice as an edit, to revert the
  change in the repository.

- **Instances starting together no longer crash migrating one database**
  (#849, #852) -- @hc12r. On Postgres, the processes of an installation
  that start at the same time all migrated the database at once. A first
  install restarted repeatedly before settling (five processes, fourteen
  restarts in a rehearsal), failing on `duplicate key value violates unique
  constraint "pg_type_typname_nsp_index"` or on two processes seeding the
  same template, and an upgrade adding an index could fail the same way.
  Migrations now run one process at a time under a Postgres advisory lock;
  the others wait, then find everything in place. They need a direct
  connection or session pooling, not a transaction-pooling proxy.
- **Starting before Postgres accepts connections no longer exits** (#852)
  -- @hc12r. A refused connection is retried for up to 60 seconds, so
  processes that start alongside their database wait for it instead of
  restarting. A wrong password or database name still fails at once.
  The wait is exported as `store.WaitForPostgres` (#853), so code that
  opens its own pool at startup waits the same way instead of coming up
  without its feature.

### Security

- **Go 1.26.10** -- @hc12r. GO-2026-6809: crypto/x509 did not distrust
  compromised intermediate certificates, reachable from every outbound
  TLS connection. The toolchain pin, CI and release builds move to the
  fixed release.

## [0.18.17] - 2026-10-17

### Changed

- **`pipeline_id` is unique within an organization** (#846) -- @hc12r.
  It was unique across the whole server, so in a multi-tenant deployment
  one tenant's create was refused for a name another tenant used. The
  unique index is now `(org_id, pipeline_id)`; opening a database creates
  it before dropping the old one. `GetPipelineByPipelineIDInOrg` looks one
  up within an organization. Single-tenant servers behave as before.
- **`conn_id` is unique within a workspace** (#847) -- @hc12r. Same issue
  for connections. Uniqueness is now `(workspace_id, conn_id)`: Postgres
  drops the inline constraint, and SQLite rebuilds an older `connections`
  table once, keeping every row. The store gains `GetConnectionIn` and
  `DeleteConnectionIn`; runs resolve a `conn_id` in their pipeline's
  workspace; connection pool budgets are keyed by workspace and name; the
  connection routes act on the connection in the caller's own workspace.

## [0.18.16] - 2026-10-17

### Security

- **`./uploads` is no longer served without sign-in** (#844) -- @hc12r.
  `GET /uploads/{filename}` served any file in `./uploads` to anyone: the
  route sat outside `/api`, so no authentication ran, and nothing tied a
  file to its owner. Core writes nothing there; the route is removed.

## [0.18.15] - 2026-10-17

### Security

- **Scope checks fail closed and answer alike** (#840) -- @hc12r. Run and
  pipeline routes checked the caller's organization only when the pipeline
  could be read, so a failed read skipped the check, and a missing
  pipeline answered differently from another organization's. They now
  refuse anything short of a pipeline the caller can see, with the same
  404. `POST /api/git/push/{id}` exports only a pipeline the caller's
  organization can see, and `GET /api/git/status` takes `gitsync.view`.
  Dependency lookups for a pipeline with no organization are scoped to the
  caller's organization. Blob capability refusals no longer say whether
  the attempt exists. Finished-run events keep their organization in the
  live state, and the cancel and wait-timeout events carry one.

### Fixed

- **The installer test no longer reports a phantom install** (#842) --
  @hc12r. The harness reused install directories through `$RANDOM`, so a
  refusal check could find an earlier run's binary.

## [0.18.14] - 2026-10-17

### Security

- **No admin creates or resets an account above its own role** (#837) --
  @hc12r. `POST /api/auth/users` accepted role `superadmin` from any
  caller holding role `admin`; only a superadmin may create one now, and
  anyone else gets 403 with no account created. `POST
  /api/auth/admin-reset-password` no longer lets an admin reset a
  superadmin's password, and in multi-tenant mode, with the new
  `UserOrganizationsFunc` hook set, resets only a user whose every
  organization is the caller's current one (it compared primary
  organizations only).
- **Variables and connections are created only in the request's own
  workspace** (#838) -- @hc12r. `POST /api/variables`, `PUT
  /api/variables/{key}` and `POST /api/connections` honoured a
  `workspace_id` from the body as given; a named one must now be the
  workspace the request was authorized for (403 otherwise), and an
  organization member who belongs to no workspace can no longer create
  into `default`. `PUT /api/variables/{key}` also wrote the body's key,
  ignoring the path; the two must match.
- **Server-wide operations ask for permissions of their own** (#839) --
  @hc12r. Plugin install and remove take the new `plugins.manage`, and the
  notification channels (read and write) take the new
  `notifications.manage`, instead of `settings.edit`. The built-in admin
  role holds both; custom roles that relied on `settings.edit` for these
  need the new permission. Also: `POST /api/system/purge` is scoped to the
  caller's organization whenever there is one (an organization whose id
  is `default` purged every tenant's runs); `POST /api/test-connection`
  takes `connections.test`; `POST /api/pipelines/import` always assigns a
  new id and logs store errors instead of returning them; `GET /api/audit`
  and `/api/audit/{id}` take `audit.view`, and the list keeps only the
  caller's organization's entries.

## [0.18.13] - 2026-10-17

### Fixed

- **SQLite connections wait for a busy writer instead of failing** (#835,
  fixes #834) -- @hc12r. A `sqlite://` URI or a bare `.db`/`.sqlite` path
  was opened with SQLite's default busy timeout of 0, so a writer that
  found the file locked failed at once with `database is locked
  (SQLITE_BUSY)`. A partitioned migrate into one SQLite file, whose ranges
  write in parallel, failed this way on most runs (4 of 10 succeeded at
  `max_parallel` 6). Both forms now get `_pragma=busy_timeout(5000)`, the
  value the metadata store already uses, unless the URI sets its own
  `busy_timeout`. Nothing else about the database is changed.

## [0.18.12] - 2026-10-14

### Changed

- **Template curation asks for `templates.manage`** (#832) -- @hc12r.
  Creating, changing and deleting pipeline templates was gated on the
  role claim being `admin` or `superadmin` and nothing else. Templates
  are shared by the whole instance, so these routes now go through the
  same strict permission gate as plugin install, system purge and native
  drivers. Without a permission extension nothing changes: only `admin`
  and `superadmin` may curate templates. With one, the extension decides
  who holds `templates.manage`; a role named `admin` is no longer enough
  on its own. Listing templates still needs no permission.

## [0.18.11] - 2026-10-14

### Changed

- **`RequestIdentityFunc` can resolve a request's permissions** (#830) --
  @hc12r. The hook now returns an `IdentityRefinement`: the claims and a
  refusal as before, and optionally the request's permissions. When it
  resolves them, `PermissionChecker.HasPermission`, the open-source
  permission gate and `/api/auth/me/permissions` read them instead of
  looking the role claim up in the roles table, whose one namespace for
  the whole instance had no room for roles defined per organization. An
  empty, non-nil list grants nothing; with none, the role claim decides
  as before. `api.RequestPermissions` exposes the list. The hook's
  signature changes; it was added in 0.18.10.

## [0.18.10] - 2026-10-13

### Added

- **`RequestIdentityFunc`** (#827) -- @hc12r. An embedding application
  can refine the identity of every request that carries one, after its
  organization and workspace are resolved and before any handler runs:
  return the claims the rest of the request sees, or refuse it with a
  status and message. Every permission check and handler reads the
  returned claims, so memberships kept by the application take effect on
  the next request instead of when the token expires. Unset, nothing
  changes; anonymous and open-mode requests never reach it.

### Fixed

- **API tokens on Postgres** (#828) -- @hc12r. `GetAPITokenByHash`
  scanned the nullable `last_used_at` into a `time.Time`, and nothing sets
  that column, so on Postgres every API token was refused with
  `401 invalid token`. SQLite was unaffected.

## [0.18.9] - 2026-10-11

### Added

- **Managed native drivers** (#825) -- @hc12r. `BROKOLI_MANAGED_DRIVERS`
  declares the driver set by exact identity (name, version, library
  sha256). A deployment that sets it is managed: `GET /api/drivers` and
  `/api/drivers/catalog` list the declared set whatever the answering
  process has installed, install and remove through the API return `403`
  for every caller, and `serve --mode worker` installs every declared
  build it lacks from `BROKOLI_DRIVER_INDEX` at startup, checks each
  library against its declared digest, and does not start without them.
  Any non-empty value is managed, even one that does not parse.
  `/api/capabilities` reports `native_adbc_drivers.managed`; the Drivers
  page then lists the provided drivers with nothing to install or remove.
  `Manager.EnsureManaged` and `InstallFromCatalogAt` take the index URL
  explicitly.

## [0.18.8] - 2026-10-11

### Added

- **Park before dispatch** (#822) -- @hc12r.
  `Engine.ParkBeforeDispatch(run, pipe, params)` checks a pipeline's wait
  nodes that have nothing upstream (`file_exists` excepted) on the
  dispatching process, before the run is handed to another, and parks the
  run on the first unmet one, so it holds no slot anywhere while it waits.
  Config is resolved as a runner resolves it; a failing check fails the
  run, as a runner's first check does.
- **`Engine.WakeHandoff`** (#822) -- @hc12r. Offered every run the
  parked-wait watcher wakes, after the claim: taking it means it does not
  run here, declining lets it run here as before, an error fails it.

## [0.18.7] - 2026-10-11

### Added

- **Waits can wait in place where a run cannot park** (#821) -- @hc12r.
  `Engine.WaitInPlace` makes an unmet wait keep checking its condition in
  the process running it, every `poll_interval`, until it is met, the run is
  cancelled or `timeout` passes, ending with the parked wait's timeout
  message. It is for a process that executes runs for another process's
  store, which cannot park a run; the run keeps its slot while it waits.
  Off by default. A wait node's attempt timeout covers the wait's own
  `timeout` in this mode, and a retried wait keeps its first deadline.
- **`PipelineWaitChecker`** (#821) -- @hc12r. A store that cannot look
  pipelines up can answer a `pipeline` wait itself; "pipeline not found"
  still covers both a missing pipeline and another organization's.
- **`RunOptions.RunID`** (#821) -- @hc12r. Creates the run with a given
  ID, so a process running a run another process created can cancel it by
  that ID.

## [0.18.6] - 2026-10-10

### Security

- **Bash node limits hold for every process the command starts** (#819,
  closes #818) -- @hc12r. The CPU, file-size and open-files limits, and the
  address-space limit a node asks for without a cgroup, were set on the
  shell after it had started. A command that forks at once, as any pipeline
  does, could start children before they landed, and those children ran
  with no limits (a CI run allocated 300 MB under a 100 MiB cap). The shell
  now sets the limits on itself before the command runs, clamped to the
  server's own limits as before; a limit it cannot set stops the node and
  says so. Linux only, as before: other platforms apply none of these
  limits.

## [0.18.5] - 2026-10-10

### Fixed

- **A node stops with the reason when its connection's credentials cannot
  be resolved by the store** (#816) -- @hc12r. A store that resolves
  connections on the caller's behalf can find a connection and fail to
  resolve its credentials (for example an `env://` reference not allowed
  where it is resolved). The resolver read that as a missing connection,
  logged a warning, and ran the node without credentials, so it failed
  later with "source_db node requires 'uri' config". A store now marks the
  case with `store.ErrCredentialsUnresolved`, and the node fails with the
  store's reason.
- **A `pipeline` wait says when it cannot be checked** (#816) -- @hc12r.
  Where the store cannot look pipelines up, the wait answered "pipeline not
  found" since v0.18.1, for an upstream that existed. It now says the
  process cannot check the condition. A missing pipeline, or one in another
  organization, still reads "not found".

## [0.18.4] - 2026-10-10

### Security

- **excelize past six more HIGH advisories** (#814) -- @hc12r.
  CVE-2026-107211 to -107216, all reachable from a workbook a pipeline
  reads: an unchecked pivot-cache field index, an unbounded row number in
  `Rows.Columns`, a nil dereference in `GetSlicers`, panics in `Decrypt` on
  a malformed OLE container, an attacker-sized allocation in `extractPart`,
  and mutually-referencing array formulas. No release contains the fixes
  yet; this pins upstream commit `6258dce`. Go's vulnerability database
  does not list them yet, so `govulncheck` does not report them.

## [0.18.3] - 2026-10-10

### Security

- **Go 1.26.9 and `golang.org/x/net` v0.60.0** (#812) -- @hc12r. Thirteen
  standard-library advisories (GO-2026-6599 to GO-2026-6617) in the
  net/http HTTP/1 and HTTP/2 server and client, html/template, crypto/tls,
  net/textproto and os, five of them also in `golang.org/x/net`. Release
  binaries and images are built with the fixed toolchain. The vulnerability
  allowlist is empty: the excelize advisory it waived is no longer reported.

### Fixed

- **Legacy `.xls` workbooks are refused with a clear message** (#811) --
  @hc12r. The Excel reader handles `.xlsx` only; a binary `.xls`
  (Excel 97-2003) or password-protected workbook used to fail with
  "unsupported workbook file format". It is now recognised by its signature
  and refused with a message to save it as `.xlsx`. A file that is `.xlsx`
  inside but named `.xls` still reads.
- **File extensions match without case** (#811) -- @hc12r. `DATA.CSV` or
  `REPORT.XLSX` failed as unsupported on the batch path while the streaming
  path accepted them, so the same file ran or failed depending on whether
  the run streamed. An unsupported or missing extension now names the
  supported formats.
- **Cancelling a run right after it parks** (#811) -- @hc12r. A cancel sent
  in the moment between a run parking at a wait node and its runner
  unregistering was reported as delivered while the run stayed `waiting`;
  a cancel in the same moment after a run finished reported success. Both
  now take the store-side path: the waiting run is cancelled, the finished
  one reports that it already completed.

## [0.18.2] - 2026-10-08

### Security

- **excelize past three HIGH advisories** (#809) -- @hc12r. Three
  advisories were published on 2026-10-08 against
  `github.com/xuri/excelize/v2` v2.11.0, which the Excel loader reads
  workbooks with: an integer overflow in column-name parsing
  (GHSA-c85p-xxjj-2r75), an unbounded spin count when decrypting an
  agile-encrypted workbook, and an unbounded `<col max>` attribute, all
  reachable from a workbook a pipeline reads. No release contains the fixes
  yet; this pins upstream commit `696050f`, which has all three.

## [0.18.1] - 2026-10-08

### Fixed

- **A `pipeline` wait stays inside its organization** (#807) -- @hc12r.
  The target pipeline must belong to the waiting run's organization;
  otherwise the wait fails with "pipeline not found", as for a pipeline
  that does not exist. It used to report the latest run status of any
  pipeline by ID.
- **A `file_exists` wait is held to the data directories** (#807) --
  @hc12r, as every file node is (`BROKOLI_DATA_DIRS`). It used to check
  any path on the worker.
- **A waiting run can be cancelled** (#807) -- @hc12r. A run parked at a
  wait node used to stay waiting until it woke or timed out: the cancel API
  answered 404, or 200 with no effect where cancels are broadcast. It is
  now cancelled and its park removed; if it woke at the same moment, the
  cancel is applied to it as a running run.

### For code embedding the engine

- `store.WaitingRunCanceller` is a new optional store capability
  (`CancelWaitingRun`); the built-in SQLite and Postgres stores implement
  it. A store without it keeps the previous behaviour.

## [0.18.0] - 2026-10-07

MySQL and ClickHouse connections can read through native ADBC drivers, and
a driver says which connection types can use it.

### Added

- **MySQL and ClickHouse connections can be pinned to a native driver**
  (#805) -- @hc12r. A `source_db` node reading such a connection runs
  through the pinned build in the isolated worker, as Flight SQL,
  PostgreSQL and SQLite already do.
  - MySQL uses the `mysql` driver: `mysql://host:port/schema`, with the
    connection's driver options as query parameters and credentials passed
    as options, never in the URI.
  - ClickHouse uses the `clickhouse` driver, which speaks ClickHouse's HTTP
    interface: `http://host:8123/`, or `https://host:8443/` when the
    connection sets `"secure": true`. A pin on the native-protocol ports
    9000/9440 is refused, saying why.
  See `docs/native-drivers.md`.
- **Which connection types can use a driver** (#805) -- @hc12r. The
  driver APIs report `usable_by`, and saving a connection pinned to a driver
  that does not serve its type is refused (the run checks again). The
  Drivers page shows it, and no longer offers to install a driver no
  connection can use yet; the connection form chooses drivers by it.

### Fixed

- **Arrow decimals other than 128-bit decode** (#805) -- @hc12r. Only
  `decimal128` was read; a `decimal32`, `decimal64` or `decimal256` column,
  which the MySQL driver returns for `DECIMAL(10,2)`, failed the run after
  its rows were read. All four widths decode exactly.

## [0.17.2] - 2026-10-07

### Fixed

- **A native source no longer warns that it has no driver** (#803) --
  @hc12r. A `source_db` node reading a connection through a native ADBC
  driver logged, on every successful run, that its connection type "has no
  database driver in this build" and that "the run will fail". Such a
  connection is read through its pinned driver; the warning is now only
  given for a type that genuinely has none.

## [0.17.1] - 2026-10-06

### Fixed

- **System roles now follow the code on every start** (#801) -- @hc12r.
  The admin, editor, operator and viewer roles were written to the database
  only on the first run, so an existing deployment kept the permissions of
  the version it was installed with. A permission added since --
  `secret_stores.manage` (v0.17.0) or `drivers.manage` -- was held by no
  stored role: a permission check reading the stored role refused it to
  admins, and the UI hid the matching controls. Every start now adds any
  missing system role and brings each one's permissions to the code's
  definition. Custom roles are untouched.

## [0.17.0] - 2026-10-06

Native ADBC drivers: a `source_db` node can read through a native driver --
Flight SQL always, PostgreSQL and SQLite when a connection is pinned to one --
inside an isolated, short-lived worker process. Partitioned `migrate` runs key
ranges in parallel. Secret store changes have their own permission. **Two
changes need attention before upgrading**; they are listed first.

### Changed -- read before upgrading

- **Changing a secret store takes `secret_stores.manage`** (#792) -- @hc12r.
  Creating, editing and deleting stores used to take the connection
  permissions; viewing and testing still do. The built-in editor and admin
  roles hold it, so nothing changes for them; a custom role that managed
  stores needs it added.
- **New permission `drivers.manage`** (#799) -- @hc12r. Installing and
  removing native drivers takes it, and only the built-in admin role holds
  it: a driver is native code every worker on the host loads, for every
  workspace. A distribution with tenants should map it to its platform
  operators, never to a tenant's administrators.

### Added

- **Native ADBC drivers** (#799) -- @hc12r. Driver builds are installed
  into the driver directory (`BROKOLI_DRIVER_DIR`) from an archive and its
  digest (`brokoli drivers install <archive> --sha256 ...`) or from a
  curated catalog (`brokoli drivers install <name> --catalog`, or the new
  Drivers page). A connection pins one exact build by its library digest
  (`driver_identity`); a run reads through that build in a fresh
  `worker task` child that looks the identity up and verifies the library
  immediately before loading it, then streams Arrow straight into the
  artifact store. Works on the streaming path, the batch path and dry
  runs; Test runs `SELECT 1` through the pinned driver. New connection type
  `flightsql`. Runs are routed by `native-adbc:*` capability tags to
  workers that have the build. See `docs/native-drivers.md`.
  - Loading a driver needs a binary built with cgo and `-tags adbc`. **The
    standard release binaries cannot run native drivers**; they can manage
    drivers and pinned connections, and say so where it matters.
  - There is **no default catalog**: catalog installs are available once an
    operator sets `BROKOLI_DRIVER_INDEX`, since the catalog is not signed.
  - The worker gets an allowlisted environment, never the server's
    (`BROKOLI_NATIVE_PASS_ENV` widens it), and limits itself with
    `BROKOLI_NATIVE_MEMORY_MB`, `BROKOLI_NATIVE_CPU_SECONDS` and
    `BROKOLI_NATIVE_OPEN_FILES`. A Flight SQL host is checked against the
    outbound policy before the driver is given it.
  - Run parameters (`${param.x}`) in a native source's query are refused
    for now rather than sent as literal text.
- **Partitioned `migrate`** (#793) -- @hc12r. A `partition` setting
  (`column`, `strategy` numeric or date, `boundaries`, `max_parallel`)
  splits a migrate into key ranges run in parallel, each with durable
  status. It requires `mode: upsert` with the partition column among
  `key_columns`, so a retried partition cannot duplicate rows. Postgres to
  Postgres on the same endpoint merges server-side through a staging table;
  across databases it streams in bounded batches. Measured at about 2.25x
  (same endpoint, 8 partitions) and 2.47x (separate databases, 4
  partitions) a single stream.
- ADR-044 (Arrow transport and a composable runtime) and ADR-045 (optional
  native worker capabilities), both proposed (#796, #797) -- @hc12r.

### Fixed

- **A secret variable read without an encryption key is still reported
  secret** (#792) -- @hc12r. A worker without the key returned the stored
  value flagged as a plain variable, so it could reach logs unmasked.

### For code embedding the engine

- `extensions.CapabilityJobQueue.DequeueForCapabilities(advertised func() []string)`
  is a new optional queue capability; the worker reads its capabilities
  each time a job is considered. The in-memory queue implements it.
- `extensions.WorkOrder.Migrate` (`MigrateWorkOrder`) carries one migrate
  partition, as connection references, never credentials (#793).
- `Engine.NativeADBCWorker` runs native sources; `NativeWorkerLauncher` is
  the isolated implementation. An embedder whose binary does not register
  core's `worker task` command sets its `Executable`.
- `codeexec.AllowlistedEnv(passVar, extra...)` is the environment filter
  code nodes and the native worker share.
- `models.DriverIdentity` and `models.Connection.DriverIdentity` are new;
  connections gained a `driver_identity` column (added automatically).

## [0.16.0] - 2026-10-03

Secret stores (ADR-041): a workspace can keep its credentials in its own
secret manager (HashiCorp Vault, OpenBao, AWS Secrets Manager or SSM), and a
connection refers to them with `secret://<store>/<path>#<field>`. The value is
fetched where the node runs and never written down. Resolved credentials are
now masked in everything a run records. **One change needs attention before
upgrading**; it is listed first.

### Changed -- read before upgrading

- **Credential references are checked when a connection is saved** (#783,
  closes #781) -- @hc12r. `password_ref` and `extra_ref` used to be accepted
  whatever they held, and a typo surfaced as a failed run. Saving now refuses,
  with a 400 naming the field: an unknown scheme, a malformed `vault://` or
  `k8s://` body, an `env://` name that is one of the server's own secrets, a
  client-made `encrypted://` value, and a `secret://` reference to a store
  that does not exist in the connection's workspace or whose `#field` does not
  suit the provider. Nothing is fetched on save, and the operator allowlists
  are still enforced where a reference resolves, not on save. A reference
  already stored is not re-checked.
- For code embedding the engine: `secrets.Resolver.Resolve` takes a
  `secrets.Scope` (#784). `Chain.Resolve(ctx, ref)` keeps its signature;
  `Chain.ResolveIn(ctx, scope, ref)` is new.

### Added

- **Secret stores** (#787, #788) -- @hc12r. A store is a named,
  workspace-scoped connection to a secret manager, managed under
  `/api/secret-stores` (create, edit, delete, test) and on a new Secret
  stores page. A connection's password, its whole extra document, or any
  single value inside extra can be a `secret://` reference; the connection
  form offers "From a secret store" for the password. Stores authenticate
  with the machine's own identity (`ambient`, refused where
  `BROKOLI_SECRET_STORE_AMBIENT=deny`), a short-lived OIDC token naming the
  store, run and node (`oidc`), or a stored token (`token`, encrypted, never
  returned). Values are cached for one run and dropped when it ends; a store
  test reports the version and field names, never the value; a store a
  connection refers to cannot be deleted or renamed. A distribution can add
  providers through `extensions.Registry.SecretStoreProviders`. See
  `docs/secret-stores.md`.
- **`vault` provider** for HashiCorp Vault and OpenBao (#789) -- @hc12r.
  KV v2 with versions; token, JWT/OIDC and Kubernetes login; https required
  except for a loopback dev server; redirects kept on the configured server;
  session tokens revoked after use. See `docs/secret-store-vault.md`.
- **`aws_secrets_manager` and `aws_ssm` providers** (#790) -- @hc12r.
  Secrets Manager secrets are single values or, when JSON objects, named
  fields; SSM parameters are decrypted single values. OIDC uses
  `AssumeRoleWithWebIdentity` with a session named after the run;
  `ambient` uses the machine's credential chain. A store cannot set its own
  endpoint. See `docs/secret-store-aws.md`.

### Security

- **Resolved credentials are redacted from what a run records** (#784,
  closes #782) -- @hc12r. A connection's password and the credential-like
  values of its extra settings (keys, tokens, auth headers, with the token
  after a `Bearer` scheme on its own) are masked as `[redacted]` in node
  logs, node and run errors, events, alerts and notifications, the DLQ and
  recorded SQL. Before this, a target that echoed a credential back put it
  verbatim in the run's error and every log line. Values under 8 bytes are
  not masked. Remote paginated `source_api` pages mask their own errors.
- **The connection API says which references it shows** (#783, closes
  #755) -- @hc12r. `encrypted://` stays masked; `env://`, `vault://`,
  `k8s://` and `secret://` references are returned, as the location of a
  credential a user needs to see to edit it. The code comments now agree.

### Docs

- ADR-041 is accepted (#785).

## [0.15.0] - 2026-10-02

Four new connectors -- Google Cloud Storage for file nodes, and
Snowflake, Oracle and Databricks for reading -- and a Bash operator
node. It also closes two ways a role that may only start runs could
change what a pipeline executes. **Two changes need attention before
upgrading**; they are listed first.

### Changed -- read before upgrading

- **Run parameters are bound in source SQL, not written into it** (#778,
  closes #774) -- @hc12r. A `${param.*}` reference in a `source_db`
  query or a `migrate` `source_query` was pasted into the SQL text, so
  anyone allowed to start a run (`pipelines.run`, which the built-in
  operator role has without `pipelines.edit`) could change the SQL it
  executed. It is now replaced by the driver's placeholder and the value
  passed separately. `'${param.x}'` binds as text and a bare
  `${param.x}` as a number. **These now fail, with a message saying
  what to write instead:** a reference inside a longer literal
  (`LIKE '%${param.x}%'`, refused at validation), one used as a table or
  column name, and a bare one whose value is not a number. A query that
  uses a run parameter is no longer pushed down into a same-server
  write. `${var.*}`, `${interval.*}` and `${run.*}` are unchanged.
- **Code-node scripts no longer substitute `${param.*}`** (#775) --
  @hc12r. The same escalation in Python and TypeScript source: a run
  parameter could change the code a node ran. Scripts read parameters
  from `params` (`params["x"]`, `params.x`), as they always could. A
  script that contains `${param.x}` now fails validation with that
  advice. Other references still substitute.
- For code embedding the engine: `QueryDatabase`, `QueryBigQuery` and
  `DryRunBigQuery` take trailing bind arguments (#778); existing calls
  compile unchanged.

### Added

- **Google Cloud Storage for file nodes** (#772) -- @hc12r.
  `source_file` and `sink_file` read and write objects in a `gcs`
  connection's bucket, authenticating with OIDC workload identity
  federation, a service-account key, or the machine's identity where
  allowed. A failed write leaves no object behind, downloads are capped
  and pinned to the measured generation, and the API endpoint cannot be
  set from connection data. See `docs/gcs-file-delivery.md`.
- **Snowflake, read-only** (#769) -- @hc12r. Queries through a guarded
  driver: only five URI options are accepted, password authentication
  only, `PUT`/`GET` file transfer refused, the native minicore library
  never loaded (`-tags minicore_disabled` plus `SF_DISABLE_MINICORE`),
  and errors never repeat the password. Writes are refused by name. See
  `docs/snowflake.md`.
- **Oracle, read-only** (#770) -- @hc12r. Through the pure-Go `go-ora`
  driver: numbers decode exactly, passwords are redacted from driver
  errors, and writes are refused by name. Tested against a real Oracle
  Free server in CI. See `docs/oracle.md`.
- **Databricks SQL, read-only** (#771) -- @hc12r. Through a guarded
  driver that validates the host and HTTP path, never repeats the token,
  and sends every request through the outbound network policy. Writes
  are refused by name. See `docs/databricks.md`.
- **Bash operator node** (#773) -- @hc12r. Runs a shell command on the
  worker and passes its input through. It runs in its own process group,
  so cancellation stops everything it started; output is logged with
  bounds; values reach the command only through `env`, never by
  substitution into the command. Trusted-worker execution, like a code
  node. See `docs/bash-operator.md`.
- **Memory limit for bash nodes** (#777) -- @hc12r. Enforced through a
  cgroup v2 leaf per run under `BROKOLI_BASH_CGROUP`, which counts real
  memory so a JVM or Node started from the command works; an address
  space limit only when the node sets `max_memory_mb`; otherwise the log
  says the limit is not enforced. On Kubernetes the pod's memory limit
  remains the ceiling.
- **`BROKOLI_DISABLED_NODE_TYPES`** (#775) -- @hc12r. Refuses node types
  such as `bash,code,task` at validation and again on the worker, and
  lists them in `GET /api/capabilities` as `disabled_node_types`. The
  editor leaves them out of the node palette and marks existing nodes of
  a refused type (#776). See `docs/code-execution-policy.md`.

### Fixed

- **The editor's file-node Location field offers Cloud Storage** (#772)
  and the source-database forms offer Snowflake, Oracle and Databricks
  as sources only (#769, #770, #771) -- @hc12r.

## [0.14.0] - 2026-09-28

Three new backends -- Azure Blob Storage for file nodes, native bulk
writes to SQL Server, and BigQuery -- and short-lived OIDC credentials
for reaching a customer's cloud without a stored key. It also closes
four ways a credential could be used wrongly or leak. **Three changes
need attention before upgrading**; they are listed first.

### Changed -- read before upgrading

- **The server refuses to start without a valid encryption key** (#757)
  -- @hc12r. When `BROKOLI_ENCRYPTION_KEY` was set but invalid (not
  base64, or not 32 bytes), the server logged one warning and ran with a
  key of 32 zero bytes: credentials saved under the real key stopped
  decrypting, and new ones were encrypted under a key anyone can
  reproduce. A key file that existed but was unreadable or short was
  silently replaced with a new key, losing every stored credential. Each
  is now a startup error naming what to fix. **Before upgrading, grep
  the server logs for `could not load encryption key`**: a deployment
  that logs it has been running on the zero key and will not start until
  the key is fixed. Key files longer than 32 bytes are still read as
  their first 32 bytes, as before.
- **A credential reference that cannot be resolved fails the node**
  (#758) -- @hc12r. A `password_ref` or `extra_ref` (`env://`,
  `vault://`, `k8s://`, `encrypted://`) that failed to resolve was logged
  to the server and skipped, and the node ran with an empty password, or
  with the ciphertext as the password. The run log now says which
  reference failed and why, and the node stops before it runs. Runs that
  were silently proceeding on an empty credential will now fail at the
  step that caused it.
- **Remote `source_api` pages carry the connection by reference** (#760)
  -- @hc12r. A paginated page dispatched to a worker used to carry the
  connection's password and auth headers in its work order. It now
  carries the connection's ID and the pipeline's workspace, and the
  worker resolves the connection itself. **Workers from before this
  release fail those pages** with "source URL is required" (never an
  unauthenticated request), so upgrade workers with the server. Code
  that runs work orders directly should call
  `ExecuteInstanceWorkOrderResolving` / `ExecuteInstanceJobResolving`
  with a connection resolver.
- For code embedding the engine: `ConnectionResolver.Resolve`,
  `ResolveIn`, `ResolveWithWarnings` and `ResolveWithWarningsIn` gain an
  `error` return (#758).

### Added

- **Azure Blob Storage for `source_file` and `sink_file`** (#750, #687)
  -- @hc12r. Through an `azure_blob` connection, with an account key or
  a SAS token, on the batch and streaming paths. A failed write leaves
  nothing behind, and the connection test authenticates for real. Every
  remote file location now sits behind one transport interface, so the
  next one is a single file. See `docs/azure-blob-file-delivery.md`.
- **SQL Server bulk writes** (#761, #681) -- @hc12r. `sink_db` and
  `migrate` append and overwrite through SQL Server's native TDS bulk
  copy: a million rows in about 12 seconds instead of about 3 minutes.
  Columns are matched under the database's collation, as statements
  are. Upsert is refused by name. Test connection uses the compiled
  driver.
- **BigQuery** (#762, #764, #765, #766, #767, #685) -- @hc12r. A native
  backend (ADR-042): `source_db` queries as query jobs, `sink_db` append
  and overwrite as load jobs, which are free and atomic per job. Every
  query is capped at 10 GiB billed by default, columns are discovered by
  a free dry run, and jobs are labelled with the run. Authentication by
  service-account key, by the machine's own identity, or by OIDC
  workload identity federation with no stored key; an optional
  `billing_project` pays for the jobs. Upsert is refused by name. See
  `docs/bigquery.md`.
- **OIDC token sources and Google workload identity federation** (#763)
  -- @hc12r. `BROKOLI_OIDC_TOKEN_FILES` serves tokens a platform writes
  to disk, such as Kubernetes projected service-account tokens.
  `BROKOLI_SECRET_STORE_AMBIENT=deny` switches off the machine's own
  identity on servers that run pipelines for several teams; tokens read
  from disk are that identity and follow the switch. See
  `docs/workload-identity.md`.
- **Test connection resolves credential references** (#759) -- @hc12r,
  through the same path a run uses. A connection whose credentials come
  from `env://`, `vault://` or `k8s://` used to be tested with none. The
  result notes when references were resolved on the server rather than
  where runs resolve them.
- **ADR-041, external secret stores** (#756) and **ADR-042, native
  BigQuery backend** (#762) -- @hc12r.

### Fixed

- **File nodes could only pick SFTP connections** (#750) -- @hc12r. The
  Location field offered SFTP alone, so an S3 connection was never
  selectable in the UI though the engine supported it.
- **SQL Server statement writes over 1,000 rows** (#761) -- @hc12r. SQL
  Server refuses an `INSERT` with more than 1,000 row values; batches are
  now capped there.

## [0.13.1] - 2026-09-26

Fixes two defects in 0.13.0, both found by validating 0.13.0 live on a
cluster before deploying it. Upgrade if you run 0.13.0.

### Fixed

- **Numeric contract rules gave wrong answers on integer data** (#748,
  Tnsor-Labs/actually-fine#8) -- @hc12r. `range`, `type: number` and
  `type: integer` breached every value that was not a 64-bit float,
  reporting "value is outside the allowed range" for 3 in [0, 5]. That is
  every integer column from `source_db`, and any spilled or streamed
  data, where whole numbers come back as 64-bit integers to stay exact.
  Under `quarantine` the rows were dropped without explanation, and the
  same pipeline cleared different rows depending on whether its data had
  been spilled. The contract engine now treats every numeric type as a
  number and decides `integer` on the value.
- **Unrunnable transform rules are now actually refused on save** (#747)
  -- @hc12r. 0.13.0 described this and did not do it: the check was wired
  into the per-node detail validation, not the path saving a pipeline
  takes. **The behaviour change listed under 0.13.0 takes effect in this
  release.** The same split ran the other way for contracts, which were
  refused on save but not flagged in the per-node view; both checks now
  run on both paths, and a test asserts the two validators agree.

## [0.13.0] - 2026-09-26

### Added

- **`contract_gate` node** (#741, #742) -- @hc12r. Validates rows against
  a versioned data contract from
  [actually-fine](https://github.com/Tnsor-Labs/actually-fine), evaluating
  the contract's own IR rather than translating it into the older
  `quality_check` rule model. Cleared rows pass downstream, quarantined
  rows are excluded, and a `reject` or `halt` breach fails the node after
  its evidence has been written to the run log. The gate **streams**: it
  is stream-eligible like a transform, so a `source_db -> contract_gate ->
  sink_db` pipeline keeps flowing by reference and never materialises the
  dataset. The contract engine checks record by record and a halt stops
  at the record that caused it. See `docs/contract-gate.md`.
- **Guided contract editor in the UI** (#744) -- @hc12r. Contract Gate
  appears under Processing in the Node Library with a form for contract
  metadata and rules, predicate-specific fields for all nine predicates,
  and optional JSON paste or `.json` upload. A rule's kind (`record` or
  `stream`) is derived from its predicate rather than chosen, because
  every predicate mandates exactly one and the other is always refused.
- **`run.lineage_incomplete` run event** (#734) -- @hc12r. Recorded when
  a lineage profile or provenance write does not land, naming which half
  of the record is missing and why. Informational: it never changes a
  run's status.
- **Node.js runtime documentation and a TypeScript example** (#520) --
  @dvd233.

### Changed

These can change what an existing deployment does.

- **Transform rules are validated when a pipeline is saved** (#740) --
  @hc12r. Thirteen requirements that were enforced only at execution --
  `sort` without `columns`, `aggregate` without `group_by`, `add_column`
  without a name or expression, `project` without projections, and so on
  -- are now save-time **errors**, as are unknown rule types and rules
  with no type. **A pipeline carrying such a rule, which previously saved
  and then failed on the last step of every run, will now be refused on
  save.** Nothing that could ever have run is affected: each of these is
  wrong under any input. Checks that depend on the data, such as whether
  a named column exists, remain at execution time.
- **Contract gate contracts are validated when a pipeline is saved**
  (#744) -- @hc12r. An invalid contract, for example a `unique` predicate
  on a `record` rule, is refused on save rather than failing at run time.
- **Outbound HTTP requests identify as Brokoli** (#739) -- @hc12r. Every
  request a pipeline makes -- `source_api`, `sink_api`, webhooks, wait
  nodes, alerts, notifications -- now sends `User-Agent:
  brokoli/<version> (+https://github.com/Tnsor-Labs/brokoli)` unless the
  node sets its own. Previously they arrived as `Go-http-client/1.1`.
  Operators who filter or allowlist on the agent string will see the new
  value.
- **A recovered run that fails reports its own failure** (#736) --
  @hc12r. A run put back on the queue by startup recovery carries a note
  saying why while it is pending. If it then fails, its error is now the
  failure that ended it, not that note. The note is not lost: it stays in
  the `run.recovery_requeued` event.

### Fixed

- **A failed run persisted with an empty error** (#736) -- @hc12r. Every
  terminal path wrote the reason into the run's terminal event and none
  wrote it onto the run, so `runs.error` stayed empty and the run view
  had nothing to show. Runs executed in-process by the core engine were
  affected on every failure. The stored row now agrees with what the run's
  own events rebuild to.
- **Endpoints rejected pipeline requests with 406** (#739) -- @hc12r.
  Many endpoints refuse the Go default agent outright. Measured against
  one public API, the same request answered 406 as `Go-http-client/1.1`
  and 200 with an identifying agent. The failure surfaced as an opaque
  `HTTP request failed: status code 406` pointing at the user's URL.
- **A run could succeed with its lineage silently missing** (#734) --
  @hc12r. When a store could not write a lineage profile or provenance
  record, the only trace was a debug log line per node, or for provenance
  a line in the process log that was never associated with the run. The
  gap is now logged against the run -- a store that cannot record lineage
  says so on…49942 tokens truncated…ng it.
- **Kubectl argument injection in k8s secret ref resolution** (#259)
  — @hc12r.
- **Static API key auth never actually worked past bootstrap** (#251)
  — @hc12r. Found via local end-to-end SDK testing, not code review:
  `APIKeyAuth` and `JWTAuth` are independent stacked middlewares, and a
  validated static key produced no signal `JWTAuth` could recognize —
  every `Client(api_key=...)` call beyond initial admin bootstrap
  silently required a JWT a static key was never going to produce.
  `APIKeyAuth` now stamps admin-equivalent claims into the request
  context on success, in the same shape a real JWT produces.

### Added

- **Drag-and-drop node placement from the palette to the canvas**
  (#247) — @hc12r.

### Fixed

- **CI hang: drop `--with-deps` from playwright install, add job
  timeouts** (#248) — @hc12r.
- **sodp: broadcast a mutation's delta before acking it, not after**
  (#249) — @hc12r. Ordering bug found while chasing an apparently-flaky
  test; the delta broadcast and the ack were sequenced against spec
  §8.3.

### Changed

- **Apply official brand identity to README and favicon** (#250) —
  @hc12r. Replaces the old placeholder favicon (unrelated shape,
  unrelated colors) with the official Forked-B mark, and recolors
  README badges from an arbitrary blue to the official brand green.

### Docs

- **ADR-021: browser-based login for the CLI and SDK** (`brokoli auth
  login`) (#252) — @hc12r.
- **ADR-022: enforce tenant isolation and outbound-request safety
  structurally** (#255) — @hc12r. Design record for the systemic
  pattern behind the security batch above — status: proposed, since
  the structural store-layer scoping decision is a larger migration
  than the direct fixes already shipped.

## [0.10.52] - 2026-08-18

### Changed

- **`extensions.RunCancelRelay` renamed to `RunCancelBroadcaster`** (#244)
  — @hc12r. Pure rename, no behavior change: `Registry.CancelRelay` →
  `CancelBroadcaster`, `Engine.CancelRelay` → `CancelBroadcaster`. Frees
  "Relay" as a product name ahead of the ecosystem strategy work below —
  this mechanism is unrelated pub/sub run-cancellation, not the proposed
  private-execution-gateway product. `CancelRelayedRun`/`BroadcastCancel`
  keep their names.

### Docs

- **ADR-012, ADR-015, ADR-016, ADR-017 promoted from proposed to
  accepted** (#245) — @hc12r. Each ADR's core decision is shipped and
  load-bearing in production (artifact/dataset plane, physical
  execution plans, plugin packaging/distribution, worker protocol v2
  instance dispatch); each gets a dated Update section documenting what
  shipped and what legitimately remains deferred.

- **ADR-020: adopt the ecosystem framing, Phase 1 portfolio** (#246) —
  @hc12r. New ADR formalizing the decidable slice of the "Brokoli
  Ecosystem" strategy proposal: Phase 1 portfolio (Brokoli/Nodus/Relay/
  Strata), the OSS/Enterprise boundary principle, and the rule that
  productization follows architecture. Status: proposed — an open
  decision, not self-approved.

## [0.10.51] - 2026-08-17

### Added

- **Connector work-unit planning protocol** (#234) — @JefferMarcelino.
  Protocol-layer slice of M3 (#39, ADR-013): a source plugin can now
  optionally break an already-discovered stream into independent work
  units via a new `plan` command (`CmdPlan`/`MsgWorkUnit`), instead of
  one blocking `read` for the whole stream. `NodeTypeDecl.SupportsPlan`
  opts a node type in; `Runner.ReadUnit` threads a planned unit's params
  back through `read` via `ReadParams.Unit`. Wire format and host
  plumbing only — no engine/scheduling wiring yet, and a plugin that
  only implements `read` is completely unaffected.

- **Physical run instances, pre-run plan, and Chronicle** (#239) —
  @hc12r. The run details view now shows Nodus physical instances
  beneath the logical run summary (node, instance key, status, attempt,
  rows, duration), the pre-run physical execution plan, and Chronicle,
  an exploratory view over a run's events.

- **Lineage observed metadata and column mappings** (#242) — @hc12r.
  The lineage graph now carries observed schema and statistics per node
  (row count, column count, per-column null%/unique%/min/max) and
  evidence-backed column mappings across shared assets, sourced from a
  bounded runtime profile persisted after every successful node without
  adding profiling latency to the run itself.

- **Password login can be disabled by an enterprise integration**
  (#241) — @hc12r. `api.PasswordLoginEnabledFunc`, an optional hook
  (nil by default, matching every existing deployment) an EE
  integration can set to require OAuth instead of local passwords.

### Fixed

- **Instance-level artifact writes were not fenced against stale
  attempts** (#240) — @hc12r. ADR-017's instance dispatch wrote a
  physical instance's artifact and only afterward settled the execution
  attempt through fencing, so a worker that stalled past the
  dispatcher's timeout and then finally delivered its result could
  silently overwrite a retry's already-written, correct output.
  `FencedArtifactWriter` guards the write with `(attempt, fencing
  generation)` ordering, extending the same race protection
  `CompleteAttempt`/`FailAttempt` already gave the attempt's status to
  the data underneath it.

## [0.10.45] - 2026-08-16

### Fixed

- **Same-store WorkOrder cancellation** — core workers now observe durable
  run cancellation while executing physical instances, including in-flight
  code processes and source API page requests.

## [0.10.44] - 2026-08-16

### Fixed

- **Remote WorkOrder cancellation** — workers now observe durable run
  cancellation and terminate in-flight code-node processes instead of
  continuing physical instance execution or settling late results.

## [0.10.43] - 2026-08-16

### Added

- **Capability-aware physical placement** (#236) — @hc12r. Placement
  requirements now travel from the run trigger through core WorkOrders and
  JobQueue jobs, allowing Nodus to deliver physical instances only to
  workers advertising the requested capabilities.

## [0.10.42] - 2026-08-16

### Added

- **Durable physical-instance dispatch** (#236) — @hc12r. Enterprise
  WorkPool and core workers can now execute dynamic-expansion instances as
  fenced WorkOrders while the control plane keeps orchestration and artifact
  settlement. Whole-pipeline queue execution remains compatible.

- **Eligible pagination pages as physical instances** (#143) — @hc12r.
  Offset and numbered `source_api` pages with dataset-visible termination can
  be dispatched concurrently to workers, retried independently, and settled
  through execution-attempt fencing. Metadata-driven termination,
  checkpointed pagination, rate limiting, artifact responses, and unsupported
  strategies retain the existing in-process path.

### Behaviour Changes To Read Before Upgrading

- With instance dispatch enabled, eligible offset and numbered pagination
  pages run on WorkPool or JobQueue workers rather than the API process. Page
  configurations using response metadata, checkpoints, rate limiting, or
  artifact responses continue to use the existing local pagination path.

## [0.10.41] - 2026-08-15

### Added

- **Users carry a display name and email** (#232) — @hc12r. An
  account's username is an identity key, and for SSO accounts it is
  provider-prefixed, so using it as the label greeted people with a
  string that was never their name. `users` gains optional
  `display_name` and `email` columns (added by `ALTER TABLE` on boot,
  so existing installs need no migration step), read back on every
  user load and carried in the session token as claims omitted when
  empty. `SetProfile` writes only non-empty values, so one provider
  can never blank what another supplied. The UI resolves labels
  through a single `userLabel()` helper. Identity still keys on
  username: nothing here creates, merges, or re-points an account.

## [0.10.40] - 2026-08-15

### Added

- **Collapsible sidebar and grouped navigation** (#229) — @hc12r. The
  sidebar collapses to a 64px icon rail via a chevron in the brand
  zone, and nav items are grouped, with the Data group collapsible.
  Both states persist and apply pre-paint, so there is no width flash
  on load. Below 1025px the rail is forced as before; the duplicated
  60px collapsed width is unified onto the 64px token. Settings moves
  to a footer utility cluster with the user row and status controls.

- **Visual connector catalog** (#229) — @hc12r. Creating a connection
  starts from a searchable card grid grouped by category, each type
  with a glyph and one-line description, then a form step with a
  summary tile and back control; editing is unchanged.
  `GET /api/connection-types` additionally serves `description` and
  `icon` per type, and per-field hints now surface as input
  placeholders. A handler test pins the payload shape.

- **Local preflight script** (#229) — @hc12r. `preflight.sh` runs
  every CI gate locally (formatting, vet, UI build and checks, race
  tests, build smoke, gosec vs baseline, govulncheck, licenses) so
  regressions surface before a push, not in CI. CONTRIBUTING
  documents the local-first workflow.

## [0.10.39] - 2026-08-15

### Added

- **Application-surface redesign** (#221) — @hc12r. The pipeline
  inventory gains run metrics, filtering, saved views, density
  controls, and a five-run history strip per pipeline (new
  `run_history` field in `GET /api/pipelines/summary`); the DAG editor
  gains a searchable node library, explicit save state, and undo for
  node and edge movement. Settings, Variables, Connections, Plugins,
  API Integrations, Calendar, and the Dashboard adopt one
  control-center hierarchy — operational hero, section navigation,
  bordered control surfaces, deliberate empty/loading/error states —
  with width caps removed from management routes and layouts hardened
  against horizontal overflow. Existing routes and graph behavior are
  preserved.

## [0.10.38] - 2026-08-15

### Fixed

- **Cancellation converges mid-node, with or without the relay** (#226)
  — @hc12r. Third live-found cancellation bug of the arc, surfaced by
  the Redis kill-tests: a Redis restart permanently killed every pod's
  relay subscription (the receive loops exited on first error — fixed
  in brokoli-ee alongside this), after which cancels returned 200 with
  intent durably recorded while a 45-second single-node run completed
  anyway. Wave boundaries never arrive during a node, and single-node
  pipelines have none at all. A per-run watcher now polls the durable
  cancel_requested flag every 5 seconds while nodes execute and
  cancels the run context the moment it appears: any run converges
  within one poll interval of a cancel landing, regardless of relay
  transport health or pipeline shape. The relay is now a latency
  optimization, not a correctness dependency.

## [0.10.37] - 2026-08-15

### Fixed

- **SQL artifact store wires in for every distributed deployment**
  (#222) — @hc12r. Live-verifying v0.10.36's blob reclaim found it
  dormant: the SQL artifact store was gated behind ADR-017 instance
  dispatch (which nothing enables), so distributed deployments still
  ran local-disk artifacts — leaving worker disks growing AND resume
  broken by placement luck (a resumed run claims on any worker; the
  original run's artifacts lived on the original worker's own disk).
  Distributed mode now always uses the SQL store: cross-pod artifact
  reads work, and terminal blob reclaim engages on the deployment
  shape it was written for. Single-process mode keeps local disk.

## [0.10.36] - 2026-08-15

### Fixed

- **Everything that accumulates is now managed** (#217, #218, #219) —
  @hc12r. The production-readiness review's verdict was "everything
  that executes is solid; everything that accumulates is unmanaged."
  Three fixes close it:
  - **Lost queue deliveries recover** (#217, fixes half of #216): the
    dispatch outbox finally has its consumer — a sweep on every
    queue-wired instance re-enqueues pending runs whose committed
    outbox intent is older than a grace window. Live-verified gap: a
    Redis restart wiped the queue and stranded 5 accepted runs
    pending forever. Idempotent enqueue + the claim CAS make the
    sweep safe everywhere with no leader gating. The helm chart's
    Redis gains AOF + a PVC in brokoli-ee so the sweep is backstop,
    not routine.
  - **Transient blob scratch reclaimed at run-terminal** (#218, fixes
    #215): the 3-hour soak measured ~450MB/hour/worker of spill and
    reference-passing blobs nothing reclaimed. On SQL-artifact
    deployments those blobs are pure transport and are now deleted
    the moment a run finalizes (success, failed, or cancelled). On
    local-disk deployments the blobs ARE the artifacts and are
    deliberately untouched — the new TransientBlobJanitor capability
    encodes exactly that conditional.
  - **Scheduled run retention** (#219, fixes #214):
    BROKOLI_RUN_RETENTION_DAYS drives a 6-hourly purge that deletes
    expiring runs' artifacts first, then the rows (events and node
    runs cascade). Unset keeps runs forever — exactly the old
    behavior.

## [0.10.35] - 2026-08-14

### Fixed

- **The remaining cancel-loss windows, closed with durable intent**
  (#207) — @hc12r. Two windows where a cancel could still vanish
  after v0.10.34's relay: a claimed run waiting for a concurrency
  slot was not yet registered as cancellable (minutes-long window
  under saturation — precisely when users cancel things), and a
  relay message could be lost outright (fire-and-forget transport,
  or the owning process dying mid-cancel). The runner now registers
  before the semaphore wait, and a new runs.cancel_requested column
  records intent durably BEFORE any acting: the Runner re-checks it
  at every wave boundary (so cross-instance cancel now works even
  with no relay configured, at node-boundary latency) and recovery
  honors it when closing out a run with no recoverable path —
  cancelled, not recovery-failed. A too-late cancel never rewrites
  genuinely completed work. ADR-018 flipped to accepted in the same
  cycle (#209); remote-WorkOrder cancellation filed as #208.

## [0.10.34] - 2026-08-14

### Fixed

- **Run cancellation now works in distributed deployments** (#205) —
  @hc12r. Found live-verifying v0.10.33 on a real cluster: every
  cancel against the API pod returned 404 while the run kept
  executing on its worker — Engine.CancelRun only ever consulted its
  own process's active-runner map, so cancellation simply did not
  work in scheduler mode, and never had. Three fixes in one seam:
  the new extensions.RunCancelRelay (enterprise transport, mirrors
  JobQueue) broadcasts cancels to the instance that owns the run;
  the new store.PendingRunCanceller cancels queued, unclaimed runs
  with a compare-and-swap that ClaimPendingRun can never override
  (exactly one of claim and cancel wins); and Runner.Cancel is now
  durable across the pre-Execute window (previously a silent no-op
  against a nil cancel func — under semaphore saturation that window
  lasts minutes). Resolution order: local runner, pending
  compare-and-swap, relay broadcast, loud error. Single-process
  deployments behave exactly as before.

## [0.10.33] - 2026-08-14

### Fixed

- **Cancelling a run could record it as failed** (#203) — @hc12r.
  CancelRun and the runner's node-failure exit path raced on the
  run's terminal status: a cancel that killed an in-flight node made
  the node fail with "pipeline cancelled", and the runner then
  persisted the run as FAILED — plus a dead-letter-queue entry, a
  failure alert, and a critical notification — whenever its goroutine
  won the write race. Cancellation now wins over failure on every
  exit path. Found by TestExecutionContext_ObservesRunCancellation
  flaking about 1 in 5 full-suite runs; the test was right, the
  product lost the race.
- **Engine test teardown races** (#203) — @hc12r. 27 test sites
  across 13 files built engines without draining their background
  goroutines before store close and TempDir removal (the #94-family
  teardown flake). All now route through a shared
  drainEngineOnCleanup helper.

## [0.10.32] - 2026-08-14

### Fixed

- **add_column arithmetic silently stored expression text as data**
  (#201) — @hc12r. Found live verifying ADR-019 Milestone 2:
  "quantity * unit_price" put the literal STRING of the expression in
  every row, and a downstream aggregate summed it to zero with no
  error anywhere — since the rule's beginning, in pure batch.
  `*`/`/`/`-` now evaluate as arithmetic when every operand resolves
  numerically; div-by-zero and non-numeric column values yield nil
  (aggregates skip it); the `+`-concat and bare-word-literal
  behaviors are pinned and untouched, and hyphenated column names
  stay literals via the all-operands-numeric guard.

## [0.10.31] - 2026-08-14

### Added

- **Streaming hash aggregation (ADR-019 Milestone 2)** (#199) —
  @hc12r. An `aggregate` rule was a hard barrier — one anywhere in a
  transform forced the whole node to materialize its input. Rule
  lists now compile to a stream plan: row-local prefix streams per
  batch, the aggregate folds batches into per-group accumulator
  state, and suffix rules (anything, including sort) run on the
  grouped output. Incremental accumulators replicate the batch
  implementation's exact semantics, held to equivalence by test.
  Fan-out replay of ref outputs is pinned by test (it fell out of
  Milestone 1's per-consumer blob opens); concurrent cross-node
  stages are deferred with the reason recorded in ADR-019 (the
  re-iterable lazy-rows compat contract requires a seekable file — a
  FIFO would deadlock `len(rows)` — so pipelined subprocess input
  needs a future explicit per-script opt-in).

## [0.10.30] - 2026-08-14

### Added

- **Lazy rows, emit, and generator outputs in code nodes (ADR-019
  Milestone 1.5)** (#195) — @hc12r. Bounds the Python side of a code
  node, the residual peak Milestone 1 left behind. `rows` becomes a
  disk-backed, re-iterable lazy view in NDJSON mode: iteration
  streams in constant memory (double loops still work — each pass
  re-reads the file), while `len(rows)`/indexing transparently
  materialize for full backward compatibility. New `emit(row)` /
  `begin_emit(columns)` write output row-by-row with no output list
  at all, and generator `output_data["rows"]` becomes legal — a
  fully-streaming script holds near-zero Python memory. Input column
  order now travels from the ref via `BROKED_INPUT_COLUMNS`,
  preserving order that map keys never did.

### Fixed

- **Fault-injection tests leaked engine goroutines into teardown**
  (#195) — @hc12r. The CI flake that blocked two PRs: the fault-test
  harness never drained the engine's background goroutines (the
  missing "test half of #94"), so trigger-mode fan-out's in-flight
  SQLite connection recreated WAL files during `t.TempDir` removal —
  "TempDir RemoveAll cleanup: directory not empty". Both engine
  constructions in the file now close the engine before the store,
  before the directory.

## [0.10.29] - 2026-08-14

### Fixed

- **Stream engagement gets its own threshold** (#192, ADR-019) —
  @hc12r. Live measurement of v0.10.28 showed reference-passing never
  engaged for the benchmark workload: it was gated on the spill
  threshold (64 MiB encoded), while the datasets that OOM'd pods
  encode to ~15-20 MiB and cost 5-6x that as in-memory map rows.
  Spilling parks memory already paid; streaming prevents it being
  paid — different economics, different knob.
  `BROKOLI_STREAM_THRESHOLD_BYTES` (default 8 MiB encoded), negative
  disables reference-passing entirely.
- **Toolchain pinned to go1.25.13** (#192) — @hc12r. Seven newly
  published Go standard library vulnerabilities affect go1.25.12 and
  broke govulncheck CI on every branch; the toolchain directive makes
  the fix independent of what patch release the CI runner has cached.

## [0.10.28] - 2026-08-13

### Added

- **Reference-passing dataflow (ADR-019 Milestone 1)** (#190) —
  @hc12r. A stream-capable node whose input is held by blob reference
  processes it in bounded memory and hands its output back as a
  reference — code nodes go ref-to-ref (the engine no longer
  materializes their input or output datasets at all; the Python
  wrapper's NDJSON files are stream-copied to and from the blob store
  with rows counted in transit), and transforms whose rules are all
  row-local stream batch-wise without the whole-input clone. Engaged
  by the existing spill threshold: streaming is the completion of
  spilling — data large enough to spill now lives on disk its whole
  life instead of visiting memory first. Scheduling, retries,
  attempts, recovery, previews, and the ADR-010 resume-artifact
  contract are preserved exactly (artifacts for streamed outputs are
  a zero-copy manifest write via the new optional RefArtifactWriter
  capability where the store supports it). Blocking rules (sort,
  dedup, aggregate), multi-input nodes, expansion nodes, and dry runs
  keep the batch path unchanged, as ADR-019 barriers. Also fixes file
  mode's longstanding column-order loss via a wrapper sidecar.

## [0.10.27] - 2026-08-13

### Fixed

- **Per-run memory divisor sized from measurement, not a guess**
  (#186, ADR-018) — @hc12r. v0.10.26's adaptive concurrency default
  assumed 256 MiB per concurrent run; measuring a single 100k-row run
  directly against its pod's cgroup showed a ~284 MiB peak — two
  concurrent runs put ~570 MiB of largely live memory on a 512 MiB
  pod, past the ceiling where no GC setting helps, which is exactly
  why GOMEMLIMIT alone didn't stop the OOMs. The divisor becomes
  512 MiB: a 512 MiB pod runs one pipeline at a time, and demand
  beyond that surfaces as queue depth an autoscaler answers with
  more replicas.

## [0.10.26] - 2026-08-13

### Added

- **Resource defaults now adapt to the container's own memory limit**
  (#184, ADR-018) — @hc12r. Two changes, both no-ops on bare hosts:
  `GOMEMLIMIT` is auto-set to 90% of the cgroup limit when the
  operator hasn't set one (without it, Go's collector paces itself
  off GOGC alone with no idea a hard ceiling exists — on a small pod
  the runtime sails heap+garbage straight through the cgroup limit
  and the kernel OOM killer fires where an aggressive GC cycle would
  have sufficed); and `BROKOLI_MAX_CONCURRENT_RUNS`'s default becomes
  memory-derived, `clamp(limit/256Mi, 1, 4)` — a 512Mi pod takes 2
  concurrent runs instead of a hardcoded 4, letting demand surface as
  queue depth an autoscaler answers with more replicas rather than
  overcommitting one pod. Explicit `GOMEMLIMIT` /
  `BROKOLI_MAX_CONCURRENT_RUNS` values always win.

## [0.10.25] - 2026-08-13

### Fixed

- **Recovery's transition grace period was too narrow for real pod
  contention** (#181, ADR-018) — @hc12r. Found live installing KEDA
  and testing memory-aware admission control (#176/#178) together
  with elastic worker scaling (EE-ADR-008) for the first time — the
  first time this deployment genuinely had several concurrent runs
  sharing one worker pod under real CPU contention. A node transition
  under that contention took ~12s where an uncontended pod sees
  single-digit milliseconds, exceeding the original 10s grace period
  (#169) and reproducing the identical false-reclaim race a second
  time. Widened to match `reclaimSweepInterval` (20s) — the same "at
  most one extra sweep cycle" cost the original 10s already accepted
  for true orphan detection, measured against contested-pod behavior
  instead of an idealized quiet one.

## [0.10.24] - 2026-08-13

### Fixed

- **Memory-aware admission control missed a burst-admission race**
  (#178, ADR-018) — @hc12r. Found immediately by redeploying #176 and
  re-running the same concurrent stress test it was meant to fix: a
  cgroup memory reading is a snapshot of usage right now, and says
  nothing about a job that was just admitted but hasn't started
  allocating yet. A burst of simultaneous requests could still land
  two jobs on the same worker about a second apart — confirmed
  directly from worker logs, both starting within one second of each
  other on the same pod, which then OOMKilled shortly after (the same
  outcome as before the memory check existed). A 3-second settle
  delay since a worker's last successful admission now closes this,
  trading a small amount of throughput under a genuine burst for
  actually preventing it rather than only helping the slower,
  staggered-arrival case a bare headroom check already covered.

## [0.10.23] - 2026-08-13

### Added

- **Memory-aware admission control for `--mode worker`** (#176,
  ADR-018) — @hc12r. A worker no longer claims a new job from the
  distributed queue when its container is genuinely low on memory,
  checked via real cgroup v2/v1 usage against the container's own
  limit — not a predicted or configured concurrency count. Every
  existing admission control (`maxParallel`,
  `BROKOLI_MAX_CONCURRENT_RUNS`, `workerSlots`) bounded how many
  things ran, never how much memory they were allowed to use, which
  is the actual root cause behind the OOM findings #169/#171/#173
  already fixed the aftermath of. Fails open when cgroup limits can't
  be determined (bare host, most dev environments) — a strict
  addition to existing admission control, never a replacement.
  Escape hatch: `BROKOLI_MEMORY_BACKPRESSURE_DISABLED=1`.

### Fixed

- **`SQLArtifactStore` silently disabled node-level spilling** (#176,
  ADR-018) — @hc12r. `cmd/serve.go` swaps `eng.ArtifactStore` to
  `SQLArtifactStore` alongside instance-level remote dispatch — the
  deployment shape most likely to run large pipelines across real
  worker pods — and `SQLArtifactStore` didn't implement
  `BlobStoreProvider` at all, so every node's output stayed fully in
  memory regardless of size in that entire deployment mode, with no
  error or warning. Now backed by a local-disk blob store for
  intra-run spill scratch space, matching the default
  `LocalDiskArtifactStore`'s behavior.

## [0.10.22] - 2026-08-13

### Fixed

- **Recovery left a dangling `NodeRun` stuck "running" forever on
  runs it correctly marked failed** (#173) — @hc12r. Found
  live-testing #169/#171, re-running the same concurrent stress test
  once more to confirm them: `markRecoveryFailed` only ever touched
  the run row and appended a run-level event, so the specific node a
  "no recoverable path" determination was blamed on was left exactly
  as the dead process last wrote it — visible in the API/UI as a node
  stuck "running" indefinitely on a run whose own status was already
  terminal. Not a new regression from #169/#171; present since the
  original recovery feature, just newly surfaced by the same stress
  test. `closeDanglingNodeRun` now settles that node and appends a
  matching failure event, mirroring the normal failure path, so a
  recovered run's full history is consistently terminal end to end.

## [0.10.21] - 2026-08-13

### Fixed

- **Recovery's periodic sweep could defer a genuinely orphaned run
  forever** (#171) — @hc12r. Follow-up to #169, found immediately by
  redeploying it and re-running the same concurrent stress test.
  `recoverRun` unconditionally appends `RunEventRecoveryStarted` at
  the top of every call, before #169's grace-period check ever runs
  — including a pass that only ends up deferring — so reading the
  run's last event naively for recency always found *that same
  pass's own just-appended event*, which is by definition always
  fresh. A genuinely orphaned run therefore deferred forever, on
  every sweep tick, and was never actually reclaimed. Confirmed live:
  4 runs left mid-flight by an OOMKilled worker sat "running" with
  `since_last_event` under 5ms on every 20s sweep pass for several
  minutes straight. `lastGenuineActivity` now skips recovery's own
  event types when computing the recency signal.

## [0.10.20] - 2026-08-13

### Fixed

- **Recovery's periodic sweep could false-positive-orphan a live run mid
  node-transition** (#169) — @hc12r. `reconcileExecutionAttempts` only
  sees non-terminal `execution_attempts` rows, so the instant between
  one node going terminal and the Runner claiming the next node's
  attempt has no lease at all — not because the run is orphaned, but
  because nothing has asked for one yet. Before the periodic reclaim
  sweep (#167), `RecoverNonTerminalRuns` only ran once per process at
  startup, so this window was effectively never hit; a sweep ticking
  every 20s hits it routinely under real concurrent load. Found
  live-testing #167/brokoli-ee#85/brokoli-ee#86 against a real
  multi-worker deployment under a 10x-concurrent-run stress test: 7 of
  7 runs whose worker was OOMKilled mid-flight had a node permanently
  orphaned as "running" this way, on top of the run itself being
  correctly marked failed. A 10s grace period on the run's most recent
  event now defers this specific classification instead of immediately
  concluding orphaned — negligible added latency against a 20s sweep
  interval, and it does not affect genuinely orphaned runs, which stay
  reclaimed on the next pass once the grace period elapses.

## [0.10.19] - 2026-08-12

### Added

- **`extensions.JobQueueRenewer`** (#167) — @hc12r. Optional capability
  interface (`RenewClaim(jobID string) error`) a `JobQueue`
  implementation can support to keep a claim's visibility timer reset
  while genuinely still processing it. `cmd/serve.go` starts a 10s
  heartbeat goroutine alongside both whole-pipeline and `WorkOrder` job
  execution when the configured queue implements it. Fixes a real race
  found live-testing under sustained concurrent load: a Redis Streams
  consumer-group job whose processing legitimately exceeded the 30s
  visibility timeout had its claim silently reassigned via
  `XAUTOCLAIM` mid-flight, so the true owner's own eventual `Ack`
  failed with "job is not claimed" even though the work itself
  completed correctly.
- **Periodic reclaim sweep on the scheduler leader** (#167) — @hc12r.
  `RecoverNonTerminalRuns` was already idempotent and safe to call
  repeatedly, but was only ever called once, at process startup — so a
  run whose execution attempt died after startup (lease expired, no
  process left to complete it) stayed "running" forever with nothing to
  reclaim it. `Scheduler.runReclaimSweep` now calls it on a 20s ticker,
  gated on the same leader-election check `catchUpMissedRuns` already
  uses.
- **`engine.SQLArtifactStore`** (#161, #167) — @hc12r. A Postgres/SQLite-
  backed `ArtifactStore`, replacing the default local-disk store when
  remote instance dispatch is enabled. Found live: a dynamic-expansion
  instance dispatched to a remote worker pod wrote its result artifact
  to that pod's own local disk, invisible to the dispatcher pod reading
  it back, so the run failed with "result could not be read back" even
  though the worker finished successfully. Uses the same database every
  pod in a distributed deployment already connects to — no new
  infrastructure required.

## [0.10.18] - 2026-08-12

### Changed

- **Remote dynamic-expansion instances now dispatch concurrently** (#164,
  ADR-017) — @hc12r. Found under real load in a Kubernetes deployment: a
  30-item expansion node with 6 idle worker pods still ran no faster
  than one worker, because instances were dispatched strictly one at a
  time, waiting for each to fully settle before starting the next.
  Remote dispatch (`Engine.InstanceJobQueue` set) now fans out up to 16
  instances at once; local execution is unchanged. No config needed —
  existing opt-in behavior via `BROKOLI_INSTANCE_DISPATCH=1` (#158) is
  unaffected.

## [0.10.17] - 2026-08-12

### Added

- **ADR-017 dispatcher-side wait-and-fetch loop** (#155) — @hc12r. The
  engine can now enqueue a dynamic-expansion instance for remote
  execution and wait for its result, instead of always running it
  in-process — `Engine.InstanceJobQueue`, opt-in, nil by default
  everywhere, zero behavior change for existing deployments.
- **ADR-017 worker-side execution of a `WorkOrder`** (#156) — @hc12r.
  `ExecuteInstanceWorkOrder`/`ExecuteInstanceJob` let a worker sharing
  the dispatcher's store actually run a dispatched instance and settle
  its claim; wired into `cmd/serve.go`'s `--mode worker` loop, the first
  real consumer.
- **`Engine.InstanceJobQueue` now actually wired in `cmd/serve.go`**
  (#158) — @hc12r. #155/#156 shipped the mechanism, but nothing wired
  the dispatcher side into either core's own binary or the enterprise
  binary (which reuses this same command) — found while testing the
  mechanism end-to-end against a real deployment, not by any test in
  the original PRs. Opt-in via `BROKOLI_INSTANCE_DISPATCH=1`.
- **`extensions.RunJob.DeliveryCount`** (#159) — @hc12r. Separates the
  queue-transport's own delivery/redelivery counter from `Attempt`
  (which mirrors `models.ExecutionAttempt.Attempt`, the node/instance
  execution-attempt identity a `WorkOrder`-bearing job settles under).
  Found live-testing ADR-017 in Kubernetes: the enterprise Redis-backed
  `JobQueue` was overwriting `Attempt` with its own delivery count on
  every dequeue, which silently broke every remote instance dispatch —
  no test caught it because every earlier test used a simplified
  fake/channel queue that never exercised real redelivery bookkeeping.

## [0.10.16] - 2026-08-12

### Added

- **Dynamic-expansion items get durable claim/lease/fencing** (#149,
  ADR-017) — @hc12r. Extends #144's node-level wiring down to instance
  granularity: each expansion item now gets its own claim/lease/fencing
  record, generalizing #145's in-memory-only retry-skip into the same
  durable mechanism nodes already have. Same-process only.
- **ADR-017's claim-payload work-description envelope** (#150) —
  @hc12r. `extensions.RunJob` gains `InstanceKey` and a `WorkOrder`
  field (`InstanceWorkOrder`): node type, script, config, the instance's
  input row, run params, timeout — what a remote claimant would need to
  execute one physical instance. Purely additive; nothing populates it
  yet.
- **ADR-017 output-reference delivery design** (#151) — @hc12r. Answers
  how a completed remote instance's output gets back: push not pull,
  `ArtifactStore` gains an instance dimension, `ExecutionAttempt` stays
  control-plane only, a worker→server endpoint modeled on the existing
  checkpoint-forwarding handlers, reusing ADR-012's inline-vs-spill
  threshold.
- **`ArtifactStore` gains instance-level identity** (#152, ADR-017) —
  @hc12r. Key widens from `(run_id, node_id)` to `(run_id, node_id,
  instance_key)`, byte-identical paths for the empty-instance-key case
  (every existing artifact stays readable), purely additive.

## [0.10.15] - 2026-08-12

### Added

- **Node-level execution attempts now go through claim/lease/fencing**
  (#144, #90 M3) — @hc12r. `store.ExecutionAttemptStore`'s claim/lease/
  fencing contract existed but had no production caller below the
  pipeline-level outbox row; node dispatch now creates, claims, acks, and
  settles its own attempt row with a short renewed lease
  (`store.DefaultLeaseDuration`/`RenewInterval`), so a crashed process's
  lease becomes reclaimable within seconds instead of being deferred to
  for however long the node's own configured timeout is. Optional
  capability throughout — a store that doesn't implement
  `ExecutionAttemptStore` is unaffected.
- **Dynamic-expansion node retries skip already-succeeded siblings**
  (#145, #90 M3) — @hc12r. A retry of a `.expand()` node used to
  re-execute every item from scratch, even ones that had already
  succeeded before a later sibling failed. Now only items that failed or
  were never reached run again; reused items still get their own
  `models.ExpansionInstance` row for the new attempt, so per-attempt
  history stays complete.
- **ADR-017 (proposed): worker protocol v2, instance-level dispatch**
  (#146) — @hc12r. Scopes moving the unit of worker claim from a whole
  pipeline run to a physical instance (node/page/expansion-item) — what
  #90 M3's acceptance gate (a worker killed mid-instance, fenced out, its
  instance retried by a different worker without disturbing successful
  siblings) actually needs. Status proposed; implementation gated on
  co-design review.
- **`execution_attempts`' claim key gains an instance dimension** (#147,
  ADR-017 slice 1) — @hc12r. `(run_id, node_id, attempt)` widens to
  `(run_id, node_id, instance_key, attempt)`, matching the physical-plan
  instance-identity scheme, so a pagination page or expansion item will
  be able to claim/lease/fence independently of its siblings instead of
  colliding with them. Purely additive — every existing caller passes an
  empty instance key and is unaffected. Migrates a live database in
  place on both backends.

## [0.10.14] - 2026-08-12

### Added

- **`sink_db`/`migrate` generate their own write SQL** (#135, #136,
  brokoli-sdk#12) — @hc12r. `source -> sink_db(table, mode)` now works on
  its own — no upstream `sql_generate` node needed. Both `sink_db` and
  `migrate` support `append`/`overwrite`/`upsert` (upsert keyed on
  `key_columns`; `ON CONFLICT` on Postgres/SQLite, `ON DUPLICATE KEY
  UPDATE` on MySQL), generating the SQL from the input rows directly. The
  older `sql_generate -> sink_db` pattern still works unchanged.
- **`filter_rows` comparison operators** (#133, brokoli-sdk#14) —
  @hc12r. `filter_rows` conditions now support `>`, `<`, `>=`, `<=` (in
  addition to the existing `=`/`==`/`!=`/`in [...]`), with numeric
  comparison when both sides parse as numbers and lexicographic
  otherwise. An unrecognized condition is now a run-time error, not a
  silent keep-all-rows pass-through.
- **`sink_api` resolves `conn_id`, matching `source_api`** (#139, closes
  #137) — @hc12r. The connection resolver had no case for `sink_api`, so
  a `conn_id` on that node type was silently ignored — no base URL, no
  headers, no Basic Auth ever got injected. It now resolves identically
  to `source_api` (relative/absolute URL, merged headers, Basic Auth).
- **`retry_backoff` actually selects a backoff strategy** (#140, closes
  #138) — @hc12r. The retry loop hardcoded exponential backoff and never
  read the `retry_backoff` config key. `"linear"` and `"fixed"` now work
  as documented, alongside the existing default `"exponential"`.
- **`GET /api/plugins` carries each plugin's payloads** (#141, #110 M3)
  — @hc12r. The list response now includes each plugin's
  runtime/os/arch/requires payload list, so a caller can resolve its own
  feasibility (`plugins.SelectPayload`) without downloading the archive
  first — the data prerequisite for per-worker plugin availability.

### Fixed

- **Physical-plan e2e test flake under parallel load** (#134) — @hc12r.
  Inline `NewEngine(s).RunPipeline(...)` calls that never closed the
  engine left background event goroutines writing to the store after the
  test returned, occasionally landing a write during `t.TempDir()`
  cleanup (`directory not empty`). Test-only fix; no production change.

## [0.10.13] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.13 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.13).

### Added

- **Plugin distribution UI and curated index** (#126, #129, #130, #131) —
  @hc12r. Plugins are now installable without the CLI: a Plugins page lists
  installed plugins, uploads a `.bkg`, and removes; a curated static index
  (`GET /api/plugins/index`, `BROKOLI_PLUGIN_INDEX` override, ADR-016 §5)
  is browsable and installable by name (`POST /api/plugins/index/{name}`),
  with the archive's sha256 verified against the index digest before any
  bytes reach the installer — no plugin executes without a digest match.
  Install/remove errors are surfaced verbatim, so a wrong-platform or
  tampered package shows the server's named reason.
- **Capability advertising for plugin packaging** (#127) — @hc12r.
  `GET /api/capabilities` now reports `supported_packaging_versions` and
  `supported_runtime_classes` (native, python, node, jvm), so clients can
  tell what a deployment understands before uploading — the same
  negotiate-before-you-act contract as `supported_ir_versions`.
- **Durable physical run instances** (#123, #125, ADR-015 §8, #90) —
  @hc12r. A run's physical instances (one per plain node, one per resolved
  expansion item) are projected at `GET /api/runs/{id}/instances` and, once
  a run records them, persisted in a durable `physical_instances` table
  (read durable-first with a projection fallback for older runs). A run now
  answers both what it *planned* (`/plan`) and what it *ran* (`/instances`)
  in the physical model.

### Fixed

- **Plugin runner SIGTERM grace on the spec path** (#128) — @hc12r. The
  install-time `spec` probe now applies the same SIGTERM-then-SIGKILL grace
  as the main run path (`Cancel` + `WaitDelay`) instead of killing a hung
  plugin immediately with no chance to shut down cleanly.
- **SODP dashboard eviction test no longer flakes after midnight** (#124) —
  @hc12r. The test derives its expected day buckets from the seeded
  timestamp rather than the wall clock.

## [0.10.12] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.12 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.12).

### Added

- **Plugin package format and management API** (#117, #118) — @hc12r.
  Plugins ship as a single `.bkg` archive (manifest + per-platform/runtime
  payloads) that installs on any supported host, or fails at install with
  a named reason: payload selection by os/arch and runtime (native,
  python, node, jvm with version checks), mandatory integrity hash, and a
  `spec` drift check — nothing lands before all three pass.
  `GET/POST/DELETE /api/plugins` and `GET /api/plugins/{name}/archive`
  install (from an uploaded archive), list, remove, and serve plugins;
  install and remove hot-reload the node-type registry without a restart.
  ADR-016.
- **Physical execution plans** (#119, #120, #121) — @hc12r. The authored
  logical pipeline and the per-run physical plan are now distinct: a
  planner expands the graph into scheduled stages and work units
  (pagination page-groups, dynamic-expansion fan-out) with deterministic
  instance keys and honest runtime-resolved counts. `GET
  /pipelines/{id}/plan` explains the plan before execution; every run
  persists its plan snapshot (`GET /runs/{id}/plan`) so recovery and
  audit see the plan as-decided, not a recomputation. Foundation for
  independently scheduled, retryable physical instances. ADR-015.

### Changed

- **The `Store` interface is composed from focused capability
  interfaces** (#120) — @hc12r. The 100+-method contract is now
  `PipelineStore`, `RunStore`, `AlertStore`, and the rest, embedded into
  `Store`. Pure refactor — no behavior change — but a new capability is
  added as its own interface instead of force-breaking every
  implementation, and a consumer can depend on the narrow slice it needs.

## [0.10.11] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.11 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.11).

### Added

- **Stateless validation** (#114) — @hc12r. `POST /api/pipelines/validate`
  runs full structural + executable validation on a request-body IR
  document without persisting anything.
- **`supported_execution_features` on `/api/capabilities`** (#115) —
  @hc12r. The list of pipeline semantics this host can actually
  *execute* — `conditional-routing`, `dynamic-expansion`, `union`,
  `pagination-checkpoints` — with an honesty rule: a feature is listed
  only when a pipeline using it runs. SDK preflight gates on it, so
  compile-only features are refused at deploy instead of failing at run
  time.
- **Cross-model contract fixtures** (#115) — @hc12r. A real SDK-compiled
  IR 2.1 payload is validated against the canonical schema on every test
  run; the SDK repo validates its emissions against a vendored schema
  copy, so contract drift in either direction fails a suite naming the
  payload.

### Changed

- **Unknown top-level fields are rejected** (#114) — @hc12r. Create,
  update, and JSON import decode fail-closed: an unrecognized field
  returns `400` naming it. Forward-compatible payloads belong under the
  new `extensions` namespace, which is persisted in both stores and
  echoed back uninterpreted. **Breaking** for clients that sent junk
  fields the server used to silently drop.
- **The JSON→YAML import fallback is gone** (#114) — @hc12r. A JSON type
  mismatch on `/api/pipelines/import` used to fall through to the YAML
  parser (YAML parses JSON) and return `201` with a silently mutilated
  pipeline — missing capabilities, params, hooks, SLA and dependency
  fields. A JSON body now gets the JSON path, full stop.
- Prose-only docs changes (Markdown, ADRs, RFCs) skip the Go CI pipeline
  (#113) — @hc12r; `docs/schema/**` deliberately still runs everything.

## [0.10.10] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.10 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.10).

### Added

- **Plugin subprocess cancellation** (#100) — @JefferMarcelino. Cancelling
  a run or hitting a node timeout now terminates the running plugin
  process: `SIGTERM`, then `SIGKILL` after the grace window (killed
  directly on non-Unix). Plugin-backed nodes no longer run to completion
  after a cancel. ADR-013 M2.
- **Canonical pipeline IR schema** (#111) — @hc12r.
  `docs/schema/pipeline-ir-2.1.json` is the machine-readable statement of
  the wire contract (ADR-014), bound to `models.Pipeline` by two-way
  contract tests: model/schema drift now fails CI naming the exact
  property. Adds `github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0).

### Changed

- **Persistence is fail-closed** (#106) — @hc12r. Create, update, import,
  clone, and rollback run full executable validation before anything is
  written: unknown node types are rejected with `400 … has unsupported
  type` instead of executing as silent successful no-ops, cycles and
  missing required config are caught at save time, and rolling back to a
  no-longer-valid snapshot is refused. Updates that leave the graph
  byte-identical (pause, rename, schedule) skip executable validation so
  legacy rows stay operable. The editor's Run/Preview/Check flows now
  bail out loudly when a save is rejected instead of silently executing
  the previously persisted graph, and the zero-node Blank template is no
  longer listed as creatable. Dry-run responses carry an `error` field
  alongside partial results.

### Fixed

- **Pipeline hooks persist** (#111) — @hc12r. `hooks` was decoded, echoed
  on the create response, and silently dropped by the store — gone on the
  next read. Both SQLite and Postgres now persist it through create,
  update, and every read path.
- **Dashboard day counts bucket in one timezone** (#108) — @hc12r.
  `runs_today` compared a local date against UTC-stored timestamps, so
  non-UTC servers miscounted for the first hours of every local day (and
  the corresponding test flaked after midnight).

### Roadmap

- **ADR-016 proposed: plugin packaging, runtime resolution, and
  distribution** (#105) — @hc12r. One published archive with per-payload
  runtime classes, install-time feasibility resolution, an install
  API/UI, worker fetch-by-digest, and a curated index. Discussion: #104;
  implementation tracker: #110.

## [0.10.9] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.9 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.9).

### Added

- **Conditional edge routing — IR 2.1** (#97, #101) — @hc12r. Condition
  nodes route true/false branches as first-class edges: skipped branches
  propagate transitively, convergence nodes wait for all inputs to
  resolve, and routing decisions, skipped outcomes, and resume lineage
  persist atomically across SQLite, PostgreSQL, JSON, YAML, clone, and
  version snapshots. #101 turned it on: the server now advertises IR 2.1
  in `supported_ir_versions` (while `CurrentIRVersion` stays 2.0),
  rejects malformed conditional graphs and unsupported expressions
  before persistence, and the UI authors branch metadata and renders
  skipped node states.

### Changed

- **SLA fields are labeled as an enterprise capability** (#98) — @hc12r.
  The deadline/timezone fields are stored by every edition but only
  monitored by an enterprise deployment; the editor now says so with a
  tag and tooltip instead of letting a community install configure a
  deadline nothing watches.

### Fixed

- **Connection Basic Auth applies on the read path, not only on writes**
  (#93) — @hc12r. `source_api` reads — the first fetch and every
  paginated page — now carry connection credentials, which were
  previously silently dropped on reads while the same connection worked
  on a sink. Deliberate precedence: connection credentials win over a
  handwritten `Authorization` header.
- **The engine can be shut down** (#96) — @hc12r. `Engine.Close(ctx)`
  refuses new dispatch, stops starting background work, and waits —
  bounded by the caller's context — for every goroutine the engine owns.
  Ends `database is closed` logs at shutdown and the `TempDir RemoveAll`
  CI failures caused by orphaned fan-out goroutines.
- **`GET /api/capabilities` is public again** (#102) — @hc12r. Exact
  anonymous capabilities discovery now passes the API-key, JWT/open-mode,
  workspace, and enterprise SSO auth layers, with per-layer regression
  coverage; every other API method and path stays protected. Unblocks
  SDK capability preflight on authenticated deployments.

## [0.10.8] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.8 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.8).

### Added

- **The artifact plane** (#85, #88, #89) — @hc12r. `pkg/artifact`:
  `ArtifactRef`/`DatasetRef` with a versioned manifest and a
  content-addressed local store; `response="artifact"` returns a real
  reference; node outputs above `BROKOLI_SPILL_THRESHOLD_BYTES` (default
  64 MiB) spill automatically. ADR-012, updated per milestone.
- **Persisted alert inbox, run events over HTTP, org-wide DLQ** (#80) —
  @hc12r.
- **Connector protocol `MsgProgress`** (#66) — @JefferMarcelino. First
  milestone of ADR-013, and the author's first shipped change. 🎉
- **Keyset pagination for run history** (#84) — @hc12r. `?after=`/`?limit=`
  cursors; `?page=` now returns real rows and a real total.
- **`union`/`dataset_map`/`dataset_filter` node types, executable
  `.expand()` fan-out** (#34, #35) — @hc12r.
- **source_api execution contract**: response contracts and pagination
  execute (#33); bounded page fan-out (#43); persisted, resumable
  checkpoints (#45, #53); page-retry config keys (#48) — @hc12r.
- **Database-backed, admin-editable pipeline templates** (#71, #72) —
  @hc12r.

### Changed

- **Dashboard rebuilt as a triage console**; light theme rebuilt on
  surface tones; dashboard figures read from the database instead of
  evicted live state (#83, #81) — @hc12r.
- **`response="artifact"` no longer inlines the body** — the output row
  is `uri`/`media_type`/`size_bytes`/`checksum` (#88) — @hc12r.
- Plugin node-type gating runs before dispatch (#65); manifest kinds
  propagate into node capabilities (#63) — @hc12r.

### Fixed

- `runs.org_id` populated at creation, legacy rows backfilled (#54);
  artifact/checkpoint stores wired into purge (#51); empty JSON array is
  zero records (#46); sample data 404 (#68); SODP presence cleanup (#56);
  `PORT` env synced with `--port` (#69); Data Quality template config
  (#70) — @hc12r.

> Releases 0.7.2 through 0.10.7 predate this file's revival; their
> histories live in the
> [GitHub releases](https://github.com/Tnsor-Labs/brokoli/releases).

## [0.7.1] - 2026-04-13

### Fixed

- **SSRF guard no longer blocks worker self-references.** `pkg/fetchers`
  rejected relative `/api/samples/...` URLs once they were resolved against
  `BROKOLI_SERVER_URL`, because in k8s/docker deployments the server
  hostname resolves to a private cluster IP (10.x / 172.16.x / ClusterIP) —
  which is exactly what `isBlockedHost` rejects. Workers running in a
  separate pod couldn't fetch sample data from the API they were already
  authenticated to. The fix tracks whether a URL originated as a relative
  path (an implicit trusted self-reference to the Brokoli server) and skips
  the SSRF check in that case only. Absolute URLs with private IPs are
  still blocked. Also removes a dead `HasPrefix` check that ran after the
  URL had already been rewritten to `http://...` form.

## [0.7.0]

### Added

- **SODP realtime stack** — `/api/ws` now speaks the State-Oriented Data
  Protocol ([orkestri/SODP](https://github.com/orkestri/SODP)) instead of a
  hand-rolled JSON event hub. Binary msgpack framing, per-key subscriptions,
  delta fanout, and reconnect-resilient via the protocol's `RESUME` frame.
  See [`docs/realtime.md`](docs/realtime.md) for the architecture, key model,
  wire format, and rate limits.
- **`pkg/sodp`** — new in-process Go implementation of SODP v0.1: versioned
  state store with ring-buffer delta log, per-org tenant-isolated fanout bus,
  per-session rate limiting and watch caps, JWT auth, eviction goroutine for
  completed run state. 50+ unit tests, 12 integration tests against a real
  WebSocket, and a cross-language test that runs `@sodp/client@0.2.1` from
  Node.js against the live server.
- **`api/eventbus_bridge_test.go`** — distributed-mode test: worker publishes
  to `EventBus` (in-memory in OSS, Redis pub/sub in enterprise), API pod
  subscribes and forwards events into the SODP fanout. The bus → bridge → SODP
  path is the only way events cross pod boundaries in distributed deployments.
- **`api/ws_jwt_context_test.go`** — regression test for the JWT-on-WebSocket
  context-propagation fix below.
- **Calendar redesign** — replaced the previous flat-grid heatmap with a
  GitHub-style horizontal layout (weeks as columns, days as rows). Each day
  cell shows a vertical color split sized by the actual success / running /
  failed proportions instead of going solid red on any failure. Activity
  intensity is log-scaled relative to the busiest day in the range. Includes
  a sparkline trend strip, muted data-viz palette local to the page, and a
  proportional detail bar with per-day success rate.

### Changed

- **WebSocket protocol on `/api/ws` is now binary msgpack, not JSON.** Any
  external tooling that opened `/api/ws` and parsed JSON event objects must
  switch to a SODP client. Reference clients available for TypeScript
  (`@sodp/client`), Python (`sodp`), and Java (`io.sodp:sodp-client`), or
  speak the wire format directly using the schemas documented in
  `docs/realtime.md`. The Brokoli UI handles this transparently via
  `ui/src/lib/ws.ts` — no UI-side migration required when upgrading.
- **`@sodp/client@^0.2.0`** is now a runtime UI dependency. Pinned in
  `ui/package.json`. Self-hosters who build the UI from source will pull it
  automatically via `npm install`.
- **`api.Hub` removed.** The pre-SODP `Hub` type and `handlers_ws.go` were
  deleted. `RegisterRoutes` now takes `*sodp.Server` instead of `*Hub`.
  Internal callers only.
- **`bridgeCh` buffer is 512 events.** Engine events that overflow it are
  dropped with a warning log line, instead of stalling the engine's event
  channel. This is a deliberate backpressure choice for high-frequency log
  events; the cap is sized for typical workloads but is not yet load-tested
  at thousands of events per second.
- **Calendar palette is now muted** (forest sage / terracotta / steel blue
  inspired by Observable's data-viz palettes) for large color regions where
  the global bright `--success`/`--failed` were "hot." The global theme is
  unchanged — every other status indicator across the app still uses the
  vivid colors where high saturation reads correctly at small sizes.

### Fixed

- **Multi-tenant isolation collapsed on the WebSocket path.** The HTTP
  `JWTAuth` middleware validated tokens on `/api/ws` upgrades but did not
  propagate `claims` or `org_id` into the request context. The SODP server
  read claims from the context to enforce per-session tenant filtering, found
  nothing, and treated every authenticated WebSocket session as the default
  org — meaning users from `org=acme` would receive realtime events from
  `org=widgets` and vice versa. The non-WebSocket branch of the same
  middleware was correct; the WebSocket branch is now aligned with it.
  `api/ws_jwt_context_test.go` locks this in as a regression test.
- **`Pipelines.svelte` run counts reset to 1 after each WebSocket event.**
  Two stacked bugs: the WS event handler replaced the cached pipeline-runs
  entry with a fresh single-element list missing the `_total/_success/...`
  counter fields; and the Run-button click handler did its own optimistic
  REST refetch that wiped the same fields a millisecond earlier. The handler
  now mutates the cached counters in place and tracks `seenStarted` run IDs
  to handle late-arriving terminal events; the click handler no longer
  refetches.
- **`Connections.svelte` and `Variables.svelte` empty-state CTAs were
  no-ops.** Both pages set a phantom `showCreateModal` variable that didn't
  exist; the actual modal state is `showModal`. The CTAs now call the proper
  `openCreate()` handler that initializes the form and opens the modal.
- **`RunIndicator` panel overflowed off-screen.** The expanded "Recent Runs"
  panel was anchored to the floating button's `left: 0`, so a 300px-wide
  panel attached to a button at `right: 24px` clipped past the viewport. Now
  anchored to `right: 0` so it expands leftward into the page.
- **WebSocket connection burned through retries before login.** `App.svelte`
  created the SODP client unconditionally on mount, before the user had
  authenticated. The client hit `503` (open mode) or `401` (locked mode) for
  the first 4-8 attempts and then sat in a 30-second backoff loop, missing
  events that fired immediately after login. Connection lifecycle is now
  driven reactively from `$authUser`: open on login, close on logout. No
  retries are wasted against an unauthenticated session.
- **`DecodeFrame` rejected bodyless frames.** Heartbeat and ack frames with
  a nil body decoded into an empty `msgpack.RawMessage`, which the body
  decoder treated as EOF. `DecodeFrame` now treats `len(raw[3]) == 0` as a
  nil body explicitly.
- **`sodp.Server` had a concurrent-write race on the WebSocket connection.**
  Handler responses (HELLO, STATE_INIT, RESULT, ERROR) wrote directly to the
  connection while the write-pump goroutine also wrote, which `gorilla/websocket`
  doesn't support. All handler responses now go through the session's `Send`
  channel; the write pump is the sole writer to the connection.

### Security

- The WebSocket JWT-context fix above is a security fix in addition to a
  correctness fix. The data leak only manifested in multi-tenant deployments
  with multiple orgs, but in those deployments it was complete: every realtime
  event visible to any org was visible to every other org. Single-tenant /
  community-mode deployments were never affected.

---

## Prior history

This file starts with the SODP migration. For commit-level history before
this point see `git log` — the project did not maintain a CHANGELOG previously.

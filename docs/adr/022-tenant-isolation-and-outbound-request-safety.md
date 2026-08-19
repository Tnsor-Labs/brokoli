# ADR-022: Enforce tenant isolation and outbound-request safety at the data/network layer, not per-handler

**Status:** proposed
**Date:** 2026-08-19

## Context

A targeted security audit of `brokolisql-go` (core) and `brokoli-enterprise`
(EE) — five parallel passes over multi-tenant scoping, injection/path
traversal, secrets handling, SSRF, and RBAC completeness — found nine
concrete, verified issues. Read individually they look like nine
unrelated bugs. Read together they're two systemic gaps wearing nine
symptoms: **resource-ownership checks and outbound-URL safety checks
are implemented per-handler, by hand, on the honor system, and several
handlers didn't get the memo.** This ADR proposes closing both gaps at
the layer where a missed check can't happen — the data-access layer for
ownership, a single required HTTP client for outbound safety — instead
of patching each handler and hoping the next one remembers.

### What the audit found

**Critical — broken tenant isolation, EE workspaces**
(`ee/team/handlers_workspace.go`). `Get`/`Delete`/`RemoveMember`/
`AddMember`/`CreateToken`/`ListMembers`/`ListTokens`/`DeleteToken` take
`wsId` from the URL path and pass it straight to the store — none check
it belongs to the caller's org. This isn't a few missed `if` statements:
`PermissionChecker.GetUserPermissions(userRole, workspaceID)`
(`ee/team/permissions.go`) accepts a `workspaceID` parameter and never
uses it — the permission model itself has no workspace dimension, only
a role dimension. An admin of org A can read or delete org B's
workspace, evict org B's members, or **mint a live admin-scoped API
token for org B for themselves** — a persistent cross-tenant backdoor,
not just a read leak. `store/postgres.go`'s `GetWorkspace`/
`DeleteWorkspace` confirm the pattern one layer down: plain
`WHERE id=$1`, no `org_id` anywhere in the query.

**Critical — SSRF, several paths with zero validation**. `sink_api`
nodes (`engine/node_handlers.go`), pipeline lifecycle webhook hooks
(`engine/runner.go`'s `fireHook`), and the generic connection-test
endpoint (`api/handlers_connection.go`'s `testGeneric`) all issue
outbound requests with no target validation at all — any pipeline
editor can point one at `169.254.169.254` or an internal host, both as
a probe and as an exfiltration channel. The one path that *does*
validate — `source_api`, via `pkg/fetchers/rest_fetcher.go`'s
`isBlockedHost` — is itself incomplete: loopback is deliberately
allowed, the check resolves the hostname once and lets the HTTP client
re-resolve it independently at request time (a DNS-rebinding TOCTOU),
and neither client sets `CheckRedirect`, so a validated URL can
redirect straight to a blocked target and Go's default client follows
it. `api/handlers_connection.go` has a *second*, near-identical
`validateExternalURL` with the same three gaps — the guard was
copy-pasted, not shared, so both copies are wrong in the same way.

**High — core org-scoping gap** (`api/handlers_run.go`).
`ListByPipeline`, `Backfill`, `DryRun`, `NodeStats` skip the
`ValidateOrgAccess`/`DenyOrgAccess` check every sibling endpoint in the
same file applies (`Get`, `GetLogs`, `CancelRun`, `TriggerRun`,
`GetPlan`, `GetInstances`, ...). An editor in org A can trigger real
execution of org B's pipeline (`Backfill`/`DryRun` actually run the DAG)
against org B's live connections, and read org B's run history and
per-node stats.

**High — OSS `editor` silently authorized as `admin`**
(`api/routes.go`'s `requirePerm` fallback, used whenever EE Team isn't
enabled). The fallback only excludes `viewer`; every other role passes
every permission check identically to `admin`. This gates plugin
install (code execution on the host), `system/purge` (mass deletion of
run history/artifacts), and notification/Slack webhook settings. A
`requireAdmin` helper exists specifically because this fallback doesn't
enforce true role-exclusivity — but it was only ever wired in for
templates, not for the three sensitive operations above.

**Medium**
- Second-order SQL injection: `engine/sqlgen.go`'s `quoteIdent()`
  doesn't escape an embedded quote character in an identifier. CSV/Excel
  column headers (`pkg/loaders/csv_loader.go`,
  `pkg/loaders/excel_loader.go`) are taken verbatim as column names and
  flow into `sink_db`'s generated `CREATE TABLE`/`INSERT INTO` text,
  executed via `tx.Exec` with no parameterization for identifiers. A
  crafted CSV header (or a sink `table` config value) breaks out of the
  quoted identifier into arbitrary SQL against the sink DB, with the
  pipeline's configured credentials.
- Plaintext DB password leaked into pipeline run logs: the migrate node
  (`engine/node_handlers.go`) logs `sourceURI[:40]`, and `sourceURI`
  comes from `Connection.BuildURI()`'s `url.UserPassword(...)` — the
  plaintext password is almost always inside those 40 characters. That
  log line is reachable through the run-logs API and a downloadable
  `.txt` export, a broader and lower-privileged audience than the
  connection endpoints, which correctly redact the same password
  everywhere else.
- Webhook token comparison isn't constant-time: `engine/webhook.go`
  defines `ValidateWebhookToken` using `subtle.ConstantTimeCompare`, but
  it's dead code — the actual handler (`api/handlers_misc.go`) does a
  plain `token != p.WebhookToken`. A 1-attempt/10s rate limit makes this
  impractical to exploit today, but it's a real gap between intent and
  implementation.

**Low**
- `pkg/secrets/k8s.go`'s `k8sNameRE` allows `secretName`/`key` values
  starting with `-`; only `namespace` is checked against
  `AllowedNamespaces`. Unconfirmed as a live exploit (needs a real
  cluster to verify kubectl's flag-parsing behavior), but the asymmetry
  with `namespace`'s stricter treatment is real.
- `dlqResolveHandler` (`api/handlers_misc.go`) has no permission check
  beyond org membership — any `viewer` can resolve (mutate) DLQ entries.

**Checked and found sound** (not re-litigated here): JWT secret
generation/distribution (`InitJWTSecret`, shared correctly across k8s
replicas via one `Secret`), encryption-at-rest key handling (same
sharing pattern; the local/unconfigured-deployment key-next-to-db case
is a known, already-documented tradeoff), connection/variable secret
redaction on all list/get/update responses, `admin-reset-password`'s
authorization, plugin-archive extraction's zip-slip guard, `source_file`
/`sink_file`'s path allowlisting, every store-layer `ORDER BY`/`WHERE`
(fully parameterized), and every `exec.CommandContext` call outside the
k8s-secret-ref case above (argv arrays, no shell, sanitized job names).

## Decision

Two structural changes, plus the direct fixes for everything the
structural changes don't automatically cover.

### A — Org/workspace scoping moves into the data-access layer

Every store method that fetches, updates, or deletes a resource scoped
to an org or workspace (`GetWorkspace`, `GetPipeline`, `GetRun`,
`GetConnection`, workspace membership/token operations, ...) gains a
required `orgID`/`workspaceID` parameter that becomes part of the SQL
`WHERE` clause itself — not a post-fetch comparison a handler can
forget to perform. A mismatched scope returns "not found," identical to
the resource not existing, so there's no oracle distinguishing
"wrong org" from "doesn't exist." This is deliberately a data-layer
change, not a handler-layer one: a handler that forgets to pass the
caller's scope fails to compile (the parameter is required) rather than
silently querying unscoped and leaking data — the exact failure mode
behind the EE workspace findings and the core `handlers_run.go` gap.
`PermissionChecker.GetUserPermissions` gains real use of its
`workspaceID` parameter as part of the same change, closing the
finding that it currently ignores it outright.

### B — One SSRF-safe HTTP client, required for every outbound path

A single `pkg/netguard` package provides the only sanctioned way to
build an `http.Client` for a server-initiated outbound request:

- A custom `DialContext` resolves the hostname once and dials the
  resolved IP directly after validating it — the validated IP is what's
  connected to, not the hostname re-resolved a second time, closing the
  DNS-rebinding TOCTOU by construction rather than by re-checking
  faster.
- `CheckRedirect` re-validates every redirect target against the same
  policy before following it — a validated URL can no longer bypass the
  guard via a 3xx hop.
- Blocks loopback, link-local (including the `169.254.169.254` cloud
  metadata address specifically), and RFC1918 ranges by default; a
  caller opts into loopback explicitly (`AllowLoopback: true`) for the
  narrow, deliberate case `source_api`'s current comment describes,
  rather than that exception being the silent default every caller
  inherits whether they meant to or not.

`sink_api`, `fireHook`, connection-test (`testGeneric` and
`testHTTPAuth`), `source_api`/`RESTFetcher`, and every EE outbound
caller (Slack alerts, OpenLineage emitter, DB-notifier) migrate onto
this one client. `pkg/fetchers/rest_fetcher.go`'s `isBlockedHost` and
`api/handlers_connection.go`'s `validateExternalURL` — the two
near-identical, both-incomplete copies — are deleted in favor of the
shared implementation.

### Direct fixes bundled with the above

- `requirePerm`'s OSS fallback gets `requireAdmin`'s actual
  role-exclusivity check applied to plugin install, `system/purge`, and
  notification/webhook settings — the same fix already proven for
  templates, extended to where it was always supposed to cover.
- `quoteIdent()` rejects (not just wraps) identifiers containing the
  dialect's quote character, and `sink_db`'s `table` config is validated
  against a strict identifier pattern before reaching `GenerateSQL`.
- The migrate node's log line uses `Connection.BuildURI()`'s redacted
  form (`url.URL.Redacted()`, which Go provides for exactly this) instead
  of the plaintext-embedding `.String()`.
- `api/handlers_misc.go`'s webhook-trigger handler calls the existing
  `ValidateWebhookToken` instead of `!=`.
- `dlqResolveHandler` gets the same `requirePerm` check its sibling DLQ
  handlers already have.
- `pkg/secrets/k8s.go` applies `AllowedNamespaces`-equivalent strictness
  to `secretName`/`key`, or passes them via `--` / an explicit
  `-o jsonpath=...=` form that can't be reinterpreted as a flag,
  whichever a focused review of kubectl's argv parsing determines is
  sufficient.

## Consequences

### Positive

- The two most severe findings (#1 broken workspace isolation, and the
  SSRF paths) stop being "N handlers each need their own fix" and
  become "the thing that made them possible no longer compiles/no
  longer exists" — `GetWorkspace(id)` without a scope argument, or
  `&http.Client{}` built ad hoc for an outbound pipeline request, are
  both gone as available mistakes, not just fixed at today's call sites.
- Future resource types and outbound-request call sites inherit safety
  by construction instead of needing a security review to remember the
  pattern — exactly the gap that let four of nine findings exist despite
  the correct pattern already being used correctly elsewhere in the same
  files.
- Deleting the two duplicated, both-incomplete SSRF guards
  (`isBlockedHost` / `validateExternalURL`) means there's one thing to
  get right and one thing to audit going forward, not two copies that
  can drift.

### Negative

- Real migration cost: every store method touching a scoped resource
  changes signature, and every caller (all of `api/handlers_*.go`, both
  repos) updates to pass the caller's org/workspace — a large,
  mechanical, but non-trivial diff across two repositories.
- The custom `DialContext`/redirect-revalidation client is more complex
  than `http.DefaultClient` and needs its own focused tests (dial to a
  blocked IP is rejected; dial to an allowed IP that redirects to a
  blocked one is rejected mid-flight; loopback is blocked by default and
  reachable only with the explicit flag) before it's trusted to replace
  every outbound path at once.
- `AllowLoopback: true` still means *some* caller intentionally punches
  a hole in the guard — worth a follow-up audit of which callers
  actually need it (today, effectively just `source_api`'s
  self-reference convenience case) versus which just inherited it by
  copying that code.

### Deferred

- Per-request outbound rate limiting / an allowlist mode (deny by
  default, admin-approved host list) for outbound-capable node types in
  high-security deployments — a stronger posture than blocklisting
  private ranges, not decided here.
- Whether the k8s-secret-ref argv concern (Low, above) needs the
  `AllowedNamespaces` treatment or a different fix entirely depends on
  kubectl flag-parsing specifics not resolved in this ADR.
- A lint rule / CI check that fails a build introducing a new
  `&http.Client{}` outside `pkg/netguard` for outbound pipeline/hook
  traffic — valuable belt-and-suspenders on top of Decision B, not
  designed here.

## Alternatives considered

- **Patch each of the nine findings independently, no structural
  change** — the fastest path to closing today's known issues, and
  still worth doing for anything Decision A/B don't naturally cover
  (the Medium/Low findings above). Rejected as the *whole* answer:
  it leaves the exact pattern that produced four of the nine findings
  (a correct check that existed elsewhere in the same file, just not
  called here) fully intact for the next handler someone writes.
- **A per-handler-registered middleware that checks ownership**, instead
  of pushing the check into the store layer — closer to the existing
  `ValidateOrgAccess`/`DenyOrgAccess` pattern, less invasive to store
  signatures. Rejected because it's the same failure mode one layer up:
  a handler still has to remember to attach the middleware, and the EE
  workspace findings show handlers already don't. Store-layer
  enforcement fails closed (won't compile without a scope) instead of
  failing open (works fine until someone forgets).
- **Keep `isBlockedHost`/`validateExternalURL` as two implementations
  but fix both** — rejected per the audit's own observation that they're
  already two copies of the same logic with the same bugs; keeping them
  separate only guarantees the next fix has to happen twice, correctly,
  in sync, forever.

## Follow-ups

- Implementation order: Decision B (`pkg/netguard`, self-contained, no
  store signature changes) before Decision A (store-layer scoping,
  larger and more invasive) — B closes the SSRF criticals faster with
  less blast radius, A needs more careful migration planning across two
  repos.
- The direct fixes bundled above (RBAC fallback, `quoteIdent`, log
  redaction, webhook token compare, DLQ permission check, k8s secret-ref
  argv) are not blocked on A or B and can land independently, in
  parallel, as soon as this ADR is accepted.
- A follow-up audit of every `AllowLoopback: true` call site once
  Decision B lands (see Consequences → Negative).
- Test plan for Decision B before it's trusted for all outbound traffic:
  blocked-IP dial rejection, mid-redirect re-validation, and the
  loopback-default-blocked case, each as a real test against a local
  listener, not just unit-level URL-string checks.

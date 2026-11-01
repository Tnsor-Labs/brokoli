# ADR-049: One place for tenancy: a request scope enforced by the data layer

**Status:** accepted (approved by the product owner, 2026-11-01)
**Date:** 2026-11-01

## Context

An organization holds several workspaces, and a person may belong to some
of them and not others. Every piece of tenant data (pipelines, runs, logs,
alerts, triggers, change requests, ownership) belongs to one workspace,
usually through its pipeline.

Identity is already settled in one place. The workspace middleware takes
the requested workspace (`X-Workspace-ID`), refuses it when the caller does
not belong to it, and the resolver (`UserWorkspaceResolverFunc`) knows
every workspace the caller belongs to. What is not settled in one place is
**data access**: every handler re-derives the tenant rule by hand.

Measured on main today:

| | count |
| --- | --- |
| `ValidateOrgAccess` calls in core handlers | 26 |
| `userOwnsWorkspace` calls in core handlers | 15 |
| organization lookups (`GetOrgID*(r)`) in core handlers | 39 |
| store interface methods taking an `orgID` argument | 27 |
| `ValidateWorkspaceAccess` calls | 0 (defined, never used) |

Two checks per route, written by hand, means some routes have one. Found
from two screenshots of the same person's Home page in two workspaces:

- **Runs.** Every run route (get, logs, events, plan, previews, profile,
  grid, cancel, resume, backfill, dry run) authorizes through
  `validateRunAccess` / `pipelineInOrg`, which check the organization only.
  Someone who is not a member of a workspace can read its runs and logs,
  and act on them, by id.
- **Runs you started** (`/runs?started_by=me`) filters by organization, not
  workspace.
- **Alerts.** The list and the unread count filter on `org_id` only; the
  alerts table has no workspace, only `pipeline_id`.
- **Ownership and other extension lists** filter by organization the same
  way.

Each could be fixed where it is. The next route would repeat the mistake:
the pipeline routes got the workspace check (#856 era) and the run routes
next to them did not. The fix has to make the mistake hard to write.

## Decision

### 1. A `Scope` is computed once per request and carried in the context

```go
// package tenancy
type Scope struct {
    OrgID     string   // "" in the community edition: no tenancy to enforce
    Workspace string   // the current workspace, already checked by the middleware
    Member    []string // every workspace the caller belongs to
    Actor     Actor    // person, agent, worker, operator
}
func From(ctx context.Context) (Scope, bool)
```

It is built by the existing middleware chain, after authentication and the
workspace check, from facts the chain already has. It is the only
constructor outside tests. A request with no scope is refused, not
treated as unrestricted: the zero value allows nothing.

### 2. Tenant data is read and written only through a scoped store

```go
s := store.Scoped(scope)       // or store.For(r)
s.Runs().Get(id)               // ErrNotFound unless visible to the scope
s.Runs().StartedBy(userID, n)  // current workspace only
s.Alerts().List(q)             // current workspace only
s.Pipelines().Get(id)
```

Two rules, applied by the scoped store and nowhere else:

| access | rule | why |
| --- | --- | --- |
| **list** | the current workspace (`Scope.Workspace`) | what the workspace switcher promises: switching changes what you see |
| **by id** | any workspace the caller belongs to (`Scope.Member`) | a link to a run in another of your workspaces keeps working; a non-member gets 404, never 403, so an id's existence is not disclosed |

The organization check is implied by both (a workspace belongs to one
organization) and kept as a second condition in the SQL, so a bug in one
cannot widen the other.

### 3. Each resource declares how to find its workspace, once

```go
tenancy.Register("pipelines", tenancy.Direct("workspace_id"))
tenancy.Register("runs",      tenancy.Via("pipeline_id", "pipelines"))
tenancy.Register("alerts",    tenancy.Via("pipeline_id", "pipelines").AllowNull())
tenancy.Register("pipeline_ownership", tenancy.Via("pipeline_id", "pipelines"))
```

The scoped store builds its `WHERE` clause from that declaration with one
helper. Adding a resource is one line; there is no per-query hand-written
tenant filter to forget. `AllowNull` is explicit for rows that belong to
the organization rather than a workspace (an alert with no pipeline), and
rows whose parent is gone (an alert of a deleted pipeline) are invisible,
since their workspace cannot be established.

Organization-level resources (teams, roles, the organization profile,
audit, usage) are registered as `tenancy.Org()` and filter by organization
only, deliberately and visibly.

### 4. The raw store is for the system, and a gate keeps handlers off it

The unscoped store stays: the scheduler, workers, migrations and recovery
need every row. It is reached through `store.System()`, with a reason
string recorded in the call (`store.System("scheduler: due triggers")`).

A test walks the HTTP handler packages and fails when a handler calls a
tenant-data method on the system store. It starts with an allowlist of
today's call sites, which may only shrink; the gate must be observed
failing (a planted call) before it counts.

### 5. One isolation test covers every registered resource

Because resources register in one place, a single table-driven test runs
for all of them: two workspaces in one organization, a member of one,
a member of both, and someone from another organization. For each, list and
get-by-id are asserted. A new resource is covered by the test the moment it
is registered; there is no isolation test to forget to write.

### 6. Operators are explicit

Support operators and superadmins who must see across organizations get a
scope with `Actor: Operator`, built by the operator middleware only, and
every such read is written to the audit log. There is no "skip the check"
flag on the scoped store.

### 7. Database row-level security is a later layer, not this decision

PostgreSQL row-level security could enforce the same rules a second time,
with `SET LOCAL` from the scope in each transaction. It does not exist in
SQLite (the community default), and it complicates pooled connections, so
it is an optional later phase, not the mechanism.

## Phases

1. **Scope and the first resources.** `tenancy.Scope`, the middleware that
   builds it, the registry, and scoped `runs`, `alerts` and pipeline
   reads. Moving the run routes, `started_by=me` and the alert list and
   count onto it fixes every leak listed above, so the fix ships as the
   first use of the pattern rather than as patches.
2. **Migrate the rest** resource by resource (triggers, change requests,
   lineage, messages, extension stores), each removing its handler-level
   checks.
3. **Turn the gate on** once the allowlist is empty, and delete
   `ValidateWorkspaceAccess`, `pipelineInOrg` and the hand-written checks.
4. **Optional:** row-level security in PostgreSQL as defense in depth.

## Consequences

- One rule, one implementation, one test. A handler cannot express "every
  run in the organization" without asking for the system store by name,
  which the gate refuses.
- Store signatures change from `(orgID, ...)` to methods on a scoped
  store. That is a wide but mechanical change, done per resource across
  phase 2; both shapes coexist until phase 3.
- Extensions (enterprise) build their scope from the same context value
  and register their resources the same way, so tenancy is not
  re-implemented outside core.
- The by-id rule (any workspace you belong to) is a choice: stricter
  "current workspace only" would break links between a person's own
  workspaces for no isolation gain, since they may switch to it anyway.

## Decisions taken on approval

1. **By id:** any workspace the caller belongs to (membership), as proposed.
2. **Alerts of deleted pipelines:** hidden, as proposed; their workspace
   cannot be established.
3. **Row-level security:** kept as optional phase 4.

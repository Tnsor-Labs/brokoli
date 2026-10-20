# Change requests

A **change request** is a proposed new version of a pipeline that does not
run until a person applies it. It is how an agent changes a pipeline, and
how anyone who may propose but not edit (an operator, say) suggests one.

- **Proposing changes nothing that runs.** The request keeps the whole
  proposed definition, the version of the pipeline it was made against,
  and what validating it said.
- **A person decides.** Someone who may edit the pipeline sees the diff and
  the validation, and applies or declines. Applying is exactly a save from
  the editor: the pipeline becomes the proposed version, with a version
  message `change request: <title>`, and the previous version stays in its
  history.
- **Agents never apply**, whatever their role, and only propose with the
  `pipelines.propose` permission (in the built-in `agent` role).
- **Out of date is refused.** When the pipeline has changed since the
  request was made, applying it would undo those changes, so it cannot be
  applied: decline it and propose again from the current version.

This is phase 3 of [ADR-046](adr/046-agents-mcp-server-and-agent-identities.md).

## In the UI

A pipeline with open change requests lists them on its page, above its
runs, and the editor says so above the canvas (an edit saved there makes
them out of date). Each row shows the title, who proposed it ("agent
triage-bot, for Ada"), and whether it is out of date or not valid, and
opens the change's **review page** (`/pipelines/{id}/changes/{changeId}`):

- **Overview**: the description, rendered as Markdown (headings, lists,
  code blocks, quotes), and why it cannot be applied when it cannot.
- **Changes**: one card per node the change adds, changes or removes, and
  one for the pipeline's own settings and one for rewired connections.
  Scripts, SQL and other text fields show as a line diff with line
  numbers, cut to the changed lines and three lines around them ("Show
  the whole file" expands it); other settings show before and after.
- **Graph**: the pipeline as proposed, with added nodes and connections
  outlined in green, changed nodes in amber, and removed ones dashed in
  red where they were.
- **Test**: runs the current and the proposed pipelines side by side on
  the first 10 rows (see [Testing a change](#testing-a-change)) and shows,
  step by step, what the change does to the data: columns added or
  dropped, row counts, and values that differ, highlighted cell by cell.

**Apply** (for whoever may edit the pipeline, when it can be applied;
otherwise it says why not) and **Decline** (with an optional reason) sit
at the top of the page. Whoever proposed it can **Withdraw** it.

## Testing a change

A test previews both versions of the pipeline on a few rows, the way the
editor's **Preview** previews a saved one, and leaves nothing behind:

- **Nothing is written.** Steps that may act outside Brokoli are held
  back and shown as "held" with the reason: `sink_file`, `sink_db`,
  `sink_api`, `notify`, `migrate`, `bash`, `dbt`, `task` and `wait`
  nodes, any node declaring the `sink` capability, and node types the
  preview does not know. What follows a held step is "not run".
- **Nothing is recorded.** The preview runs in a scratch store that is
  thrown away: no run in the pipeline's history, no event, log or alert,
  and the pipeline's hooks are not called.
- **Sources stop at the first rows.** A CSV file is read until it has
  them; a database query streams and is cancelled once it has them (a
  native driver stops at them too); a paginated API stops fetching pages;
  a Python step that `emit()`s rows stops once it has emitted them. What
  cannot stop early is read whole: a JSON or Parquet file, a BigQuery
  query, a script that builds `output_data` as a list. A preview stops
  after 90 seconds.
- **Code runs.** Code steps run as proposed, in the same sandbox as a run
  and with the pipeline's connections and secrets.

Because a test runs the proposed code, **only a person starts one**, with
permission to run the pipeline (`pipelines.run`), after the page warns
that the change's code will run. An agent's call is refused. At most two
tests run at a time on a server; a third is answered `429`.

## From an agent (MCP)

```
propose_change   {"pipeline": "<id>", "definition": {...}, "title": "...", "description": "..."}
list_change_requests {"pipeline": "<id>", "status": "open"}
```

The agent reads the pipeline with `get_pipeline`, changes the definition,
checks it with `validate_pipeline`, and proposes it. `propose_change` is
the only MCP tool that is not read only; it is marked as such, so a client
that asks before changing anything asks here. Nodes that exist already
keep their place on the canvas: an agent works without the layout, and
the proposal takes the current positions of the nodes it keeps.

## Who may do what

| | Needs |
|---|---|
| Propose | `pipelines.propose` (admin, editor, operator, agent roles; in core, routes still refuse the operator role, issue #864) |
| See and list | the pipeline |
| Test | `pipelines.run`, and being a person |
| Apply | `pipelines.edit`, and being a person |
| Decline | `pipelines.edit`, or having proposed it (withdraw) |

## What is checked

When proposed:

- the body is a pipeline definition (decoded strictly, as a save is);
- the proposal is this pipeline's: its id, logical id, source, workspace
  and webhook token are the pipeline's, whatever the body said;
- something changes: a definition identical to the current one (empty and
  absent values count as equal) is refused with 422;
- validation runs (structure, cycles, node configuration, unless it is a
  draft) and its result is stored with the request. An invalid proposal
  is recorded, so the person deciding sees what is wrong, but cannot be
  applied.

When applied, the save's own checks run again (validation, dependency
scope and cycles); a request that would fail them is refused with 400
and stays open.

## Pipelines deployed from a repository

A pipeline whose definition lives in a repository is not changed by
applying a request in place: the next deployment would undo it. Core
refuses change requests for such pipelines (409, "propose the change
there"). A distribution that can carry the change to the repository sets
`api.ChangeRequestDeliverFunc`: the request is then recorded and
delivered (for instance as a pull request), its `delivery` says where,
applying it in Brokoli is refused ("the change is applied by merging it
there"), and the distribution records the outcome with
`api.SetChangeRequestOutcome`. A delivery that fails declines the request
with the reason.

## The API

```
POST /api/pipelines/{id}/changes                       {"pipeline": {...}, "title": "...", "description": "..."}
GET  /api/changes[?status=open|applied|declined]                the workspace's, across pipelines
GET  /api/pipelines/{id}/changes[?status=open|applied|declined]
GET  /api/pipelines/{id}/changes/{changeId}
POST /api/pipelines/{id}/changes/{changeId}/test      {"rows": 10}
POST /api/pipelines/{id}/changes/{changeId}/apply
POST /api/pipelines/{id}/changes/{changeId}/decline    {"reason": "..."}
```

`GET /api/changes` lists the change requests of the pipelines the caller
may see in the workspace of the request, newest first, at most 100, each
with `pipeline_name`: what a person has to review, wherever it was
proposed.

A change request (the lists leave out `proposed` and `diff`):

```json
{
  "id": "02d21c00fff38c42bf90806902337609",
  "pipeline_id": "01a15873-c159-76eb-a6d4-10ef9882799d",
  "title": "Run every 30 minutes",
  "description": "the source updates twice an hour",
  "status": "open",
  "stale": false,
  "base_version": 1,
  "validation": {"valid": true, "errors": []},
  "diff": {
    "pipeline": [{"field": "schedule", "before": "0 * * * *", "after": "*/30 * * * *"}],
    "nodes_added": [], "nodes_removed": [], "nodes_changed": [],
    "edges_added": [], "edges_removed": []
  },
  "created_by": "f550455c88225a392faba2e3526a9e88",
  "created_by_name": "triage-bot",
  "created_by_kind": "agent",
  "on_behalf_of": "ada",
  "created_at": "2026-10-20T08:35:44Z"
}
```

`diff.nodes_changed` lists each node that stays with its changes
(`name`, `type`, `config.<key>`); the diff compares definitions, not
canvas positions. Applying answers `{"pipeline": {...}, "version": 2}`;
once settled, a request carries `decided_by_name`, `decided_at`, `reason`
and, when applied, `applied_version`.

Testing answers both previews, every node of each in the pipeline's
order, with its status (`success`, `failed`, `skipped` for a branch not
taken, `held`, `not_run`), its columns and first rows, its error, and,
when it did not run, why:

```json
{
  "rows": 10,
  "current":  {"nodes": [{"node_id": "classify", "name": "Classify User", "type": "code",
                          "status": "success", "columns": ["user_id", "tier"], "rows": [{"user_id": 1, "tier": "high"}]},
                         {"node_id": "save", "name": "Save Report", "type": "sink_file",
                          "status": "held", "reason": "Not run in a preview: it writes a file"}]},
  "proposed": {"nodes": [...], "error": "node Classify User (classify) failed: ..."}
}
```

`error` is set on a preview that stopped; the nodes it reached are still
there. `rows` is between 1 and 50 (10 by default).

## Not yet

- **Refreshing** an out-of-date request onto the current version: decline
  it and propose again.
- **Testing from an agent** before proposing: a test runs the proposed
  code, so a person starts it.
- **Opening a proposal in the editor** to adjust it before applying.

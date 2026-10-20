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

A pipeline with open change requests shows them on its page, above its
runs: the title, who proposed it ("agent triage-bot, for Ada"), and
whether it is out of date or not valid. Opening one shows its description,
the diff in words, and any validation errors, with **Apply** (for whoever
may edit the pipeline, when it can be applied; otherwise it says why not)
and **Decline** (with an optional reason). Whoever proposed it can
**Withdraw** it.

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
GET  /api/pipelines/{id}/changes[?status=open|applied|declined]
GET  /api/pipelines/{id}/changes/{changeId}
POST /api/pipelines/{id}/changes/{changeId}/apply
POST /api/pipelines/{id}/changes/{changeId}/decline    {"reason": "..."}
```

A change request (the list leaves out `proposed` and `diff`):

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

## Not yet

- **Refreshing** an out-of-date request onto the current version: decline
  it and propose again.
- **A dry run** of the proposed version attached to the request: the dry
  run works on saved pipelines today.
- **Opening a proposal in the editor** to adjust it before applying.

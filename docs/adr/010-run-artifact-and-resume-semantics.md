# ADR-010: Run definition snapshots and durable artifact/resume semantics

**Status:** accepted
**Date:** 2026-08-04

## Context

ADR-009 established a crash-recovery baseline and explicitly deferred two
things to this issue (#8): artifact semantics and reproducible resume.
Before this change, both `RunPipeline`/`RunPipelineAsync` and `ResumeRun`
resolved the pipeline to execute via `store.GetPipeline` — the current,
mutable row — never a snapshot. A pipeline-version snapshot mechanism
already existed (`store.SavePipelineVersion` / `GetPipelineVersion`,
auto-saved on every UI edit) but nothing linked a `Run` to a specific
version, so editing a pipeline between a run and a later resume silently
changed which DAG the resume executed.

Separately, `ResumeRun` built a `skipNodes` set from node runs that
previously succeeded and passed it to `Runner`. `executeNode` returned
immediately for a skipped node, before ever writing that node's output into
the in-memory `outputs` map used to wire node inputs for the rest of the
run. Its downstream consumer then hit the nil-input fallback and received a
freshly synthesized **empty** dataset instead of an error or the real data
— a silent data-loss bug: every node downstream of a skipped node in a
resumed run got wrong output with no error raised anywhere.

## Decision

**Run definition snapshot.** `models.Run` gained `PipelineVersion int`,
populated once at run-creation time via `resolveRunPipelineVersion`, which
reads the current maximum `store.PipelineVersion` for the pipeline
(`ListPipelineVersions` returns newest-first) and auto-saves an initial
snapshot if none exists yet (e.g. a pipeline created but never edited).
`RunPipeline`, `RunPipelineAsync` (both the in-process and job-queue-enqueue
paths), and `ResumeRun` resolve the pipeline to execute via
`Engine.resolvePipelineForRun(pipelineID, version)`, which fetches the exact
`GetPipelineVersion` snapshot instead of the live row. A run with no
recorded version (`PipelineVersion <= 0` — a row created before this field
existed) falls back to the live pipeline, reproducing the previous
behavior for legacy rows instead of failing them.

`ExecuteQueuedRun` (the JobQueue claim/execute path) intentionally keeps
resolving the live pipeline at claim time — this is unchanged, existing,
tested behavior (`TestExecuteQueuedRunPersistsPreExecutionFailure` depends
on a pipeline edited after enqueue being picked up at claim time). It does
still record the `PipelineVersion` that was live at *enqueue* time on the
pending run row, for lineage/observability and so a later `ResumeRun` of
that run has something to pin to; pinning claim-time execution itself to a
snapshot is a natural follow-up but is out of scope here to avoid changing
tested semantics for a path the issue does not name explicitly.

**Durable per-node artifacts.** A new minimal `ArtifactStore` interface
(`engine/artifact_store.go`) persists a node's completed output keyed by
`(run_id, node_id)`, with `LocalDiskArtifactStore` as the only
implementation: one NDJSON file per artifact, reusing
`arrow_transfer.go`'s `WriteArrowJSON`/`ReadArrowJSON` — the closest
existing precedent for durable inter-node data transfer in this codebase
(otherwise used only by the code-node subprocess bridge). Every successful,
non-dry-run node write an artifact unless its node type is in
`nonResumableNodeTypes` (`notify`, `migrate`, `dbt` — side-effecting nodes
whose returned dataset, if any, isn't meaningful to hand to a downstream
node on resume). Writes go to a temp file and are renamed into place so a
crash or concurrent read never observes a partial artifact. The `(run_id,
node_id)` key is hashed (SHA-256, hex) into the filesystem path rather than
interpolated directly — node IDs come from pipeline JSON end users fully
control, so this closes path traversal at the type level instead of
relying on validation.

**Resume policy, per node type:**

| Situation | Resume behavior |
|---|---|
| Skipped node has a durable artifact | Restored into the `outputs` map — downstream nodes see the real prior data. |
| Skipped node has no artifact, no downstream consumers | Left unpopulated, same as before — safe, because nothing reads it (the common case for sink nodes, which return nil output on success). |
| Skipped node has no artifact, but has downstream consumers | **Resume fails loudly**, naming the node, instead of feeding downstream nodes empty/synthesized data. This is the fix for the bug above: the previous silent-empty-dataset substitution is no longer reachable from a skip. |
| Node type in `nonResumableNodeTypes` (`notify`, `migrate`, `dbt`) | Never gets a written artifact, so it always takes the above no-artifact path — loud failure if anything downstream depends on it, silent-safe skip otherwise. |

This makes "resumable" a property of *(node type, whether anything
downstream needs its output)*, not a static per-type allowlist alone —
source/transform/join/quality-check/sql-generate/condition/code nodes are
resumable whenever they produced a dataset; sinks are trivially resumable
(nothing downstream reads them); notify/migrate/dbt are treated as
non-resumable so a coincidental returned dataset is never silently reused
as if it were fresh data.

**Resume lineage.** `models.Run.ResumedFromRunID` records the run a resumed
run was resumed from. It also identifies which run's artifact store entries
a skipped node's output should be restored from — a skipped node's artifact
was written under the *original* run's ID, not the new resumed run's ID.
Both `PipelineVersion` and `ResumedFromRunID` are carried in the
`run.created` `RunEvent` payload and folded back by `engine.ProjectRun`, so
a full event-log replay reconstructs the same pinned-version/lineage
identity as the live `runs` row (extending #6's event log rather than
leaving these two fields as a gap only the direct DB row has).

## Consequences

### Positive

- A resumed run always executes the exact DAG snapshot the original run
  used, even if the pipeline has since been edited.
- Downstream nodes can no longer silently receive an empty dataset because
  an upstream node was skipped during resume — the failure mode for an
  unrecoverable node is now a loud, specific error.
- `Run.ResumedFromRunID` gives operators a traceable chain from a resumed
  run back to what it resumed, instead of a fully disconnected new row.
- `resolveRunPipelineVersion` turns `SavePipelineVersion`'s read-then-insert
  next-version computation from an occasional manual-edit/rollback race into
  a call made on every run trigger for a pipeline with no saved version yet
  — so this PR also adds a unique `(pipeline_id, version)` index and a
  bounded retry loop to both backends' `SavePipelineVersion`, closing a race
  that predates this PR but that this PR made realistic to hit.
- The artifact store's write path is additive and log-on-failure (mirrors
  this codebase's existing log-write-failure policy) — a storage hiccup
  writing an artifact does not fail the node's otherwise-successful run; it
  only means a *future* resume needing that artifact fails loudly instead
  of silently, which is the correct failure mode either way.

### Negative

- `LocalDiskArtifactStore` writes one file per successful node per run —
  unbounded local disk growth with no retention/GC policy in this PR. Left
  as explicit follow-up (see below); acceptable because it only affects
  operators who keep runs around long enough to matter, and matches the
  issue's "minimal implementation, not a full object-store abstraction"
  scope.
- `ExecuteQueuedRun`'s claim-time pipeline resolution is not pinned to a
  snapshot (see Decision above) — a pipeline edited in the enqueue-to-claim
  window still affects a queued run's execution, same as before this PR.

### Deferred

- Pluggable/object-store-backed `ArtifactStore` implementations (S3, GCS,
  a shared network volume) — the interface is intentionally small enough to
  support this without a breaking change, but provider selection is out of
  scope per the issue.
- Artifact retention/garbage collection.
- Pinning `ExecuteQueuedRun`'s claim-time execution to a snapshot (see
  Negative above).
- Startup reconciliation of runs left `running` by a crashed process (#9).

## Alternatives considered

- **Re-execute skipped nodes deterministically instead of persisting
  artifacts** — rejected: most node types (API/DB sources, anything
  touching external state) are not guaranteed deterministic or side-effect
  free to re-run, which is the entire reason resume skips them in the first
  place.
- **Treat all node types as uniformly resumable, capturing whatever they
  return** — rejected: `notify`/`migrate`/`dbt` exist for external side
  effects; treating a coincidental returned dataset as safe-to-reuse data
  conflates "this ran successfully" with "this produced meaningful data for
  downstream," which is exactly the class of confusion that caused the
  original bug.
- **Silently substitute empty data and only log a warning** — rejected:
  this is the status quo bug being fixed; a warning buried in logs does not
  prevent a corrupted pipeline output from being treated as valid.

## Follow-ups

- Object-store-backed `ArtifactStore` implementation.
- Artifact retention policy / garbage collection tied to run retention.
- Pin `ExecuteQueuedRun` to a `PipelineVersion` snapshot at claim time.
- Startup reconciliation (#9) should treat an unrestorable artifact for an
  in-flight run the same way ResumeRun does: loud failure, not silent
  corruption.

## Update (2026-08-05)

`Tnsor-Labs/brokoli#41` M2 added a narrower, parallel mechanism for a
different granularity of resume: `PaginationCheckpointStore`
(`engine/pagination_checkpoint_store.go`) persists mid-pagination progress
for a still-*running* `source_api` node's fetch — position plus records
accumulated so far — every `execution().checkpoint_every` pages, so a
node-level retry or a crash-triggered `ResumeRun` can continue a large
paginated fetch instead of restarting it from page one. It deliberately
does not reuse `ArtifactStore`: an artifact is a *completed* node's durable
output, checked once per node per run; a pagination checkpoint is interim
state written repeatedly during a single fetch and deleted the moment that
fetch succeeds, at which point `ArtifactStore` takes over exactly as it
already did for every other node. Same lineage pattern as this ADR's
`resumedFromRunID` fallback (a checkpoint lookup tries the current run
first, then the original run being resumed), same local-disk/hash-the-key/
temp-then-rename implementation shape as `LocalDiskArtifactStore`. See that
issue for the fetcher-side `CheckpointingFetcher` interface this builds on.

`Tnsor-Labs/brokoli#49` closed this ADR's "artifact retention policy /
garbage collection tied to run retention" follow-up (and the equivalent gap
`#41`'s checkpoint store shipped with): `ArtifactStore`/
`PaginationCheckpointStore` both gained a `DeleteRun*(runID)` method —
`os.RemoveAll` on the run's single hashed directory, since every
`(runID, nodeID)` key for one run already lives under it — and
`api.systemPurge` (`POST /api/system/purge`) now calls both after deleting
a run's DB rows, using two new `store.Store` methods
(`ListRunIDsOlderThan(By Org)`) that mirror `PurgeRunsOlderThan(By Org)`'s
`WHERE` clause to know which run IDs are about to disappear before they do.

`Tnsor-Labs/brokoli#52` fixed a real cost `#41` M2 flagged at merge time:
`PaginationCheckpointStore.SaveCheckpoint` used to take the *entire*
accumulated records list on every call and rewrite the whole records file
— checkpoint write cost grew with total progress, not with the interval
between checkpoints. `CheckpointSaver`/`SaveCheckpoint` now take only the
records added *since the last checkpoint*, and
`LocalDiskPaginationCheckpointStore` appends them instead of rewriting.
`PaginationCheckpoint.RecordCount` states the true running total
independent of how many append calls it took to get there; `LoadCheckpoint`
trusts only that many rows from the records file, truncating anything
beyond it (a save's append can legitimately land on disk before its
position commit does) and refusing to resume from a records file with
*fewer* rows than a committed position claims (real corruption, not the
ordinary partial-trailing-line case `ReadArrowJSON`'s decoder already
tolerates). Checkpoint saves also now target `sourceRunID` (the run whose
checkpoint was actually resumed from) rather than always the current run's
ID, so a cross-run resume keeps appending to the original run's existing
records file instead of needing to copy it forward first.

## Update (2026-08-09)

IR 2.1 condition attempts persist their selected boolean in the durable
`attempt.completed` event. Resume restores both the condition's unchanged data
artifact and that exact decision; it never reevaluates the expression against
new variables, parameters, run identity, or timestamps, which could execute
the opposite branch after side effects from the original route already
committed. A missing decision blocks resume rather than guessing.

Nodes made inactive by routing use the node-only `skipped` status. Their
`node_runs` row and `attempt.skipped` event are written in one transaction, so
snapshot reads, event projection, and startup recovery cannot disagree about
whether inactive work completed without execution.

Resume also records which skipped outcomes were reused and carries their
artifacts into the child run. If carrying that lineage fails, a later resume
walks `ResumedFromRunID` per node to the nearest durable successful ancestor;
it does not reexecute an already-successful side effect merely because an
intermediate resume failed before writing its reuse marker.

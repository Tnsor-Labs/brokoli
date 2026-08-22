# ADR-012: Artifact and dataset plane — start local-disk, reference-based, opt-in

**Status:** accepted
**Date:** 2026-08-04

## Context

Every node in a Brokoli pipeline today produces one in-memory Go value, `*common.DataSet`, and that whole value is held in memory and handed directly to the next node. There is no way to pass a *reference* to a large result instead of the result itself.

This is fine for small and medium pipelines, but it breaks down exactly where the RFC's motivating example (`BROKOLI_NEXT_ARCHITECTURE_PROPOSAL_V2.md`, §5.9) predicted it would: a multi-gigabyte result duplicates in memory every time it's touched, a retry has to redo the full fetch/transform because there's nothing durable to resume from, and the control-plane database has no way to stay small as data volume grows, because nothing is designed to move data by reference.

We already have one narrow piece of durable output storage: `engine/artifact_store.go`'s `LocalDiskArtifactStore`, added for [#8](https://github.com/Tnsor-Labs/brokoli/issues/8) specifically so a resumed run can restore a completed node's real output instead of silently substituting an empty dataset. It works, but it's scoped to that one job — it's not a general "pass this by reference" mechanism nodes can use for their own output, and nothing else in the engine knows about it.

We also already have a documented stand-in that makes the gap visible: `source_api(..., response="artifact")` (added for [#30](https://github.com/Tnsor-Labs/brokoli/issues/30)) doesn't produce a real artifact reference at all. Its own code comment says so directly — `pkg/fetchers/rest_fetcher.go`'s `artifactDataSet` wraps the raw response body as a one-row, one-column dataset, with the comment: *"There is no dedicated artifact/blob type on the Go side (unlike the SDK's `ArtifactRef`) — this is the minimal representation."*

So: the SDK already has the vocabulary for this (`ScalarRef`, `ArtifactRef`, `DatasetRef`, `CollectionRef` — merged in `brokoli-sdk#2`). The backend doesn't have anything real behind two of those four types yet.

## Decision

Start small and reference-based, building on what `LocalDiskArtifactStore` already proved out, rather than designing a full manifest/Parquet/multi-backend system up front:

1. Define `ArtifactRef`/`DatasetRef` as real Go types (URI, media type, size, checksum for artifacts; a manifest reference, format, schema, row/byte counts for datasets) — the shape the RFC sketches in §9.1–§9.3 is a reasonable starting point, but treat the exact field list as open for discussion, not fixed by this ADR.
2. Generalize `LocalDiskArtifactStore` into a small `ArtifactStore` interface with that one implementation as the only backend for the first milestone. Local disk is not a real answer for a multi-node or Kubernetes deployment, but it lets every other piece of this (the reference types, the spill logic, the SDK wiring) get built and tested before anyone has to pick a cloud storage SDK.
3. Make the move from inline to reference-based **automatic and threshold-based**, not something a pipeline author has to opt into per node — a `*common.DataSet` that crosses a configurable size threshold spills to the artifact store and gets replaced with a reference, everything smaller stays inline exactly as today. This is what RFC §9.5 calls for, and it means existing pipelines don't need to change to benefit.

## Consequences

### Positive

- Retries can reuse an already-produced large result instead of recomputing it, the same win `LocalDiskArtifactStore` already delivers for resume, generalized to every node.
- `response="artifact"` can finally mean what it says instead of being a documented workaround.
- Memory use stops scaling with the largest dataset a pipeline ever touches.
- Because this builds on `LocalDiskArtifactStore` rather than replacing it, [#8](https://github.com/Tnsor-Labs/brokoli/issues/8)'s resume behavior keeps working throughout.

### Negative

- A new subsystem to keep consistent — every node handler that currently returns a `*common.DataSet` directly needs to become aware that its input might now be a reference it has to resolve first.
- Local-disk-only doesn't solve the problem for a real multi-replica or Kubernetes deployment (a worker on pod B can't read a file spilled by pod A). That's an explicit, acknowledged gap in this first milestone, not an oversight — see Deferred below.

### Deferred

- A cloud-storage-backed `ArtifactStore` implementation (S3-compatible is the obvious first choice, given the rest of the codebase already assumes S3-shaped config in a few places).
- A real dataset format. The first milestone can treat a "dataset" reference as still being a serialized `*common.DataSet`-shaped file for simplicity; Parquet/Arrow support (RFC §9.3) is valuable but shouldn't block getting reference-passing working.
- Retention policy and cleanup of spilled artifacts.
- Any UI surface for browsing/downloading artifacts (RFC §21.4) — no UI work has happened in this project yet; that's tracked separately, not assumed here.

## Alternatives considered

- **Do nothing, keep everything in-memory.** Rejected — this is the exact problem RFC §5.9 describes, and it's the reason `response="artifact"` is currently a documented shim rather than a real feature.
- **Go straight to S3 + Parquet as the only supported path.** Rejected as too large a first step — it forces every contributor working on this to also stand up cloud storage for local development, and there's no reason the reference-passing mechanism itself needs to wait on that choice.
- **Build a new, separate manifest/reference system instead of extending `LocalDiskArtifactStore`.** Rejected — that store already solved the hard part (durable per-node output persistence, wired into resume) for one case; duplicating it instead of generalizing it would leave two overlapping systems to keep in sync.

## Follow-ups

- Tracked in [Phase 2: artifact and dataset plane](https://github.com/Tnsor-Labs/brokoli/issues) (see the companion issue opened alongside this ADR).
- Whoever picks up a milestone from that issue should come back and update this ADR — either fill in the "Deferred" items as they're resolved, or add a dated "Update" section recording what was actually decided once real backend/format choices are made.

## Update — 2026-08-09: milestones 1 and 2 shipped

The reference types and the store landed together (Tnsor-Labs/brokoli#38, M1 and M2). What was decided while building them, recorded here so the next milestone does not have to re-derive it:

**The Go reference types are not the SDK's.** This ADR and the companion issue both frame the work as "the SDK has four typed references, Go only has two real ones". That framing is misleading and the code now says so explicitly. The SDK's `ScalarRef`/`ArtifactRef`/`DatasetRef`/`CollectionRef` subclass `NodeRef`, which holds `(node_id, pipeline)` and implements `>>` chaining — they are authoring-time handles naming a node in a DAG being built, and carry nothing about the data. The Go types are runtime pointers to bytes that already exist. Nothing serializes between them and nothing should. The names were kept, because they describe the same two shapes of result from opposite ends of the system, but `pkg/artifact`'s package comment states the distinction so nobody reads one as the wire form of the other.

**`DatasetRef` embeds `ArtifactRef`** rather than duplicating its fields: a dataset is bytes in a store plus what is needed to treat them as rows without reading them (format, column order, row count).

**References are always wrapped in a versioned `Manifest` for storage.** A bare reference on disk has no way to say which schema produced it. The version is checked on every read, and an unrecognized one is refused rather than read optimistically — a reference points at data a pipeline is about to consume as its input.

**Blobs are namespaced per run, not shared globally.** Content addressing would otherwise make deletion require reference counting, and `DeleteRunArtifacts` (the retention counterpart added for #49) has to be able to reclaim every byte a run wrote. Deduplication therefore applies within a run, which is where a pipeline actually repeats itself. Two nodes producing identical output share one blob and keep separate manifests.

**Checksum mismatch is a distinct error from absence.** `restoreSkippedNodeOutput` treats "not found" as a recoverable miss, so corrupted output must not take that path; `artifact.ErrChecksumMismatch` and `ErrArtifactNotFound` stay separate all the way up.

**`LocalDiskArtifactStore` was re-expressed on top of the new store rather than replaced.** It keeps the `(runID, nodeID)` keyed lookup that resume needs and delegates the bytes, sharing one base directory so purging a run still removes manifests and blobs in a single call. Artifacts written in the pre-manifest layout are still read, so a run already in flight when a binary is upgraded can still be resumed. #8's behaviour is unchanged, and its tests pass untouched.

One incidental fix: the manifest records column order, which NDJSON rows do not preserve. The old read path recovered columns by iterating the first row's map and returned them in an arbitrary order; a restored dataset now comes back with the column order it was written with.

Still deferred after M1/M2: automatic threshold-based spill (M3), a cloud-storage backend, Parquet, and retention beyond per-run deletion.

## Update — 2026-08-09: milestone 4 shipped

`source_api(..., response="artifact")` now stores its response body and returns a reference to it, replacing the one-row-dataset shim this ADR quoted as the visible symptom of the missing plane.

**The reference still travels inside a `*common.DataSet`.** Making a reference a first-class node output is M3, which has not happened; a node can still only return a dataset. What changed is what the row contains: where the bytes are (URI, media type, size, checksum) rather than the bytes themselves. This is a behaviour change for anyone consuming `response="artifact"` today — the body is no longer inlined under `value`.

**Without an artifact store, the previous inline behaviour is kept exactly.** Validation, dry runs and any caller constructing a bare fetcher have nowhere to store to, and for them the body itself is the honest result. The capability is offered through an optional interface the engine type-asserts, mirroring how `CheckpointingFetcher` already works, so a fetcher that cannot store by reference is still a valid fetcher.

**A store failure fails the node.** Falling back to inlining a body that was explicitly asked to be stored would defeat the reason for asking, and would do it precisely when the body is largest.

The sink a fetcher receives is scoped to the running run, so fetched artifacts share the run's namespace with node outputs and are reclaimed by the same `DeleteRunArtifacts` call.

## Update — 2026-08-09: milestone 3 shipped, and one prediction in this ADR was wrong

Node outputs above a size threshold now spill to the artifact store and are read back on demand, completing the behavioural half of the plane.

**The Negative consequence this ADR records did not materialise, and the design is better for it.** This document says "every node handler that currently returns a `*common.DataSet` directly needs to become aware that its input might now be a reference it has to resolve first". No node handler changed. Between nodes, results live in exactly one place — a single map inside `Runner.executeRun`, with two write sites and one read site — so spilling fits entirely behind that boundary. Outputs go in as `*common.DataSet` and come out as `*common.DataSet`; whether the bytes sat in memory or on disk in between is not observable from a handler. One implementation to get right instead of one per node type, and the node contract is untouched.

That is worth stating plainly because the predicted cost was a real argument against doing this at all.

**Spilling is on by default, with a high threshold** (64 MiB estimated encoded size, `BROKOLI_SPILL_THRESHOLD_BYTES`). The issue asks for existing pipelines to benefit without changing, which argues for on; the problem being solved is memory scaling with the largest result a pipeline touches, which a few megabytes of rows is not. Below the threshold, behaviour is exactly what it was. A negative value disables spilling entirely.

**The size check is an estimate, and named as one.** Up to 32 rows are encoded and averaged out. Encoding everything to decide whether to encode everything would double the serialization cost for the datasets that never spill, which is all of them in an ordinary pipeline. Uneven row sizes will be misjudged; the consequence is holding something that could have spilled or spilling something that need not have, never incorrect data.

**A spill that fails keeps the data in memory and continues.** The dataset is already correct and in hand, and failing a pipeline because an optimisation could not be applied would turn a full disk into a failed run. **A spilled output that cannot be read back is the opposite — a hard failure.** It was the only copy, and substituting an empty dataset is precisely the data-loss shape #8 exists to prevent.

Spilled outputs share the run's namespace with node artifacts, so `DeleteRunArtifacts` reclaims them with everything else.

Deferred after M1–M4: a cloud-storage backend, Parquet, and retention beyond per-run deletion. M5 remains the stretch goal.

**Unrelated finding, recorded because the next person will hit it:** `Engine` has no `Stop`/`Close`, so its background work — dependency trigger mode in particular — keeps running after a test returns and can write into a `t.TempDir()` while cleanup is removing it. The spill tests use a manually-cleaned directory to keep that flakiness out of their assertions. Giving `Engine` a shutdown is the real fix.

## Update — 2026-08-18: promoted to accepted

M1–M4 are shipped and load-bearing: reference types, `ArtifactStore`, threshold-based spill, and `response="artifact"` all ship the design this ADR describes, and ADR-017's remote-instance-dispatch work builds directly on top of it (its own 2026-08-12 updates name this ADR explicitly as the plane its output-delivery design reuses). A second backend — `engine.SQLArtifactStore`, added to close [#161](https://github.com/Tnsor-Labs/brokoli/issues/161) — proves the `ArtifactStore` interface itself was the right cut: adding a cross-pod-visible backend required no change to the reference types, the manifest format, or any caller.

Still Deferred, and now scoped to their own future work rather than blocking this ADR: a cloud-storage (S3-compatible) backend, Parquet/Arrow dataset format, retention beyond per-run deletion, and any artifact-browsing UI.

## Update — 2026-08-22: cloud-storage backend, `pkg/artifact.Store` only

[#268](https://github.com/Tnsor-Labs/brokoli/issues/268) closes the first half of the "cloud-storage backend" Deferred item: `pkg/artifact.S3Store` is a third `Store` implementation (`s3.go`), following the same shape `LocalDiskStore` established — content-addressed, namespace-scoped, no tenant or multi-bucket awareness. Two things worth recording about how it satisfies the streaming requirement this ADR cares about most:

- **Writes stream, not buffer.** The digest still isn't known until every byte is read (same as local disk), so a write goes to a temporary key while hashing, then is published under its content-addressed key via a server-side `CopyObject` (no data re-transfers) followed by deleting the temporary key — the closest available equivalent to `LocalDiskStore.Put`'s temp-file-then-rename, since S3 has no atomic rename. The actual upload streams through `manager.Uploader` in bounded, part-sized chunks, verified by a real test (`TestS3Store_PutStreamsRatherThanBuffers`) that tracks the largest single read request the SDK ever makes, rather than trusting code review alone.
- **Reads stream too, with a real tradeoff.** `LocalDiskStore.Open` verifies a checksum eagerly (cheap on local disk) before returning any bytes. Doing the same against S3 would mean buffering a full download before the caller sees a single byte — reintroducing the exact problem this ADR exists to prevent, just on the read side. `S3Store.Open` instead verifies incrementally: bytes are hashed as the caller reads them, and a mismatch surfaces as `ErrChecksumMismatch` from the read call that would otherwise report `io.EOF`. A caller that reads the whole stream gets the same correctness guarantee; a caller that reads only part and stops will not learn whether the unread remainder was corrupted. Recorded here as a deliberate, disclosed choice, not an oversight.

**What this does not include, and why:** wiring this into `engine.ArtifactStore` (the interface `LocalDiskArtifactStore`/`SQLArtifactStore` implement) was originally scoped as this same issue's M2. Reading `LocalDiskArtifactStore` in full changed that plan — its manifest layer is inseparable from `WriteArtifactFenced`'s fencing-generation safety (the exact mechanism `#240` found a real gap in once already) and a legacy pre-manifest read fallback specific to the local-disk file layout. Building a second, independent implementation of that fencing contract without the same scrutiny the original got risks quietly reintroducing the class of bug `#240` fixed. `pkg/artifact.S3Store` is deliberately scoped to the low-level `Store` interface only — content in, content out, no manifest/fencing semantics at all — and left for whoever builds a full `engine.ArtifactStore` on top of it to design that layer with its own real design attention, the same way `SQLArtifactStore` got its own.
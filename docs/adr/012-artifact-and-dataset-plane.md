# ADR-012: Artifact and dataset plane — start local-disk, reference-based, opt-in

**Status:** proposed
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

Still deferred, unchanged by this work: automatic threshold-based spill (M3), `response="artifact"` producing a real reference (M4), a cloud-storage backend, Parquet, and retention beyond per-run deletion.

# ADR-038: Where staged task bytes live

**Status:** proposed
**Date:** 2026-09-13

## Context

A task node whose input exceeds the inline row cap has its rows staged
in a blob store and dispatched as a content digest plus a capability.
The claiming worker fetches those bytes back from the control plane.

Run against a real fleet on 2026-09-13, that fetch returns 404. Not 401:
the three auth gates are open, the worker presents a credential, the
capability checks out, and the object is simply not where the reader
looked.

`spillTaskInput` writes through `Runner.taskBlobStore()`, which is
`BlobStoreProvider.Blobs()`. For `SQLArtifactStore`, the store a
distributed deployment uses, that returns a local-disk store. Its own
comment explains the choice, and states the assumption that has since
stopped holding:

> Backed by local disk, not the SQL database `WriteArtifact`/
> `ReadArtifact` use: spill scratch space is read back only by the same
> run, on the same pod, within the same process, it has no cross-pod
> visibility requirement.

True for spill. False for task staging, where the writer and the reader
are different pods by construction. Two uses with different visibility
requirements share one store, and the one that needs cross-pod
visibility silently gets the store that cannot provide it.

What exists today:

| Store | Cross-pod | Currently used for |
| --- | --- | --- |
| `LocalDiskStore` | no | spill scratch, and task staging |
| SQL `artifacts` table | yes | `WriteArtifact`/`ReadArtifact`, not a `pkg/artifact.Store` |
| `S3Store` | yes | nothing: `NewS3Store` has zero non-test callers |

ADR-012 deferred a cloud-storage backend four times and, when `S3Store`
landed, deliberately scoped it to the low-level `Store` interface with no
manifest or fencing semantics, because that layer is inseparable from
`WriteArtifactFenced`'s safety and deserved its own design attention.
That reasoning still holds and this ADR does not disturb it.

## Decision

**Separate the two uses. Intra-run spill keeps local disk. Anything a
different process will read goes to a blob store declared cross-pod, and
staging refuses rather than writing somewhere the reader cannot look.**

Concretely:

1. **A second accessor alongside `Blobs()`**, for "a store another
   process can read". `SQLArtifactStore` answers with a cross-pod store
   when the deployment has configured one, and with nothing when it has
   not. Spill continues to call `Blobs()` and is unaffected.

2. **Staging refuses when no cross-pod store is configured**, naming
   what is missing. A 404 three hops later, after a capability was
   minted and a work order dispatched, is the least diagnosable outcome
   available; core #529's rule says a degradation path must fail or log,
   never quietly succeed. Today it quietly succeeds and fails later.

3. **`S3Store` becomes constructible from configuration.** It is the
   cross-pod store this needs and nothing instantiates it. Configuration,
   not a new abstraction: `S3StoreConfig` already exists and `#561` gave
   it `ResolveDigest`, which is what the capability read path requires.

4. **The SQL table is not extended to carry blobs.** Task inputs are
   bounded by `maxTaskDatasetBytes` at 64 MiB, and putting payloads of
   that size in a control-plane table trades a clear failure for write
   load, row bloat and vacuum pressure on the database every pod shares.
   ADR-012 already declined this for spill and the argument is stronger
   here, because task staging is per-attempt rather than per-run.

## Consequences

### Positive

- Reference-based task input works across pods, which is the only shape
  it was ever for. Battle-test scenario 6 becomes passable.
- A deployment that has not configured object storage learns at staging
  time, with a message naming the gap, instead of at fetch time as a
  404 from a component that did nothing wrong.
- The two visibility requirements stop being conflated, so the next
  cross-pod reader cannot inherit the same surprise.
- `S3Store` stops being code nothing runs.

### Negative

- Distributed deployments gain a required piece of configuration. That
  is a real operational cost, and the honest alternative is a feature
  that only works on one pod.
- Two blob stores in one process is more to reason about than one.
- Object storage in the loop adds a failure mode that local disk does
  not have, and its latency is on the path of every staged input.

### Deferred

- **A cloud-storage `engine.ArtifactStore`**, with manifests and
  fencing. Still ADR-012's deferral, still gated on giving
  `WriteArtifactFenced`'s contract its own design attention.
- **Retention and cleanup of staged inputs.** Per-run deletion covers
  artifacts; staged task inputs are per-attempt and need their own
  answer.
- **Whether a worker outside our administrative boundary should resolve
  references locally instead of through the control plane.** ADR-033
  named this a deployment-topology question and left it open; it stays
  open.

## Alternatives considered

- **Put staged bytes in the SQL `artifacts` table.** Cross-pod
  immediately, no new configuration, and the reason ADR-012 rejected it
  for spill applies more strongly here: 64 MiB payloads per attempt on
  the database every pod shares.
- **Make the worker fetch from the writer pod directly.** Requires
  pod-to-pod addressing the architecture does not have, and ADR-033
  section 6 forbids handing a worker a location rather than a
  capability.
- **Keep local disk and document the limitation.** This is the status
  quo, and the status quo is a 404 that took a live fleet, three merged
  auth fixes and a new credential type to reach.
- **Always require object storage.** Simplest to reason about and wrong
  for single-node installs, which are most of them and for which local
  disk is exactly right.

## Follow-ups

- #572, the defect this decision resolves.
- Battle-test scenario 6 passing against a real fleet is the acceptance
  gate.
- ADR-012 gains an update recording that its "no cross-pod visibility
  requirement" note now has an exception.

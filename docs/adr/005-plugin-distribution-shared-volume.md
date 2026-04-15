# ADR-005: Plugin distribution via shared volume

**Status:** accepted
**Date:** 2026-04-15

## Context

ADR-002 establishes the plugin protocol and ADR-004 puts the runtime
in OSS. A third question stayed open: **how do plugins physically get
from the admin's control plane to the worker processes that actually
execute pipeline nodes?**

In all-in-one mode the answer is trivial: the API process and the
execution engine are the same Go binary, so plugins in
`~/.brokoli/plugins/` are directly accessible. The problem is
distributed mode.

When a pipeline run is dispatched to a managed pool, the provisioner
spawns a fresh Kubernetes Job from the base `brokoli` image. That
Job's container has no connection to the API pod's filesystem.
Whatever the user `brokoli plugins install`'d on the control plane
is invisible to the worker pod. Result: `no plugin named snowflake`
at execution time.

Same story — in a milder form — for long-lived worker deployments
(`brokoli-worker-*`): they're separate pods from `brokoli-api-*`,
and pods don't share filesystems.

We need a distribution model that's:

1. **Simple enough to ship Phase 1** without building a registry or
   new infrastructure
2. **Correct** — workers see the same plugins the API sees
3. **Scalable later** — the simple model shouldn't paint us into a
   corner when we need to support multi-node k8s clusters or
   air-gapped deploys

## Decision

**Use a shared volume mounted on both the API pod and every worker
pod (long-lived deployments + ephemeral managed worker Jobs).** The
volume is rooted at `/var/lib/brokoli/plugins` inside the container
and backed by a `hostPath` volume on the k8s node by default.

- The **API pod** mounts it read-write so `brokoli plugins install`
  writes new plugins into it.
- The **long-lived worker deployments** mount it read-only.
- The **managed worker Job template** in `ee/k8s/provisioner.go`
  injects the same volume mount into every spawned ephemeral
  worker pod, so managed pool runs see the plugins too.

Default storage is `hostPath` pointing at `/var/lib/brokoli-plugins`
on the host node — which works fine on single-node k3s setups (our
current production shape) without requiring RWX storage. For
multi-node clusters, operators override the volume source with a
ReadWriteMany PVC (NFS, EFS, CephFS, etc.) via a Helm chart value.

Across all deployment modes, the `BROKOLI_PLUGIN_DIR` environment
variable (already supported by `pkg/plugins/Manager.DefaultDir()` as
of v0.8.0) points the runtime at the mount path:

```
Deployment mode      Plugin directory                         How
──────────────────────────────────────────────────────────────────────────
All-in-one           ~/.brokoli/plugins                       default
Docker Compose       /var/lib/brokoli/plugins (host bind mt)  env var
k3s single-node      /var/lib/brokoli/plugins (hostPath)      chart value
Multi-node k8s       /var/lib/brokoli/plugins (RWX PVC)       chart value override
Air-gapped / SecOps  /var/lib/brokoli/plugins (baked into     custom image
                      the container image itself)
```

Every mode resolves to the same path inside the container. The only
difference is where the bits physically live underneath.

## Consequences

### Positive

- **Zero new infrastructure for Phase 1.** No plugin registry, no
  init containers, no OCI artifact layer. Drop files in a
  directory, done.
- **One mental model for admins.** "Put plugins in this dir." Same
  answer regardless of whether they're running on their laptop,
  docker-compose, k3s, or a real cluster. Self-hosted admins in
  particular get a brain-dead simple deployment story — `sudo cp`
  and move on.
- **Zero cold-start penalty.** The plugin is already on the node's
  disk when a managed worker starts up; no download step. This
  matters because the managed worker lifecycle is already short,
  and anything that adds startup latency compounds per run.
- **Reuses all the OSS plugin runtime.** No new code path in the
  worker or the API — the same `plugins.NewManager(DefaultDir())`
  call used in all-in-one mode works in distributed mode.
- **Migration path to RWX or baked image is straightforward.** The
  volume source is a chart value; switching from hostPath to a
  real PVC is a one-line values change. Switching to a baked image
  is "build your own worker image from the base, drop plugins in
  at `/var/lib/brokoli/plugins`" — documented as the air-gap
  escape hatch.

### Negative

- **hostPath defaults only work on single-node clusters.** We
  document this clearly. Multi-node operators must opt into a RWX
  volume or a baked image, and both come with their own
  operational overhead (RWX storage classes are not free; baked
  images require the user to manage their own container registry).
- **`hostPath` bypasses k8s scheduling constraints.** A node
  failure makes plugins inaccessible until the node recovers.
  Acceptable for our current single-node deploy; revisit when we
  support HA.
- **No plugin-level access control.** Every worker pod mounted with
  the volume sees every installed plugin. Fine for a trust
  boundary that's already "the API pod and its workers are all one
  org" but wrong for a future "different workers for different
  orgs" model. See ADR-EE-003 on multi-tenant governance.
- **Plugin reload requires a worker restart** for long-lived worker
  pods. New managed workers pick up new plugins automatically
  (they spawn fresh). Persistent workers need a
  `kubectl rollout restart` when a plugin is upgraded. Documented.

### Deferred

- **API-served on-demand fetch** (Option D from the original design
  discussion) — deferred. It's the right answer for multi-node k8s
  or SaaS deployments where a shared volume isn't available. When
  we hit that need, the `Manager` can grow a fetch-if-missing step
  driven by a new `POST /api/plugins/{name}/archive` endpoint.
- **Plugin hot-reload** — deferred. The manager already supports
  `LoadAll()` at runtime, so adding a filesystem watcher is a
  small next step, but not critical for Phase 1.
- **Multi-node RWX defaults** — deferred. No changes to the
  runtime; just a chart template addition when we get there.

## Alternatives considered

- **Bake plugins into the worker image** (custom base image) — kept
  as the air-gap escape hatch, not the default. Rejected as the
  default because users have to rebuild and publish images on
  every plugin change, which is brutal cloud DX. Kept available
  because it's the only sane option for air-gapped or
  strictly-signed environments.
- **ReadWriteMany PVC everywhere** — rejected as the default
  because RWX storage classes are expensive, rarely available on
  vanilla k8s setups, and force users to pick and configure an NFS
  / EFS / CephFS provider before they can run Brokoli. We support
  it as an override.
- **Init container fetches plugins from an OCI registry on every
  worker spawn** — considered. Cleanest for multi-node, but
  requires a registry we don't have and adds 2-5 s to worker cold
  start. Deferred to Phase 3 as an optimization for clusters where
  the shared volume isn't viable.
- **API pod serves plugins over HTTP, workers fetch on demand at
  job claim time** — considered and documented. Works for multi-
  node without RWX storage, but adds an HTTP endpoint, cache
  management, and startup latency. Queued as the natural "next
  step" when shared-volume limits bite.
- **Each plugin is its own Docker image, nodes spawn plugin pods**
  (Airbyte model) — rejected for Phase 1. Massive overhead (a 10-
  node pipeline becomes 10 pod spawns) and doesn't match the way
  Brokoli pipelines execute.

## Follow-ups

- Update the Helm chart to add a plugin volume to the API and
  worker deployments (OSS repo follow-up).
- Update `ee/k8s/provisioner.go`'s `buildWorkerManifest` to inject
  the same volume into managed worker Job specs (EE repo follow-
  up, see ADR-EE-002 update).
- Write a user-facing "plugin distribution" docs page explaining
  the model for each deployment mode.
- When multi-node deployments become a real use case: implement
  API-served on-demand fetch as a fallback when the shared volume
  isn't present. The runtime already has the hooks for this
  (Manager.LoadAll is callable at runtime).

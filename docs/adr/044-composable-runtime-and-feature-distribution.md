# ADR-044: Composable runtime and feature distribution

**Status:** proposed
**Date:** 2026-10-04

## Context

`brokoli serve` is intentionally simple: one binary can host the API/control
plane, scheduler, execution engine, local workers, and installed plugins. The
same binary also links every native connector and its transitive driver
dependencies. That is a good first install experience, but it has two costs:

- A user who needs only PostgreSQL and files still downloads, scans, and ships
  drivers for databases they will never use.
- A change to one component can require understanding or rebuilding a binary
  that also owns unrelated concerns: HTTP API, persistence, scheduling,
  worker admission, connector drivers, and user-facing CLI commands.

The first cost becomes operationally important as native drivers bring large
dependency trees, CGO requirements, security advisories, and support matrices.
The second becomes a contributor and deployment cost, especially when workers
need a different capability set from a scheduler or API replica.

This is not a reason to turn every package into a service. A distributed
deployment already has real component boundaries: `--mode scheduler` avoids
worker execution, `--mode worker` claims remote work, and the worker protocol,
attempt leases, fencing, and connection-reference resolution are durable
contracts (ADRs 011 and 017). The all-in-one mode must remain useful for local
development and small self-hosted deployments.

### Existing decisions this preserves

- ADR-004 keeps the plugin runtime in OSS; optional features cannot become an
  enterprise-only substitute for a usable OSS install.
- ADR-005 establishes plugin distribution without requiring a registry in the
  default path. Feature distribution can extend this model, but must not make a
  shared volume or Kubernetes mandatory for a local install.
- ADR-011 separates scheduler leadership from API serving in HA deployments.
- ADR-017 defines worker dispatch, leases, fencing, and connection references;
  role separation must reuse these semantics rather than create a second work
  system.
- ADR-022 owns network policy and tenant isolation. A feature pack or remote
  worker cannot become an unguarded outbound-network escape hatch.
- ADR-024 requires a backend capability to be earned through implementation and
  tests, not inferred from an installed dependency.

### Goals

1. `brokoli serve` remains a one-command, batteries-included default.
2. Operators can run API/control-plane, scheduler, and worker roles separately
   without a forked product or a second configuration model.
3. New optional connectors and runtimes need not enlarge the default binary.
4. A worker advertises and receives only capabilities it has installed.
5. Installation, upgrades, diagnostics, and offline deployments remain simpler
   than operating a collection of mandatory microservices.
6. Optional distribution cannot weaken signature, credential, network, or
   tenant boundaries.

### Non-goals

- Splitting every internal package into a network service.
- Removing native connectors already shipped in the default binary immediately.
- Replacing the existing worker protocol or scheduler lease model.
- Requiring containers, Kubernetes, a plugin registry, or internet access.

## Decision

Keep Brokoli a **modular monolith with composable process roles**, not a
mandatory microservice platform. `brokoli serve` continues to compose API,
scheduler, and local execution for the default installation. The same stable
contracts allow an operator to run dedicated API/control-plane, scheduler, and
worker processes when their scale or capability needs differ.

Make optional capability distribution a first-class, versioned concept:

1. **Core** owns pipeline definitions, persistence, scheduler/worker leases,
   HTTP API, authentication/authorization, connection-reference resolution,
   data-plane policy, the execution planner, and the capability registry.
2. **Roles** are composition choices over core contracts, not independent
   products. All-in-one starts the compatible roles in one process; separated
   roles use the existing durable store and worker dispatch contracts.
3. **Feature packs** provide optional connectors, execution runtimes, and
   heavyweight native dependencies. They register declared capabilities through
   a versioned extension boundary and run out-of-process by default. A worker
   advertises its installed packs before it can receive their work.
4. **Native in-process integrations** remain allowed when they earn their
   capability and are appropriate for the default distribution. New heavyweight
   integrations must justify being linked into the default binary; otherwise
   they ship as feature packs.

The initial implementation is an inventory and boundary hardening phase, not a
new installer or a compatibility-breaking binary split. Measure binary size and
dependency contribution first; extract capability registration and role
composition behind explicit internal interfaces; then move one non-default,
heavy connector through the feature-pack path as the reference proof.

### Distribution profiles

| Profile | Contents | Intended use |
| --- | --- | --- |
| `brokoli` default | Core, all-in-one roles, current supported baseline capabilities, plugin runtime | Local development and simple self-hosted deployment |
| Dedicated roles | Same core binary configured as API/control-plane, scheduler, or worker | HA and scaled deployments without a product split |
| Worker image/profile | Core worker role plus selected feature packs | Pools dedicated to particular connectors or runtimes |
| Offline/custom image | Core plus packs baked by the operator | Air-gapped or tightly governed deployments |

The profile is a deployment and capability declaration, not a different
pipeline language. A pipeline that requests an unavailable capability fails
before it is scheduled, with the missing pack and compatible worker requirement
named explicitly.

## Consequences

### Positive

- Preserves the familiar one-command experience while creating a credible
  growth path for real deployments.
- Reduces default binary growth over time by making heavyweight additions prove
  why they belong in core.
- Lets operators keep database-specific drivers and credentials on the workers
  that actually need them.
- Reuses existing scheduler election, worker dispatch, leasing, fencing, and
  capability placement rather than adding a second orchestration system.
- Makes ownership boundaries clearer: core lifecycle and contracts versus
  optional feature implementation.

### Negative

- There are now more compatibility surfaces: pack ABI/protocol versions,
  capability declarations, pack installation state, and worker placement.
- A feature pack is an operational artifact to install, upgrade, diagnose, and
  secure; the default binary cannot hide that complexity from advanced users.
- Out-of-process packs may have startup and data-transfer overhead compared
  with native in-process drivers.
- During migration, two delivery forms coexist. Documentation and diagnostics
  must state whether a capability is built in, installed as a pack, or absent.

### Deferred

- Exact feature-pack artifact format, signing policy, registry protocol, and
  upgrade/rollback flow.
- The stable public extension ABI; initial boundaries may be internal and
  versioned with Brokoli until a reference pack proves what must be public.
- Which currently native connectors remain in the default baseline and which
  are moved first. That choice requires size, support, and usage data.
- Independent release cadence for packs versus core.
- Whether a minimal `brokoli-core` download becomes a user-visible product
  name, rather than an implementation/deployment profile.

## Alternatives considered

- **Keep one permanently full binary** — rejected as the long-term default.
  It preserves simple installation today but makes every optional dependency a
  mandatory download, CVE surface, and release coupling.
- **Immediately split API, scheduler, and worker into mandatory services** —
  rejected. It makes the simplest deployment harder, duplicates configuration,
  and would replace working durable contracts with deployment complexity before
  the boundaries need it.
- **Build-tag editions for every connector** — rejected as the primary model.
  It creates an untestable build matrix and forces users to compile or trust
  many opaque vendor binaries. Custom images remain a valid offline escape
  hatch.
- **Go `plugin` dynamic libraries** — rejected. They require exact Go toolchain
  and dependency compatibility, do not support all target platforms well, and
  would make pack upgrades tightly coupled to the core binary.
- **Containers or a separate pod per connector** — deferred. Strong isolation
  can be useful for untrusted or native dependencies, but mandatory per-node
  containers impose cold-start, scheduling, image-distribution, and local
  installation costs that do not fit the default experience.
- **External connector packs through the existing plugin protocol** — chosen as
  the first reference direction because it is cross-platform, already OSS, and
  naturally keeps heavyweight dependencies outside the core process. It must
  still earn capability, versioning, and security guarantees before replacing a
  native connector.

## Follow-ups

- Produce a binary-size and transitive-dependency inventory by connector and
  runtime, including CGO and CVE exposure.
- Document the current role-composition graph and identify packages that mix
  control-plane lifecycle with worker execution.
- Define a capability manifest for feature packs: name, protocol version,
  connector/runtime versions, OS/architecture, resource requirements, and
  required worker capabilities.
- Select one heavyweight, non-default connector as the reference external pack
  and prove install, offline image, placement, cancellation, retry, credential
  scoping, and upgrade behavior.
- Add preflight validation that reports missing capabilities before a run is
  queued.
- Revisit the default binary composition after the reference pack and inventory
  are measured.

# ADR-020: Adopt the ecosystem framing — Phase 1 portfolio and OSS/Enterprise boundary

**Status:** proposed
**Date:** 2026-08-18

## Context

Brokoli has been designed and documented as one thing — "the platform" — but the system that ADR-002 through ADR-019 describe has outgrown that framing. It is, today, at least four things wearing one name: an orchestration/control plane (pipelines, scheduling, the UI); a distributed execution substrate (`models.PhysicalPlan`/`PhysicalInstance`/`ExecutionAttempt`, fencing, remote instance dispatch — ADR-015, ADR-017); a reference-based artifact/dataset plane (ADR-012); and an extensibility/plugin ecosystem with its own packaging and distribution story (ADR-016). Three of these already have organic names that emerged bottom-up rather than being imposed: "Nodus" ships today in the sidebar and Workers page (`brokoli-ee#137`); "Chronicle" ships today as a run-provenance UI panel (`brokoli#239`); "Vektor" already appears in this repository's own issue tracker (`brokoli#236`'s "Out of scope" section: "Continuous Vektor execution") despite having zero implementation.

A strategy document ("From Brokoli Platform to Brokoli Ecosystem") proposes formalizing this: split the product into eight named products — Brokoli, Nodus, Relay, Strata, Foundry, Chronicle, Bound, Vektor — with an explicit OSS/Enterprise boundary principle, phased over five rollout stages. Section 31 of that document asks for an executive decision on four narrower points, not the full eight-product buildout. This ADR is that decision record for the part of the proposal that is ready to decide now: the philosophy, the Phase 1 portfolio, and the boundary principle. It intentionally does not commit to Bound or Vektor, which are unstaffed, green-field, multi-month builds, not renames of things that already exist.

An audit against the actual codebase (not just the strategy document's claims) found:

| Product | Maturity |
|---|---|
| Nodus | ~70-80% built — execution attempts/leases/fencing, physical instances, K8s executor, WorkPool dispatch, KEDA autoscaling all shipped; the name already ships in the UI. |
| Strata | Real foundation, missing the product layer — `pkg/artifact` (ADR-012) has real reference types and a pluggable `ArtifactStore`; missing a cloud backend, workspace-level quotas, and `CollectionRef`/`ScalarRef` as Go types. |
| Relay | 0% built, and a real naming collision exists — `extensions.RunCancelBroadcaster` (renamed from `RunCancelRelay` alongside this ADR, see Follow-ups) is an unrelated pub/sub cancellation mechanism that used "Relay" throughout its naming. |
| Chronicle | UI shipped, not wired to lineage — the provenance panel exists but doesn't yet answer "which instance produced this `DatasetRef`." |
| Foundry | Protocol mature, distribution now real — ADR-016's packaging/runtime-class/install-UX, evaluated separately this same day, turned out to be substantially shipped, not just proposed. |
| Bound | 0% — only request-time RBAC and a plan-entitlement gate exist; no admission-time, data-classification, or placement policy. |
| Vektor | 0% implementation, name already circulating in the issue tracker. |

## Decision

Adopt the ecosystem framing, scoped to what is ready to decide today:

1. **Phase 1 portfolio is Brokoli, Nodus, Relay, Strata.** These are the always-central core plus the two most-built-out products (Nodus, Strata) plus the one genuine, named gap (Relay). This matches the maturity audit above — the sequencing is not arbitrary.
2. **OSS/Enterprise boundary principle:** runtime engineering (the engine, the execution model, the artifact plane, the plugin protocol) stays open source; organizational-scale operations and managed infrastructure (multi-tenant platform services, managed distributed execution, enterprise auth/audit) are Enterprise. This names the boundary the code already draws (`extensions.Registry` vs. `ee/`), rather than inventing a new one.
3. **Productization follows architecture, not the reverse.** A product name may only apply to something that already has the "becomes a product when X" characteristics the source document's own Section 27 defines (a real, independently versioned capability boundary — not a renamed page or menu item). This ADR does not authorize renaming UI surfaces ahead of the underlying architecture existing.
4. **Bound and Vektor are explicitly out of scope for this decision.** They are green-field, multi-month distributed-systems builds (an admission-time policy engine; a streaming engine with checkpointing and backpressure), not formalization of existing work. Committing to them requires their own ADRs once (if) staffed — this ADR takes no position on when or whether that happens.

## Consequences

### Positive

- Matches the OSS/Enterprise boundary the code already draws — this names an existing line, it does not create a new one to maintain.
- Closes the doc-vs-code gap for the mechanisms Phase 1 depends on: ADR-012, ADR-015, and ADR-017 (Strata's and Nodus's underlying designs) move from Proposed to Accepted alongside this ADR (see Follow-ups) — Phase 1 product claims are now backed by Accepted ADRs, not Proposed ones.
- Gives the strategy document's Section 31 ask a concrete, version-controlled artifact instead of leaving the decision only in a standalone strategy doc.

### Negative

- Four more potential product-doc surfaces (per the source document's Section 24) without first finishing the doc-debt already identified in the Phase 1 mechanisms — this ADR closes part of that gap (item 2 above) but does not by itself write user-facing product documentation.
- Naming Relay now, ahead of any implementation, creates a second reason (beyond the ADR itself) that anyone building distributed run-cancellation-adjacent features needs to know "Relay" is reserved — a light process cost, not a technical one.

### Deferred

- Bound and Vektor: no ADR, no staffing decision, no timeline. Explicitly not part of this Decision.
- Phase 2 (Foundry), Phase 3 (Chronicle), Phase 4 (Bound), Phase 5 (Vektor) product-specific rollout ADRs — each gets its own ADR when its phase is actually staffed, not pre-approved here.
- User-facing product documentation and marketing surfaces for Brokoli/Nodus/Relay/Strata.
- Any renaming of existing UI surfaces to the new product names beyond what has already organically happened (Nodus, Chronicle).

## Alternatives considered

- **Keep the single-platform framing (status quo).** Rejected — three of the eight names are already circulating organically in code, UI, and issue text; declining to name the boundary doesn't prevent the sprawl, it just leaves it undocumented.
- **A lighter cosmetic pass (e.g., rename "Workers" to "Nodus" everywhere) without the architectural boundary work.** Rejected per the source document's own Section 27 ("What We Should Not Do") — a renamed page is not a product, and this ADR's own Decision point 3 makes that rule explicit for future work too.
- **Approve the full eight-product portfolio and five-phase rollout now.** Rejected — Bound and Vektor are unstaffed, multi-month, green-field builds; approving them now would commit past what current maturity or staffing supports, per this ADR's own audit table.

## Follow-ups

- `Tnsor-Labs/brokoli#244` — rename `extensions.RunCancelRelay`/`ee/distributed.RedisCancelRelay` to `RunCancelBroadcaster`/`RedisCancelBroadcaster`, resolving the Relay naming collision named in the Context/Decision above, before "Relay" is customer-facing.
- `Tnsor-Labs/brokoli#245` — promote ADR-012, ADR-015, ADR-016, and ADR-017 from Proposed to Accepted, closing the doc-vs-code gap for the mechanisms Phase 1 (Nodus, Strata) depends on.
- Chronicle-to-lineage wiring (connecting the shipped provenance UI to `engine/lineage.go`'s dependency graph) is real next-milestone work with its own tracking issue; it does not require a new ADR to proceed.
- Any future Bound or Vektor work starts with its own ADR, not an extension of this one.

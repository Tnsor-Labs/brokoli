# ADR-014: Core-owned pipeline IR and explicit compatibility negotiation

**Status:** proposed
**Date:** 2026-08-09

## Context

Brokoli's Python SDK, Go backend, and Svelte UI currently encode parts of the pipeline contract independently. The backend has `models.Pipeline` and generic node config maps; the SDK emits dictionaries; the UI has handwritten TypeScript types; plugin manifests carry their own schemas. Pipeline IR `2.0` and `GET /api/capabilities` establish version and capability vocabulary, but they do not yet make one machine-readable contract normative.

This allows several forms of drift:

- the SDK can accept config the backend ignores or interprets differently;
- local validation can pass behavior the server cannot execute;
- create, update, import, and run do not apply the same validation boundary;
- compile-only SDK features look like deployable runtime features;
- older or newer clients cannot distinguish harmless additive fields from unsupported execution semantics;
- unversioned pipelines are accepted without a defined sunset policy.

The IR is the boundary between independently released repositories and future SDK languages. Compatibility cannot depend on contributors manually keeping handwritten models synchronized.

## Decision

The Brokoli core repository owns the canonical external pipeline IR as versioned JSON Schema plus normative conformance fixtures.

The serialized format remains JSON/YAML-friendly. Protobuf is not the canonical authoring/deployment format. Go, Python, and TypeScript types may be generated where tooling produces maintainable output; otherwise handwritten adapters must pass the same schema and fixture suite.

Compatibility follows these rules:

1. Every deployed pipeline declares an `ir_version` using `major.minor` form.
2. A major version may contain incompatible structural or semantic changes.
3. A minor version is additive. Existing required fields and meanings cannot change within the same major version.
4. Semantic node capabilities such as `source`, `sink`, and `dataset-output` describe what a node is. Pipeline-level required execution features such as `dynamic-expansion`, `artifact-ref`, or `partition-retry` describe what the target server must support. These are separate fields and are not inferred interchangeably at runtime.
5. The server capability response lists accepted IR versions, supported execution features, policy limits, and available connector/runtime versions.
6. The SDK performs capability negotiation before deployment. An unreachable capability endpoint may be bypassed only through an explicit compatibility flag for legacy servers; a reachable server that reports incompatibility is a hard failure.
7. Create, update, and import run the same full executable validation before persistence. Graph integrity, node kind, connector availability, data-kind compatibility, schema compatibility, and required execution features are checked at this boundary.
8. Unknown optional fields may be retained or ignored only when no undeclared execution behavior depends on them. Unknown node kinds, required fields, or required execution features fail closed.
9. Unversioned payload support is a time-bounded migration path. The server emits a deprecation warning and translates only documented legacy node shapes. The removal window must be announced before support ends.
10. Persisted pipeline versions retain their original IR and code/package digests. Migration produces a new pipeline version rather than mutating historical run definitions.

The schema package and conformance fixtures are introduced incrementally. This ADR does not require rewriting every existing `map[string]interface{}` config before the first contract tests land.

## Consequences

### Positive

- SDK/backend/UI drift becomes testable rather than procedural.
- Unsupported behavior fails before pipeline creation with a specific compatibility error.
- Additional SDK languages can target a documented language-neutral contract.
- Semantic graph changes can be distinguished from visual layout changes.
- Connector and runtime availability becomes part of deployment validation rather than a runtime surprise.
- Historical runs remain auditable against the exact contract and code digest they used.

### Negative

- The core repository becomes responsible for schema release discipline and fixture maintenance.
- Some existing permissive payloads will be rejected once full executable validation is applied consistently.
- Generated-model tooling will not be equally mature across Go, Python, and TypeScript; adapters and fixture tests remain necessary.
- A version and deprecation policy adds release coordination across repositories.

### Deferred

- The exact directory/package layout and generation toolchain for schema artifacts.
- Schema representation and evolution rules for dataset columns and nested data.
- Connector config-schema composition into the pipeline IR schema.
- Automated migration tooling for every legacy SDK signature.
- Signing and provenance policy for code packages and connector manifests.

## Alternatives considered

- **Keep Go structs authoritative and maintain SDK/UI models manually.** Rejected because that is the current drift mechanism and provides no language-neutral validation artifact.
- **Make Python models authoritative.** Rejected because the Go control plane owns executable validation and future clients are not necessarily Python.
- **Use Protobuf as the only pipeline format.** Rejected because pipelines are user-visible, edited as JSON/YAML, and contain connector-defined configuration that benefits from JSON Schema. Protobuf remains an option for internal runtime transports.
- **Treat node capabilities as both semantic roles and server requirements.** Rejected because a node being a `source` does not imply which execution feature or connector version the target must provide.
- **Accept unknown execution behavior for forward compatibility.** Rejected because silently ignoring semantics can produce incorrect data. Forward compatibility is provided through additive fields plus explicit required capabilities.

## Follow-ups

- Tracked in [brokoli#90](https://github.com/Tnsor-Labs/brokoli/issues/90), milestone M1.
- The SDK preflight is also tracked in [brokoli-sdk#9](https://github.com/Tnsor-Labs/brokoli-sdk/issues/9).
- Add the canonical schema package and cross-repository conformance fixtures.
- Apply full executable validation to create, update, and import.
- Define and publish the unversioned-payload deprecation window.

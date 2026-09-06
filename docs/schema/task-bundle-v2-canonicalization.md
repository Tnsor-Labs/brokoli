# Task bundle v2 canonicalization and archive safety (ADR-033 section 2)

This is the spec a future archive-handling implementation must match — it
is not the implementation itself, exactly like
`task-interface-canonicalization.md`'s own scoping note. `task-bundle-v2.json`
validates the manifest *document*; the rules below govern the *archive
bytes* the manifest describes, which a JSON Schema cannot express.

Not yet implemented anywhere in this repository (rollout phase 0, issue
#439 step 5): no Go code parses, extracts, or verifies a `brokoli.task-bundle/v2`
archive yet. `pkg/taskbundle` implements `task-bundle/1` (ADR-031) only, and
is not amended by this document — the two formats are additive per
ADR-035 Decision 1.

## Canonicalization (ADR-033 section 2 rule 1)

Two archives built from equivalent input must produce the same bundle
digest:

- entries ordered by normalized path, byte-for-byte (code-point
  comparison, not locale-sensitive);
- fixed archive-entry timestamps, ownership, and permission modes —
  never the build machine's clock or user/group IDs;
- one fixed, deterministic compression setting;
- the manifest's own JSON serialization is canonical (sorted keys, fixed
  whitespace) before it is hashed or archived, matching the discipline
  `models/ir.go`'s `normalizeIR`/`canonicalJSON` (Go) and `brokoli.ir`
  (Python)/`src/ir.ts` (TypeScript) already establish for pipeline IR.

## Path safety (rule 2)

Every `files[].path` must be:

- relative (no leading `/`, no drive letters);
- normalized (no `.`/`..` segments after normalization);
- free of symlinks, device files, FIFOs, and any non-regular-file entry
  type.

A future `Extract`/`ParseArchive` implementation must reject an archive
containing any violation before writing a single byte to disk, following
the exact defense-in-depth shape `pkg/taskbundle/bundle.go`'s `Extract`
(bundle.go:252) and `pkg/plugins/package.go`'s `extractArchive` (85-148)
already both independently implement for `task-bundle/1` and connector
plugins respectively: reject absolute paths and traversal before any
write, enforce a per-entry and aggregate byte cap, cap total entry count,
and read through a bounded reader as defense-in-depth even after the
declared size passes.

**Open implementation question for the phase that writes this code**:
those two existing implementations already duplicate the same pattern
once. A third bespoke copy for v2 is the naive path; extracting a shared
`pkg/archiveextract`-style helper first is the alternative. This document
does not decide that now — decide it with the real diff in hand when v2
extraction code is actually written, not speculatively here.

## Digest coverage (rules 3-4)

The bundle digest covers the canonical manifest bytes and every payload's
declared bytes (via each `payloads[].payload_digest`, itself covering that
payload's own files) — not just the manifest JSON in isolation. A payload
whose declared `payload_digest` does not match its actual files fails
verification before any byte executes. The manifest's own
`interface_digest` must equal the ADR-032 semantic digest of the `task`
node's referenced interface at admission time — a bundle built against a
different interface digest is a different implementation, never
silently accepted as "close enough."

## Dependency locking (rule 5)

A `payloads[].dependency_lock` file must resolve to hash-verified
artifacts only. A lockfile that still requires mutable registry
resolution at attempt time is not a production bundle — dependency
resolution happens before admission or in a separately governed build
service, never during an attempt (ADR-033 section 11).

## Immutability (rule 7)

Bundles are never updated in place. A content change is a new digest and
a new bundle; "upgrade in place" is not a valid operation on an existing
bundle digest.

## Entrypoint grammar (rule 9)

`task-bundle-v2.json`'s `#/$defs/entrypoint` closes this to exactly three
shapes (managed-language `module`+`symbol`, native `executable`, OCI
`command`/`args`) — a free-form colon/dot string is never a valid
entrypoint, regardless of runtime class.

## OS/arch scoping (rule 10)

`os: "any"`/`arch: "any"` is valid only when the full payload and its
locked dependencies contain no platform-specific binaries. A payload with
any native extension, compiled dependency, or platform-specific binary
must declare each feasible platform as its own separate payload with its
own digest — never one `any` payload that happens to work on the builder's
platform.

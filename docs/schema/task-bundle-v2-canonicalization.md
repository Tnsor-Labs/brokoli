# Task bundle v2 canonicalization and archive safety (ADR-033 section 2)

This is the spec every archive-handling implementation must match — it
is not the implementation itself, exactly like
`task-interface-canonicalization.md`'s own scoping note. `task-bundle-v2.json`
validates the manifest *document*; the rules below govern the *archive
bytes* the manifest describes, which a JSON Schema cannot express.

`pkg/taskbundlev2` implements it: `Assemble` writes an archive, and
`ReadManifest`/`Extract` read and verify one; `brokoli bundle jvm`
builds a JVM bundle through `Assemble`. `pkg/taskbundle` implements
`task-bundle/1` (ADR-031) only, and is not amended by this document — the
two formats are additive per ADR-035 Decision 1.

## Canonicalization (ADR-033 section 2 rule 1)

Two archives built from equivalent input must have the same contents, so
every builder writes the archive below. This section is normative for any
implementation that must produce the same bundle as core's (an SDK's
bundle plugin, for one), and `pkg/taskbundlev2`'s tests pin each rule.

**Compression.** The archive is a gzip stream: no optional fields (no file
name, comment or extra field), `MTIME` 0, `OS` 255, at the default
compression level. The deflate bytes themselves are not normative:
neither Go nor zlib promises the same compressed bytes across versions.
The bundle digest is the SHA-256 of the archive's own bytes, so it
reproduces for one builder and compressor. Parity between two builders is
defined on the uncompressed tar stream, which must be byte-identical.

**Tar stream.** POSIX ustar headers (`ustar\0` magic, version `00`),
modification time 0 (the Unix epoch), uid and gid 0, empty user and group
names, and entries in exactly this order:

1. a directory entry for every directory that contains a file, at any
   depth, in byte order of its slash path, written without a trailing
   slash, with mode 0700 and size 0;
2. `manifest.json`, mode 0600;
3. every file, in byte order of its slash path (byte order of UTF-8 is
   code-point order, never locale-sensitive), mode 0600.

**`manifest.json`** is compact JSON: no whitespace between tokens and no
trailing newline. Each object's keys are written in this order, and an
optional key is left out when its value is empty (empty string, absent
object, or an empty or absent list):

| Object | Keys, in order (optional ones in italics) |
| --- | --- |
| manifest | `format`, `name`, `interface_digest`, `source_digest`, *`build`*, `payloads`, `files` |
| `build` | `builder`, *`attestation`* |
| payload | `id`, `runtime`, *`requires`*, `os`, `arch`, `entrypoint`, `effects`, *`dependency_lock`*, `payload_digest`, *`image`* |
| `entrypoint` | *`module`*, *`symbol`*, *`executable`*, *`command`*, *`args`* |
| `image` | `digest`, *`reference`* |
| file | `path`, `size`, `sha256` |

`files` lists every file of the archive except `manifest.json`, in the
same byte order as the tar entries. Each file has its byte `size` and the
lowercase hex SHA-256 of its bytes. Strings are written as Go's
`encoding/json` writes them:
- `"`, `\`, `\n`, `\r` and `\t` take their two-character escapes;
- every other character below U+0020, and `<`, `>`, `&`, U+2028 and
  U+2029, are written as `\u` with four lowercase hex digits;
- everything else is literal UTF-8.

Numbers are decimal integers.

This is `json.Marshal` of the `taskbundlev2.Manifest` struct, which lists
its fields in this order. The keys are deliberately not sorted:
reordering them would give every bundle built from the same input a new
digest (#986).

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

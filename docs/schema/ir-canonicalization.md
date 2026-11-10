# IR canonicalization and digests

This is the language-neutral specification of how a Brokoli SDK
normalizes pipeline IR, renders it canonically, and digests it. The
Python SDK's `brokoli.ir` module is the reference implementation; any
second SDK (TypeScript, Go, ...) MUST produce byte-identical canonical
renderings and equal digests for the same semantic pipeline, and the
cross-SDK differential fixture suites are the enforcement. A change to
this file is a change to every SDK at once: version it accordingly.

Purpose: the canonical rendering identifies exactly what would be
deployed while ignoring cosmetic churn (key order, editor layout,
server-assigned fields). Digest equality means "deploying this is a
no-op"; digest inequality means a semantic change, and `diff` renders
the two canonical forms against each other.

## 1. Normalization

Input: a pipeline IR object (the shape of `pipeline-ir-2.1.json`,
possibly as returned by the server with server-side fields attached).
Normalization operates on a deep copy and applies, in order:

1. **Drop server-assigned fields** at the top level:
   `id`, `source`, `workspace_id`, `org_id`, `created_at`, `updated_at`.
2. **Fill absent-or-null list defaults** with `[]`:
   `nodes`, `edges`, `tags`, `depends_on`, `dependency_rules`.
3. **Fill absent-or-null map defaults** with `{}`:
   `params`, `hooks`.
4. **Drop empty `webhook_url`**: remove the key when its value is `""`.
5. **Drop default `schedule_timezone`**: remove the key when its value
   is null, `""`, or `"UTC"`.
6. **Blank `webhook_token`**: when the key is present (with any value),
   set it to `""`. Presence is semantic (webhook triggering is enabled);
   the token value is server-assigned.
7. **Per node** (each element of `nodes`):
   - remove `position` (editor layout, not semantics);
   - if `capabilities` is absent or `[]`, fill it with the built-in
     defaults for the node's `type` (the `node_type_capabilities` table
     served by `/api/capabilities`);
   - sort `capabilities` (ordering rule below).
8. **Sort `nodes`** by their `id` (ordering rule below). Nodes without a
   mapping shape or without an `id` sort after well-formed nodes, by
   their rendered value; SDKs authoring IR never produce these, but
   normalization must not crash on foreign input.
9. **Sort `tags` and `depends_on`** (ordering rule below).

Nothing else is reordered or rewritten. In particular: `edges` keep
their authored order, node `config` values are untouched (only the
rendering sorts keys), and unknown top-level keys (for example
`extensions`, or fields introduced by newer servers) pass through
untouched.

### Ordering rule

The reference sorts scalar lists by the pair
`(type_name, canonical_json_of_value)` compared lexicographically by
Unicode code point. For the lists this spec sorts, values are plain
strings in practice, where the rule reduces to: **sort by Unicode code
point of the string value**. Implementations MUST NOT use
locale-sensitive comparison (`localeCompare`, `strcoll`); a Turkish
locale must not produce a different digest.

## 2. Canonical rendering

The normalized object renders as JSON with exactly these properties:

- object keys sorted by Unicode code point, every key the same way:
  integer-like keys such as `"10"` and `"9"` sort as text (`"10"` before
  `"9"`), and a character above U+FFFF sorts by its code point, not by its
  UTF-16 surrogates;
- two-space indentation; a single space after `:`; no trailing spaces;
  an empty object is `{}` and an empty array `[]`;
- non-ASCII characters emitted literally as UTF-8 (no `\uXXXX` escaping
  of printable characters beyond what JSON requires);
- numbers as RFC 8785 section 3.2.2.3 writes them: an integer as its
  digits, and a float as the shortest decimal that reads back as the same
  IEEE-754 double (the closest, then the even one, when there is a
  choice), laid out by ECMAScript's Number-to-string rule. So `0.00001`,
  `1e-7`, `123456789012345680000`, `1e+21` and `5e-324`; a whole float is
  written as an integer (`5`, not `5.0`), and `-0` as `0`;
- `NaN` / `Infinity` are an error, never rendered;
- exactly one trailing newline (`\n`) at the end of the document.

The rule for numbers is the one JavaScript's `JSON.stringify` and Go's
`encoding/json` already follow (Go except for `-0`, which it writes as
`-0`). Python's `json.dumps` does not: it writes `1e-05`, `1e-06`,
`1.2345678901234568e+20` and `5.0`. A renderer built on `json.dumps`, or on
`JSON.stringify` of a key-sorted object (which moves integer-like keys
first), gives a different digest for the same pipeline, so each SDK writes
the layout itself (#982).

[`fixtures/ir-canonicalization/rendering.json`](fixtures/ir-canonicalization/rendering.json)
holds the cross-SDK vectors: JSON text inputs and the exact canonical
rendering each must give. They cover RFC 8785 Appendix B, the values above,
key order and string escapes. Every SDK's tests parse each input with that
language's own JSON parser and compare the rendering byte for byte.

> **Number hazard.** JSON does not distinguish `1` from `1.0`, and with the
> rule above neither does the rendering: both are `1`. Integers beyond
> 2^53 - 1 are still not portable between SDKs, because JavaScript cannot
> hold them exactly. Authoring SDKs MUST still
> emit integral numeric config values as integers (Python: `int`, never a
> whole-valued `float`), because a value's type, not only its rendering,
> reaches the engine.

## 3. Digest

`digest = "sha256:" + lowercase_hex(sha256(utf8_bytes(canonical_rendering)))`

The digest covers the normalized, canonically rendered document --
nothing else. Consequences, by construction: a create and a later
update with the same content have the same digest; server round-trips
(which attach `id` and timestamps) do not change it; editor layout does
not change it.

## 4. What SDK compilers must emit (parity anchors)

For cross-SDK byte parity, the *compilation* step (pipeline object to
IR) must also agree on the emitted key sets, not just the rendering:

- Top level, always: `pipeline_id`, `name`, `description`, `schedule`,
  `enabled: true`, `ir_version` (`"2.1"` when any edge carries
  `condition`, else `"2.0"`), `nodes`, `edges`, `tags`, `depends_on`.
- Top level, only when set: `schedule_timezone`, `catchup: true`,
  `sla_deadline` + `sla_timezone` (defaulting the timezone to `"UTC"`),
  `hooks`, `webhook_token: ""`.
- Edges: `{"from", "to"}` plus `condition` only on branch edges.
- Nodes: `{"id", "type", "name", "config", "capabilities"}`;
  `position` may be emitted (the reference does) since normalization
  strips it.
- Source schema hints: compilers annotate `config._schema_hint` with
  `"query_result"` on `source_db` nodes that carry a `query`, and
  `"api_response"` on `source_api` nodes. These survive normalization
  and are part of the digest.
- **`code` node content addressing (ADR-031 task bundles):** a code node
  carries exactly one of `config.script` (inline source) or
  `config.task_bundle` (a content-addressed project archive reference) —
  never both, never neither; `config.language` is present in both forms.
  `task_bundle` is `{"digest", "format"}` with
  `digest == "sha256:" + 64 lowercase hex chars` (the SHA-256 of the
  archive's own bytes) and `format == "task-bundle/1"`. The digest is
  part of the node config, so it is canonicalized and hashed like any
  other value: two pipelines referencing different archives differ in
  IR digest. The archive bytes themselves ride sideband (uploaded by the
  deployer); they never appear in the IR. SDKs MUST NOT write
  `archive_sha256` into a packaged manifest: it is self-referential (the
  digest of bytes that contain the field naming it), so no real value can
  equal the archive's actual digest, and core's `ParseArchive` refuses any
  non-empty value that does not match the archive it ships in — which
  means, in practice, every non-empty value.
- Node ids: `clean(name) + "_" + n`, where `clean` lowercases, replaces
  spaces with underscores, drops every remaining character that is not
  alphanumeric or `_` (alphanumeric per Unicode, as in Python's
  `str.isalnum` — not ASCII-only), truncates to 20 code points, and
  falls back to `"node"`; `n` is a per-base counter starting at 1,
  skipping ids already taken (including explicitly keyed ones).
  `"Read A"` therefore yields `read_a_1`, not `reada_1` — the
  space-to-underscore step happens before the strip, and a compiler
  that strips first diverges on every multi-word display name.

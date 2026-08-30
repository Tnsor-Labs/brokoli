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

- object keys sorted by Unicode code point;
- two-space indentation; a single space after `:`; no trailing spaces;
- non-ASCII characters emitted literally as UTF-8 (no `\uXXXX` escaping
  of printable characters beyond what JSON requires);
- `NaN` / `Infinity` are an error, never rendered;
- exactly one trailing newline (`\n`) at the end of the document.

This is Python's `json.dumps(value, indent=2, sort_keys=True,
ensure_ascii=False, allow_nan=False) + "\n"` and JavaScript's
`JSON.stringify(sortedValue, null, 2) + "\n"` over a key-sorted value;
both produce the same bytes for the same data, with one documented
hazard:

> **Number hazard.** JSON does not distinguish `1` from `1.0`, but
> Python renders the float `1.0` as `1.0` while JavaScript renders the
> number `1` as `1`. Authoring SDKs MUST emit integral numeric config
> values as integers (Python: `int`, never a whole-valued `float`), and
> fixture suites should include a non-integral float (for example
> `0.5`) to prove agreement where the representation is unambiguous.

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
  digest of bytes that contain the field naming it), so core's
  `ParseArchive` rejects a non-empty value, and no real archive can
  match.
- Node ids: `clean(name) + "_" + n`, where `clean` lowercases, replaces
  spaces with underscores, drops every remaining character that is not
  alphanumeric or `_` (alphanumeric per Unicode, as in Python's
  `str.isalnum` — not ASCII-only), truncates to 20 code points, and
  falls back to `"node"`; `n` is a per-base counter starting at 1,
  skipping ids already taken (including explicitly keyed ones).
  `"Read A"` therefore yields `read_a_1`, not `reada_1` — the
  space-to-underscore step happens before the strip, and a compiler
  that strips first diverges on every multi-word display name.

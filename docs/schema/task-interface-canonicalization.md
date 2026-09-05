# Portable task interface: canonical value encoding and digests

This is the language-neutral specification for the Brokoli Portable Type
Descriptor (BPTD) and portable value encoding ADR-032 defines. It
complements `task-interface-v1.json` (which is the *type descriptor*
schema — `int64`, `record`, `union`, and so on) with the *value*
encoding rules: how an actual portable value is represented as JSON so
that Go, Python, and TypeScript compute the same bytes and the same
semantic digest for the same value.

**Scope note (rollout step 1, ADR-032 section 12):** this document and
`task-interface-v1.json` are published together as schema and rules
only. Neither is wired into the pipeline IR or execution yet — that is
IR 2.2 and the `task-ports-v1` capability, a later rollout step. A
canonical digest *implementation* in Go (as opposed to this written
rule) is also a separate, later follow-up; this document is what that
implementation must match, the same relationship `ir-canonicalization.md`
has to `brokoli.ir`.

## 1. Contract identity

```json
{ "contract": "brokoli.task-interface/v1" }
```

is independent of `ir_version` and of `brokoli.task-runtime/v1` (ADR-033).
Within `v1`, additions may only be optional and semantics-preserving; a
change to assignability, coercion, nullability, default evaluation, or a
primitive's wire representation requires a new major task-interface
version, exactly as ADR-032 section 2 states.

## 2. Descriptor vs. value

`task-interface-v1.json` describes a **type** — `{"kind": "int64"}`. A
**value** of that type is a separate JSON encoding, described below.
Confusing the two is the most likely implementation bug: a schema
validator accepting a *descriptor* says nothing about whether a given
*value* is a valid instance of it.

## 3. Canonical value encoding

Values that JSON represents losslessly on their own (`boolean`, `string`
within a closed descriptor, plain arrays/objects for `array`/`map`/
`record` composites) are encoded as themselves. Values JSON cannot
represent losslessly use a tagged object:

```json
{"$bptd": "int64", "value": "9223372036854775807"}
{"$bptd": "decimal", "value": "-1200.50"}
{"$bptd": "bytes", "value": "SGVsbG8="}
{"$bptd": "timestamp", "value": "2026-09-05T12:34:56.123456789Z"}
{"$bptd": "duration", "seconds": "90", "nanos": 500000000}
```

A union value is encoded using the descriptor's own declared field names:

```json
{"kind": "card", "value": {...}}
```

(the field names `kind`/`value` above are the union's *declared*
`tag_field`/`value_field` — a union declaring different field names
uses those instead.)

## 4. Canonical encoding rules (normative)

These are ADR-032 section 4's rules, restated here as the rule set an
implementation is checked against:

1. JSON is UTF-8, rejects duplicate object keys and invalid Unicode
   scalar sequences, and preserves code points without Unicode
   normalization.
2. Object keys and record descriptor fields are sorted by UTF-8 byte
   order for hashing. Dataset schemas needing presentation/physical
   column order use the descriptor's explicit `column_order` list (see
   `value_dataset` in `task-interface-v1.json`); declaration order in
   `fields` alone is never semantic. Array value order remains semantic.
3. `int64` uses `0` or `-?[1-9][0-9]*`: no leading plus, no leading
   zero (beyond the bare `0` case), and the value must fit the signed
   64-bit range.
4. `decimal` uses `-?(0|[1-9][0-9]*)(\.[0-9]+)?`: no exponent notation,
   no leading plus. Trailing fractional zeros are preserved (declared
   scale may be semantic). Negative zero normalizes to positive zero
   with the same scale.
5. `float64` is finite and hashes using the shortest round-trippable
   IEEE-754 representation; `-0` normalizes to `0`. NaN and infinities
   are not portable v1 values.
6. `bytes` uses padded RFC 4648 standard base64 — never URL-safe, never
   unpadded.
7. `timestamp` uses UTC `Z`, a four-digit year, and zero, three, six, or
   nine fractional digits. Offset forms normalize before persistence;
   leap seconds are rejected in v1.
8. `duration` is a fixed signed seconds/nanoseconds pair with
   `-999999999 <= nanos <= 999999999`, normalized to one sign. Calendar
   months and years are deliberately not durations.
9. Maps reject duplicate decoded keys. `__proto__`, `prototype`, and
   `constructor` have no special behavior in JavaScript adapters — a
   map with one of these as an ordinary string key is not special-cased
   or dropped.
10. Semantic digests hash this canonical tagged representation, never an
    SDK's native in-memory value (a Python `Decimal`, a JS `bigint`, a Go
    `int64`, ...).

For a descriptor of kind `json`: numbers are finite IEEE-754 binary64
values and use the `float64` canonical hashing rule; integers requiring
lossless 64-bit semantics must use an `int64` descriptor instead, not
`json`. A tagged `$bptd` object is interpreted only when the *declared
descriptor* requires that tagged type — an ordinary `json`-kind value
that happens to contain a `$bptd` key is not reinterpreted as a tagged
value. A descriptor of kind `unknown` is restricted to canonical `json`
values; transporting bytes, int64, decimal, or temporal values through
an `unknown` port requires a known descriptor instead.

In JavaScript/TypeScript, `int64` maps to `bigint` or an explicit
lossless adapter, never an imprecise `number`. NDJSON codecs preserve
tagged values; an Arrow codec must use lossless logical/physical types
and pass the same round-trip vectors as the JSON codec.

## 5. Digest

Once a Go implementation exists (a named follow-up, not this document),
the digest formula mirrors `ir-canonicalization.md` section 3:

```
digest = "sha256:" + lowercase_hex(sha256(utf8_bytes(canonical_rendering)))
```

applied to the canonically rendered, key-sorted JSON of the object being
digested (a task interface, a pipeline's parameter declarations, or a
portable value) — never to native in-memory representations.

## 6. Assignability (reference, not restated in full)

Directional assignability rules (producer-emits vs. consumer-accepts)
are ADR-032 section 9 in full; this document does not restate all twelve
rules. The one worth repeating here because it is easy to get backwards
in an implementation: **numeric widening from `int64` to `float64` is
never implicit** — values beyond 2^53 lose precision in common runtimes,
so that conversion must be an explicit node in the graph, never silent.

## 7. What this covers vs. what is deferred

Covered by `task-interface-v1.json` + this document, as of this PR:

- The BPTD type descriptor language (all v1 primitives and composites).
- Named task input/output ports and their cardinality.
- Task-local and pipeline-level parameter declarations, including the
  `required`/`default` mutual exclusion.
- The canonical value-encoding rules (written specification).

Explicitly deferred to later PRs, per ADR-032's own incremental rollout
(section 12) and its Follow-ups list:

- A canonical normalized interface digest and directional compatibility
  *implementation* in Go (this document is the spec it must match).
- A JSON Schema for tagged portable *values* (as opposed to type
  descriptors) and round-trip vectors covering the boundary cases named
  in ADR-032's acceptance gates (64-bit boundaries, temporal values,
  field-order permutations, large JSON numbers, `$bptd` key collisions,
  unknown JSON-only values).
- Wiring `task_interface` into the pipeline IR (IR 2.2, `task-ports-v1`)
  and into `models/ir_schema_contract_test.go`'s two-way field sweep —
  there is no Go struct to bind against yet.
- SDK inference adapters and the cross-language differential fixtures
  proving equivalent Python/TypeScript/JSON declarations normalize to
  the same interface.

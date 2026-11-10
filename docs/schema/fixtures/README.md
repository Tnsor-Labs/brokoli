# Golden client fixtures

Real client-emitted IR payloads, validated against
`../pipeline-ir-2.1.json` by `models/ir_schema_contract_test.go` on every
test run. A failure means a client and this server disagree about the
contract — one of them changed on purpose, and the other (or this
fixture) must catch up in the same change.

| Fixture | Producer | Refresh |
|---|---|---|
| `sdk-emitted-2.1.json` | brokoli-sdk `brokoli compile -f json` of an IR 2.1 pipeline exercising conditional routing (`condition_node` + `.when()/.otherwise()`), declarative pagination with execution policy, `@task` module-context packaging, and `node_key` | Recompile the equivalent pipeline with current SDK main and replace the file |

## IR canonical rendering

`ir-canonicalization/rendering.json` holds the cross-SDK rendering vectors
of `../ir-canonicalization.md` section 2: each case's `input` is JSON text,
and parsing it with an SDK's own JSON parser and rendering it canonically
must give exactly `canonical`. The Python SDK (`tests/fixtures/ir_canonicalization/`)
and the TypeScript SDK (`tests/fixtures/ir-canonicalization/`) vendor a copy;
change all three together. The number cases were generated from RFC 8785
Appendix B's bit patterns with JavaScript's `JSON.stringify` and checked
against Go's `encoding/json`.


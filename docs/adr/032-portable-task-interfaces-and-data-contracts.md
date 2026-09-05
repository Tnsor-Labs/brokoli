# ADR-032: Portable task interfaces and data contracts

**Status:** proposed
**Date:** 2026-09-05

## Context

Brokoli pipelines can be authored in Python and TypeScript today, and the
pipeline IR is intentionally language-neutral. The executable-task boundary is
not. Each SDK currently knows facts that disappear during compilation:

- Python function annotations and defaults are not represented in the IR;
- TypeScript generic types disappear at runtime and are not represented in the
  IR;
- run parameters are an untyped `map[string]string` even though task code can
  interpret them as numbers, booleans, arrays, or structured values;
- `DatasetRef`, `ScalarRef`, and `ArtifactRef` are SDK authoring types, while
  serialized edges contain only node IDs;
- `source_api(response="artifact")` can return an authoring-time artifact
  reference while its serialized semantic capability still says
  `dataset-output`;
- generated code wrappers rely on ambient `rows`, `columns`, `params`, and
  `output_data` globals whose shapes are not declared by the pipeline;
- downstream compatibility is discovered by executing code rather than by
  validating that an upstream output can satisfy a downstream input.

Consequently, a pipeline can compile, validate structurally, and deploy while
still having a missing parameter, an invalid default, a dataset connected to a
scalar input, or an upstream schema that cannot satisfy a downstream task. The
UI cannot generate a trustworthy run form. A scheduler cannot distinguish a
small inline scalar from a large dataset or opaque artifact by reading the
edge. Another SDK cannot reconstruct the contract that the author saw.

This is not only a Python typing problem. Python annotations, TypeScript types,
Go structs, Java records, Rust types, and JSON/YAML declarations are different
authoring surfaces. If any one of them becomes the canonical model, every other
language either loses fidelity or must reproduce that language's semantics.
Brokoli needs a deliberately smaller portable type system that every SDK can
target and every worker can enforce.

### Existing decisions this ADR preserves

- ADR-014 makes versioned JSON Schema and normative fixtures in the core
  repository the canonical external pipeline contract. This ADR extends that
  contract; it does not make Python models, TypeScript declarations, or
  Protobuf authoritative.
- ADR-012 makes large datasets and artifacts reference-based. A type contract
  describes a value; it does not require that value to travel inline.
- ADR-015 separates logical declarations from physical execution plans. Task
  interfaces belong to logical IR; resolved storage and transport choices
  belong to physical planning.
- ADR-019 distinguishes materialized, reference-passing, and streaming
  execution. A value kind and an I/O mode are related but not interchangeable.
- ADR-022 requires secrets to be resolved through controlled references rather
  than embedded in pipeline definitions or ordinary parameter values.

### Design goals

The contract must:

1. mean the same thing in every authoring and execution language;
2. support useful validation without claiming to prove arbitrary user code;
3. preserve Brokoli's low-ceremony authoring experience;
4. distinguish pipeline parameters, task data inputs, runtime context, and
   secrets;
5. describe datasets, scalars, artifacts, and collections without forcing all
   values through a tabular abstraction;
6. permit gradual adoption by existing nodes and pipelines;
7. be deterministic, digestible, and suitable for compatibility review;
8. remain small enough for independent SDK implementations and conformance
   testing.

### Non-goals

This ADR does not define how Python or TypeScript code is packaged or launched;
ADR-033 owns that decision. It does not turn Python into a statically checked
language, require every field to be known, infer schemas from production data,
or replace connector-specific schemas. It does not promise exactly-once task
execution.

The existing `extensions.DataContract` is a pipeline-output quality rule with
column checks and ownership metadata. It is not the portable task ABI and is
not silently reinterpreted as one. A later migration may compile compatible
parts of that model into BPTD plus runtime quality checks; until then both names
and API surfaces remain distinct.

## Decision

Brokoli will add a versioned, language-neutral **Portable Task Interface** to
the canonical pipeline IR. SDKs may infer this interface from native language
types, but only the serialized portable interface is authoritative after
compilation. The control plane validates interfaces before persistence and run
admission; runtime adapters validate values at the execution boundary according
to an explicit validation mode.

### 1. Three contracts, not one overloaded schema

The model separates three concerns:

| Contract | Declares | Does not declare |
|---|---|---|
| Pipeline parameters | Values accepted when a run is requested | DAG data flowing over edges |
| Task interface | Named input/output ports and their portable types | How code is packaged or launched |
| Runtime context | Attempt identity, cancellation, deadlines, logger, metrics, secret handles | User-supplied business parameters |

An upstream dataset is never disguised as the first user parameter. A secret
is never accepted as an ordinary default. Attempt IDs and loggers never appear
as fake function arguments in the portable contract.

### 2. Contract identity and versioning

Each interface declares a stable contract identifier:

```json
{
  "contract": "brokoli.task-interface/v1"
}
```

The identifier is independent of `ir_version` and the task runtime protocol.
The pipeline IR version determines where and how the interface is embedded;
the interface version determines the meanings of types and compatibility
rules; ADR-033's runtime protocol version determines how an executor exchanges
values and events.

Within `v1`, additions may only be optional and semantics-preserving. A change
to assignability, coercion, nullability, default evaluation, or a primitive's
wire representation requires a new major task-interface version. Unknown
required type features fail closed through ADR-014 capability negotiation.

### 3. Pipeline parameter declarations

The pipeline gains a `parameters` object whose entries are declarations, not
run values:

```json
{
  "parameters": {
    "threshold": {
      "type": {"kind": "float64"},
      "required": true,
      "description": "Minimum score retained by the pipeline"
    },
    "region": {
      "type": {
        "kind": "enum",
        "values": ["us-east", "eu-west"]
      },
      "required": false,
      "default": "us-east"
    },
    "include_debug": {
      "type": {"kind": "boolean"},
      "required": false,
      "default": false
    }
  }
}
```

Rules:

1. Parameter names match `^[A-Za-z_][A-Za-z0-9_]{0,127}$` in v1 and are
   case-sensitive.
2. `required: true` and `default` are mutually exclusive.
3. A default is a literal portable value validated at compile time. Defaults
   cannot execute code, read environment variables, or refer to another
   parameter.
4. Run requests carry a JSON object of typed values. Unknown parameters and
   missing required parameters fail before a run is created.
5. The control plane applies defaults and persists the resulting resolved
   parameter object in the immutable run-definition snapshot.
6. SDK/CLI conveniences may parse `--param threshold=0.8`, but parsing is
   driven by the declaration. The wire API does not standardize everything as
   a string.
7. `sensitive: true` controls display, logging, and audit redaction for an
   ordinary inline parameter but does not make it a securely stored secret.
   Actual secrets/resources exclusively use the invocation-level
   `resource_bindings` contract described below.

The run stores a versioned run-input snapshot containing canonical typed values
and the parameter-contract digest. Original CLI/API lexical text may be retained
separately for audit but is never reparsed for execution. Resume uses the
snapshot. This tightens ADR-010's historical claim-time live-resolution
exception for pipelines using typed parameters; implementation must update that
ADR before claiming immutable queued execution generally.

The legacy top-level `params: map[string]string` remains readable during
migration. It is translated to optional string declarations with defaults and
never silently merged with a conflicting `parameters` declaration.

### 4. Portable type descriptor

The **Brokoli Portable Type Descriptor** (BPTD) is a closed, discriminated JSON
model. It is intentionally not the whole of JSON Schema, Python typing, or the
TypeScript type system.

V1 primitives are:

| Kind | Portable meaning | JSON representation |
|---|---|---|
| `boolean` | true or false | boolean |
| `int64` | signed 64-bit integer | tagged canonical string |
| `float64` | IEEE-754 binary64, finite by default | number |
| `decimal` | exact base-10 value | tagged canonical string |
| `string` | Unicode scalar sequence | string |
| `bytes` | arbitrary octets | tagged base64 string |
| `date` | calendar date without timezone | RFC 3339 full-date string |
| `timestamp` | instant in time | tagged UTC timestamp string |
| `duration` | fixed signed duration | tagged seconds/nanoseconds |
| `json` | JSON value with no stronger claim | any JSON value |
| `unknown` | no static type claim | no value encoding of its own |

V1 composites are:

```json
{"kind": "array", "items": {"kind": "string"}}
```

```json
{"kind": "map", "keys": "string", "values": {"kind": "int64"}}
```

```json
{
  "kind": "record",
  "fields": [
    {"name": "id", "type": {"kind": "int64"}, "required": true},
    {"name": "email", "type": {"kind": "string"}, "required": false}
  ],
  "additional_fields": false
}
```

```json
{"kind": "enum", "values": ["active", "inactive"]}
```

```json
{
  "kind": "union",
  "tag_field": "kind",
  "value_field": "value",
  "variants": [
    {"tag": "card", "type": {"kind": "record", "fields": []}},
    {"tag": "bank", "type": {"kind": "record", "fields": []}}
  ]
}
```

Enum values are strings in v1. Union tags are unique non-empty strings and a
union value is encoded as `{"kind":"card","value":{...}}` using the
descriptor's declared field names. Recursive unions are not supported.

Nullability is explicit and orthogonal:

```json
{"kind": "string", "nullable": true}
```

An absent optional record field and a present field whose value is null are
different states. SDKs must preserve that distinction where their language can
express it. A language that cannot represent a descriptor losslessly must
require an explicit contract rather than silently narrowing it.

Portable value encoding is distinct from descriptor encoding. Values that JSON
cannot represent losslessly use tagged objects:

```json
{"$bptd": "int64", "value": "9223372036854775807"}
{"$bptd": "decimal", "value": "-1200.50"}
{"$bptd": "bytes", "value": "SGVsbG8="}
{"$bptd": "timestamp", "value": "2026-09-05T12:34:56.123456789Z"}
{"$bptd": "duration", "seconds": "90", "nanos": 500000000}
```

Canonical encoding rules are part of v1 conformance:

1. JSON is UTF-8, rejects duplicate object keys and invalid Unicode scalar
   sequences, and preserves code points without Unicode normalization.
2. Object keys and record descriptor fields are sorted by UTF-8 byte order for
   hashing. Dataset schemas that require presentation/physical column order use
   a separate explicit `column_order` list; SDK declaration order alone is not
   semantic. Array value order remains semantic.
3. `int64` uses `0` or `-?[1-9][0-9]*`, with no leading plus or zero, and must
   be in the signed 64-bit range.
4. `decimal` uses `-?(0|[1-9][0-9]*)(\.[0-9]+)?`; exponent notation and a
   leading plus are forbidden. Trailing fractional zeros are preserved because
   declared scale may be semantic. Negative zero normalizes to positive zero
   with the same scale.
5. `float64` is finite and hashes using the shortest round-trippable IEEE-754
   representation; `-0` normalizes to `0`. NaN and infinities are not portable
   v1 values.
6. `bytes` uses padded RFC 4648 standard base64, never URL-safe or unpadded
   alternatives.
7. `timestamp` uses UTC `Z`, a four-digit year, and zero, three, six, or nine
   fractional digits. Offset forms normalize before persistence; leap seconds
   are rejected in v1.
8. `duration` is a fixed signed seconds/nanoseconds pair with
   `-999999999 <= nanos <= 999999999`, normalized to one sign. Calendar months
   and years are deliberately not durations.
9. Maps reject duplicate decoded keys. The names `__proto__`, `prototype`, and
   `constructor` have no special behavior in JavaScript adapters.
10. Semantic digests hash this canonical tagged representation, never an SDK's
    native in-memory value.

For `json`, numbers are finite IEEE-754 binary64 values and use the `float64`
canonical hashing rule; integers requiring lossless 64-bit semantics must use
an `int64` descriptor instead. Tagged `$bptd` objects are interpreted only when
the declared descriptor requires that tagged type, so an ordinary `json` object
containing a `$bptd` key is not reinterpreted. `unknown` values are restricted
to canonical `json` values; transporting bytes, int64, decimal, or temporal
values requires a known descriptor. These rules avoid self-describing native
objects leaking through an unknown boundary.

In JavaScript/TypeScript, `int64` maps to `bigint` or an explicit lossless
adapter, never an imprecise `number`. NDJSON codecs preserve tagged values;
Arrow codecs must use lossless logical/physical types and pass the same
round-trip vectors.

The following are deliberately absent from v1: arbitrary classes, object
identity, recursive types, executable validators, language-specific generics,
Python pickle, Java serialization, and JavaScript prototypes. These may not
cross a task boundary under the label of a portable contract.

### 5. Constraints and semantic annotations

Portable types may carry a conservative set of declarative constraints:

- numeric `minimum`, `maximum`, `exclusive_minimum`, and
  `exclusive_maximum`;
- string `min_length`, `max_length`, and RE2-compatible `pattern`;
- array `min_items`, `max_items`, and `unique_items`;
- record field descriptions;
- string `format` values from a closed advertised vocabulary;
- custom semantic annotations under an inert `extensions` namespace.

Constraints have identical server and SDK meanings. A Python callable,
Pydantic validator, TypeScript refinement function, or regular expression with
engine-specific behavior is not serialized as a portable constraint. An SDK
may run richer local validation, but deployment cannot depend on it unless the
portable result is represented in the contract.

### 6. Named task input and output ports

Executable task nodes declare an interface with named ports:

```json
{
  "interface": {
    "contract": "brokoli.task-interface/v1",
    "inputs": {
      "orders": {
        "value": {
          "kind": "dataset",
          "row": {
            "kind": "record",
            "fields": [
              {"name": "id", "type": {"kind": "int64"}, "required": true},
              {"name": "amount", "type": {"kind": "decimal"}, "required": true}
            ],
            "additional_fields": false
          }
        }
      }
    },
    "parameters": {
      "threshold": {
        "type": {"kind": "float64"},
        "required": false,
        "default": 0.5
      }
    },
    "outputs": {
      "result": {
        "value": {
          "kind": "dataset",
          "row": {
            "kind": "record",
            "fields": [
              {"name": "id", "type": {"kind": "int64"}, "required": true},
              {"name": "score", "type": {"kind": "float64"}, "required": true}
            ],
            "additional_fields": false
          }
        }
      }
    }
  }
}
```

Port `value.kind` and BPTD `type.kind` are separate discriminators. A scalar
value is `{"kind":"scalar","type":{"kind":"int64"}}`; a collection is
`{"kind":"collection","items":{"kind":"scalar","type":...}}`. The
canonical schema defines these as separate closed unions rather than one
overloaded Go/SDK type.

Value kinds are:

| Value kind | Meaning |
|---|---|
| `dataset` | zero or more records with an optional row descriptor |
| `scalar` | one portable value |
| `artifact` | opaque bytes addressed by an artifact reference |
| `collection` | a finite collection of separately addressable portable values |
| `control` | no business value; ordering/routing only |

Artifacts may constrain `media_types` and carry an optional logical type, but
their bytes remain opaque to the scheduler. Collections declare their item
value contract, ordering (`ordered` or `unordered`), and a stable item-key
descriptor used by ADR-015 to derive physical instance identity. Duplicate
keys are invalid even when duplicate values are allowed. Dataset schemas may
be open (`additional_fields: true`) or unknown by using
`{"kind":"unknown"}` for `row`; unknown does not mean “proven compatible.”
A scalar nests one BPTD descriptor. An artifact port carries an ADR-012
reference and optional media-type set. A control port carries no business
value, has cardinality one, and cannot be read by task code.

Ports declare cardinality (`one`, `optional`, or `many`). Every required input
port must have exactly one binding unless its cardinality is `many`; every
emitted output must be declared; an absent optional output is explicit.
Duplicate bindings are rejected. Fan-out may read one immutable output from
many consumers. Fan-in requires a `many` input or an explicit combining node.

Edges identify ports:

```json
{
  "from": "score_orders",
  "from_port": "result",
  "to": "publish",
  "to_port": "orders"
}
```

Existing edges without ports map to the single conventional `result` output
and `input` input. A node with multiple data inputs or outputs must use explicit
ports. Port names are stable logical identity and cannot be inferred from edge
order.

### 7. Task parameters are declared in the interface and bound by invocation

A task interface declares local parameter names, portable types, requiredness,
and task-level defaults, as shown above. The pipeline node invocation, not the
reusable interface, binds those local names:

```json
{
  "parameter_bindings": {
    "threshold": {"pipeline_parameter": "threshold"},
    "mode": {"literal": "strict"}
  }
}
```

Bindings are checked directionally: a pipeline parameter's type must be
assignable to the task parameter type, and a literal must validate against it.
A task default applies only when that local parameter has no invocation
binding; a pipeline default is resolved before a bound value reaches the task.
Required task parameters need a binding or task default.

The task implementation receives a resolved parameter object. SDK-generated
wrappers may bind those values to native function arguments, but positional
argument order is not part of the portable ABI. This avoids making Python's
signature conventions or TypeScript destructuring semantics authoritative.

Bindings may reference a pipeline parameter or contain a portable literal.
Expression evaluation, arbitrary templates, and cross-parameter computation
are not part of v1. Resource and secret bindings use a separate
invocation-level `resource_bindings` object. It maps declared local names to
immutable, tenant-scoped resource IDs and allowed operations. The control plane
resolves them to opaque attempt capabilities through runtime context. They are
not BPTD parameters, and task code cannot discover undeclared secret names.

Interface, implementation, and invocation have separate semantic digests.
Bindings, literals, resource bindings, and requested validation policy affect
the invocation digest, not the reusable interface digest.

### 8. SDK inference is an adapter, not authority

SDKs should infer the least contract they can prove:

```python
class ScoredRow(TypedDict):
    id: int
    score: float

@task
def score(rows: list[InputRow], threshold: float = 0.5) -> list[ScoredRow]:
    ...
```

```typescript
type ScoredRow = { id: bigint; score: number };

const score = task<InputRow, ScoredRow>({
  parameters: { threshold: parameter.number({ default: 0.5 }) }
}, async (rows, ctx) => { /* ... */ });
```

Both may compile to the same portable interface. Neither language syntax is
stored in it.

Inference rules:

1. No annotation means unknown, not `json` and not an inferred sample shape.
2. Unsupported, recursive, ambiguous, or lossy native types require an
   explicit portable contract when strict mode is enabled; otherwise the SDK
   emits only what it can prove and reports a visible warning.
3. Explicit contracts override inference only after the SDK proves that any
   native annotation is not contradictory.
4. Default values are validated against the portable descriptor during
   compilation.
5. SDK-generated metadata may record the inference source for diagnostics,
   but source metadata has no execution semantics.
6. TypeScript SDKs cannot recover erased generic types at runtime. They provide
   schema builders, generated contracts, or build-time tooling; they do not
   pretend reflection exists.
7. Python SDKs resolve annotations without importing arbitrary modules during
   discovery. Unresolvable forward references remain unknown or require an
   explicit contract.

Every official SDK must pass common fixtures that map native declarations to
the same normalized JSON. Language-specific tests are additive; they cannot
replace the cross-language fixtures.

### 9. Assignability and graph validation

Graph checking returns `assignable`, `incompatible`, or `unverified`.
`Unverified` requires runtime validation and may be rejected by deployment
policy; it is never displayed as statically compatible. Rules are directional
from values a producer may emit to values a consumer accepts:

1. Value kinds must match, except for explicit conversion nodes.
2. A producer record satisfies a consumer when every required consumer field
   is required on the producer with an assignable type. Producer extras are
   allowed only when the consumer is open; optional producer fields cannot
   satisfy required consumer fields.
3. An open or unknown producer does not statically prove a closed consumer. If
   the consumer relies on that concrete schema, the edge requires `full`
   validation before invocation or is rejected by policy. Sample validation
   may provide diagnostics but cannot satisfy a task precondition. A consumer
   can explicitly accept unchecked values with an open/unknown input contract.
4. Required fields are invariant with respect to absence; nullable values are
   assignable only to nullable consumers.
5. Numeric widening from `int64` to `float64` is not implicit because values
   beyond 2^53 lose precision in common runtimes. Conversion must be explicit.
6. Enum producers are assignable when their value set is a subset of the
   consumer's set.
7. Every media type a producer may emit must be accepted by the consumer. A
   concrete runtime artifact carries one selected media type.
8. Collection item and dataset row descriptors are checked recursively.
9. Arrays and maps are covariant in their contained descriptor; map keys remain
   strings in v1.
10. Every producer union variant must have a matching consumer tag and
    assignable payload. Union-to-non-union requires explicit conversion.
11. Producer constraints must imply consumer constraints: producer ranges and
    lengths must be subsets of accepted consumer ranges and lengths. Patterns
    are assignable only when identical; otherwise the result is `unverified`.
12. `json` is assignable only to `json`; `unknown` yields `unverified` except
    when both sides are `unknown`.

Compatibility failures identify the edge, ports, and shortest failing type
path, for example:

```text
publish.orders <- score_orders.result: required field $.score expects
float64 but producer declares string
```

The normalized interface and compatibility result are deterministic. Map key
order, SDK source order, and documentation-only fields cannot change semantic
digests.

### 10. Validation modes and runtime honesty

Each input/output port has a validation mode:

| Mode | Behavior |
|---|---|
| `none` | Kind/reference integrity only; no field validation |
| `sample` | Validate a deterministic bounded sample and report coverage |
| `full` | Validate every value crossing the boundary |

The default is policy-controlled: `full` for parameters and scalars;
`sample` for datasets; reference integrity and metadata validation for
artifacts. A deployment may strengthen a requested mode but cannot weaken the
server's minimum policy.

Runtime validation errors are first-class task failures with:

- direction (`input`, `output`, or `parameter`);
- port or parameter name;
- contract path;
- expected descriptor;
- redacted observed kind/value summary;
- checked row count and validation mode.

Sampling never permits the system to claim static assignability or that an
entire dataset was valid.
The run record states what was checked. Validation may be fused into an
existing read/write boundary, but it must not silently materialize a stream or
duplicate a large dataset.

For immutable materialized datasets, `sample` selects stable row indices using
`SHA-256(dataset_manifest_digest || interface_digest || algorithm_version)` as
the seed for the normative sampling algorithm. The run records algorithm
version, seed, population count, selected indices, and checked count, so retries
validate the same rows. For one-pass streams, sampling means deterministic
validated-on-read coverage (for example every Nth record from a digest-derived
offset), never random coverage. Input validation occurs before user code
observes a value, and output validation occurs before the trusted worker commits
it. Security-critical policy requires `full`.

### 11. Contract evolution and deployment compatibility

Every deployed pipeline version stores the normalized interface and its
semantic digest. Contract comparison classifies changes from the perspective
of existing callers and downstream consumers:

| Change | Classification |
|---|---|
| Add optional parameter with default | backward-compatible |
| Add required parameter | breaking |
| Remove parameter | breaking for callers that still send it |
| Widen accepted enum/input constraint | backward-compatible input change |
| Narrow accepted input | breaking |
| Producer adds optional output field accepted by an open consumer | compatible |
| Remove or change required output field | breaking |
| Change value kind | breaking |
| Documentation-only change | non-semantic |

The server reports compatibility; it does not automatically block every
breaking change because a new immutable pipeline version may intentionally
break callers. Deployment policy decides whether acknowledgment, a new logical
pipeline ID, or downstream migration is required. Historical runs always
retain the exact contract digest they used.

### 12. Capability negotiation and phased adoption

Portable interfaces require the execution feature `task-interface-v1`.
Specific optional type or validation features use separately advertised names,
for example `task-type-decimal-v1` or `task-validation-full-v1`. Unknown
required features fail before persistence.

Port-aware interfaces and edges enter the canonical external schema in IR 2.2
and additionally require `task-ports-v1`. IR 2.0/2.1 readers reject the new
fields rather than discard them. Omitted ports translate to `result -> input`
only when both nodes expose exactly one compatible data port; otherwise they
are ambiguous. Conditional edge labels remain routing predicates, independent
of control/data port identity.

Rollout is incremental:

1. Add canonical schema definitions and fixtures without changing execution.
2. Emit interfaces for built-in nodes whose contracts are already known.
3. Add SDK inference and explicit-schema builders.
4. Add graph assignability and typed run-parameter validation.
5. Add runtime boundary validation through ADR-033 adapters.
6. Require interfaces for new executable task kinds while retaining the legacy
   implicit single-dataset contract for old IR versions.

An SDK must not advertise a compile-only interface as executable. If the target
server cannot validate or execute a required contract feature, deployment is
rejected under ADR-014.

### 13. Developer experience requirements

Portable contracts are infrastructure, but the default user experience stays
native:

- common Python annotations and defaults compile without additional classes;
- TypeScript offers typed builders and generated helpers that preserve static
  inference;
- JSON/YAML users can author the same contract directly;
- IDEs show required parameters, defaults, descriptions, and output ports;
- `brokoli compile` displays inference warnings and the normalized contract;
- `brokoli diff` separates graph, parameter, interface, implementation, and
  visual changes;
- `brokoli run` validates and parses parameters locally before an API call;
- the UI generates run forms from parameter declarations and previews schema
  incompatibilities on edges;
- local task tests can validate native values against exactly the same
  conformance vectors as the server;
- error messages use source-language names where metadata provides them while
  retaining portable contract paths.

Users can start with no detailed row schema and add precision where it creates
value. Strict organizational policy may require complete contracts, but the
base SDK does not turn every small pipeline into schema boilerplate.

### 14. Normative ownership and conformance

The core repository owns:

- JSON Schema definitions for BPTD and task interfaces;
- normalized semantic-digest rules;
- assignability rules and positive/negative vectors;
- parameter default/coercion vectors;
- native-language mapping guidance;
- runtime validation error fixtures.

Every official SDK and runtime adapter consumes the same versioned fixtures.
Equivalent Python, TypeScript, and declarative examples must normalize to the
same interface. Differential CI runs released SDKs against the next core
contract before either side claims compatibility.

## Consequences

### Positive

- Any language can author tasks against one documented contract rather than
  reproducing Python semantics.
- Missing and malformed run parameters fail before work is scheduled.
- The UI and CLI can provide typed forms, completion, validation, and precise
  errors without loading user code.
- Output kinds and schemas survive compilation, enabling graph compatibility,
  lineage, documentation, and safer refactoring.
- Runtime adapters receive an explicit value contract instead of reverse
  engineering generated scripts.
- Unknown schemas remain possible and honest; adoption can be gradual.
- Immutable contract digests make deployment review and historical runs
  auditable.

### Negative

- Brokoli acquires a portable type system and must maintain its semantics and
  conformance fixtures across languages.
- Native type systems are richer than BPTD. Some annotations will require
  explicit adapters or lose non-portable detail.
- Runtime validation consumes CPU and may scan data; policy and sampling must
  make that cost visible.
- Typed parameters require API, CLI, persistence, UI, and SDK migrations away
  from `map[string]string`.
- Port-aware edges and multi-output tasks add graph and visualization
  complexity.
- Compatibility classification is directional and more nuanced than equality.

### Deferred

- Recursive types and user-defined portable logical types.
- Schema registries and compatibility policy shared across pipelines.
- Automatic schema inference from external data samples.
- Stateful or infinite stream contracts such as watermarks and event-time
  guarantees.
- Column-level policy labels and automated data-governance enforcement.
- A query language for parameter expressions or computed defaults.

## Alternatives considered

- **Make JSON Schema itself the task type system.** Rejected because its many
  drafts, open extension vocabulary, numeric ambiguity, and validator-specific
  behavior make cross-language runtime equivalence difficult. The canonical IR
  remains JSON Schema, while BPTD is a small closed model embedded within it.
- **Make Python annotations canonical.** Rejected because Python import paths,
  classes, unions, and runtime conventions are not a language-neutral ABI.
- **Make TypeScript types canonical.** Rejected because they are erased at
  runtime and include structural features with no portable wire meaning.
- **Use Apache Arrow schemas as the whole contract.** Rejected because Arrow is
  excellent for tabular data but does not model pipeline parameters, artifacts,
  control ports, or arbitrary scalar/collection boundaries. An Arrow transport
  may implement a BPTD dataset contract under ADR-012/ADR-033.
- **Use Protobuf as the only authoring contract.** Rejected by ADR-014 for the
  user-visible pipeline format. Protobuf remains suitable for generated models
  or internal transports if it passes the same canonical JSON fixtures.
- **Infer schemas from the first returned row.** Rejected because empty data,
  rare fields, nulls, and heterogeneous rows make it nondeterministic and
  unsafe. Runtime observation may produce suggestions, never authoritative
  contracts.
- **Require complete schemas for every task immediately.** Rejected because it
  would destroy low-ceremony authoring and make migration impractical. Unknown
  schemas are explicit and policy can tighten requirements later.
- **Keep parameters as strings and let tasks parse them.** Rejected because it
  duplicates parsing, prevents useful UI and admission validation, and makes
  behavior language-dependent.
- **Serialize native objects between tasks.** Rejected because pickle,
  JavaScript object serialization, JVM serialization, and equivalent formats
  couple edges to one runtime, create security risks, and prevent independent
  workers from participating.

## Industry evidence

- Apache Beam's Runner API separates SDK-authored graph types, coders,
  environments, and required capabilities from the language-owned SDK harness.
  Cross-language boundaries permit only portable coders. This validates the
  separation between authoring types and a smaller wire contract:
  <https://beam.apache.org/roadmap/portability/>.
- Flyte's `TaskTemplate` combines a strongly typed interface with a separately
  extensible execution target; its IDL explicitly uses that interface for
  compile-time workflow validation:
  <https://github.com/flyteorg/flyte/blob/master/flyteidl/protos/flyteidl/core/tasks.proto>.
- Temporal treats payload bytes and their metadata as an SDK data-converter
  concern rather than teaching the orchestration service application types.
  This supports a control plane that validates portable metadata while leaving
  native conversion to adapters: <https://docs.temporal.io/dataconversion>.
- Tekton's string/array/object parameter model and file-based small results show
  the operational value of explicit parameters, but also the limitations of a
  vocabulary too narrow for data pipelines:
  <https://tekton.dev/docs/pipelines/tasks/>.

These systems do not provide a contract Brokoli can copy unchanged. The shared
lesson is to make portable values explicit at process/language boundaries and
keep native types inside the SDK or runtime adapter that owns them.

## Follow-ups

- Publish `brokoli.task-interface/v1` JSON Schema and normative fixtures in the
  core schema package.
- Add a canonical normalized interface digest and directional compatibility
  implementation in Go.
- Replace the legacy string-only run parameter API with typed JSON values while
  preserving an explicit migration adapter.
- Add port-aware edges and built-in node interface declarations.
- Add Python annotation/`TypedDict`/dataclass adapters and explicit schema
  builders.
- Add TypeScript schema builders and optional code-generation support; do not
  depend on erased generic reflection.
- Add SDK differential tests proving equivalent declarations produce identical
  normalized contracts.
- Implement runtime boundary validation through ADR-033's task protocol.
- Add UI run-form generation and edge compatibility diagnostics.
- Update ADR-014's deferred dataset-schema item when the canonical schema lands.

## Acceptance gates

This ADR remains proposed until all of the following are demonstrated:

- IR 2.2 schemas define parameters, ports, value contracts, BPTD, and closed
  extension points, with positive and negative fixtures for every composite;
- canonical value encoding and semantic digest vectors pass byte-for-byte in
  Go, Python, and TypeScript, including 64-bit boundaries, temporal values,
  field-order permutations, large JSON numbers, `$bptd` key collisions, and
  unknown JSON-only values;
- assignability vectors cover every primitive, composite, nullability,
  requiredness, constraint, openness, and unknown outcome;
- typed run inputs are snapshotted and resume without reparsing or consulting a
  changed pipeline definition;
- one equivalent Python, TypeScript, and JSON-authored task compiles to the same
  normalized interface and invocation digests;
- one Python-produced dataset is accepted by a TypeScript consumer and one
  incompatible contract is rejected before persistence;
- SDK commands provide stable diagnostic codes and non-zero exits for invalid
  annotations, defaults, bindings, and run values;
- ADR-014 and ADR-010 are updated wherever implementation changes their
  persisted-version or queued-run semantics.

## Update (2026-09-05)

This ADR does not reference ADR-029, ADR-030, or ADR-031, all shipped in
the two weeks before it. It does not conflict with any of the three
directly — it defines a type/interface contract, not an execution
mechanism — but ADR-033 (stacked on this one) does conflict with ADR-029
and ADR-031 in ways that needed resolving before implementation. See
ADR-035.

## Update (2026-09-05) — rollout step 1 landed

Per section 12's incremental rollout, step 1 ("add canonical schema
definitions and fixtures without changing execution") is done:
`docs/schema/task-interface-v1.json` (the BPTD type descriptor language,
named ports, and parameter declarations), the companion canonical
value-encoding spec at `docs/schema/task-interface-canonicalization.md`,
positive/negative fixtures, and `models/task_interface_schema_test.go`.
Not yet done, deliberately, per that same document's own scope note: a
Go digest/assignability *implementation* (the schema is the spec it must
match, not the implementation itself), a tagged-value JSON Schema and its
round-trip vectors, and wiring `task_interface` into the pipeline IR
(IR 2.2 / `task-ports-v1`) — there is no Go struct to bind it to yet, so
this ADR is not close to its acceptance gates.

## Update (2026-09-05) — rollout step 2 landed, partially

`docs/schema/pipeline-ir-2.2.json` publishes the additive, optional
node-level `interface` and pipeline-level `parameters` fields (both
`$ref`-ing `task-interface-v1.json`), proven a true superset of every
2.0/2.1 pipeline this server already accepts. `api/capabilities.go`
gains `node_type_interfaces`, a reference table (mirroring the existing
`node_type_capabilities`) giving 13 of the ~19 built-in node types their
already-known ADR-032 interface, each validated against the schema by a
test.

Five node types are named and deliberately excluded rather than
silently skipped: `source_api` (its output kind depends on per-node
config, not its type alone — a static per-type table cannot say that
honestly), `join` (takes exactly two inputs, but `models.Edge` carries
no port identity — edge order, not a named port, currently distinguishes
them), `dbt` (output shape genuinely varies by config), `wait` (the SDK
never wires it an input, and its unconnected semantics are murkier than
the other control-flow nodes), and `sql_generate`/`code` (not
investigated / not knowable ahead of schema derivation, respectively).

Still not done at that point: this table was a discovery/reference
surface only, exactly like `node_type_capabilities` itself — no deploy
or validate path read it, no `models.Node` carried a persisted
`interface`, and no `task-ports-v1` capability was advertised
(advertising it before anything actually accepted these fields would be
the same dishonest-capability gap `sdk#86`'s fail-closed fix was built
to prevent).

## Update (2026-09-05) — rollout step 2 complete

`models.Node` gains a real `Interface map[string]interface{}` field and
`models.Pipeline` gains `Parameters map[string]interface{}` (both
`json:",omitempty"`, free-form like `Node.Config` already is — full BPTD
typing stays in the JSON Schema and its contract tests, not a parallel
Go type system). `models/ir_schema_contract_test.go` now targets
`pipeline-ir-2.2.json` directly (its own fully-populated fixture carries
a real node `interface` and pipeline `parameters`), gains a
`TestSchemaAndModelDeclareTheSameNodeFields` sweep — `models.Node`'s own
counterpart to the existing pipeline-level two-way sweep, which never
existed before this change — and two new contract-violation cases
(malformed node interface; a pipeline parameter with both `required:true`
and a `default`). `SupportedIRVersions` gains `"2.2"`
(`TaskInterfaceIRVersion`): structural acceptance only, since nothing in
`Validate()` or run admission consults either field's contents yet.

Still, deliberately, not done: no `task-ports-v1` capability advertised
(nothing validates or executes against these fields yet — that's ADR-032
rollout step 4), and `nodeTypeInterfaces` still isn't attached to any
real, deployed node. Tracked in issue #439, step 2 now fully checked off.

## Update (2026-09-05) — rollout step 4 started: the assignability engine

`pkg/taskinterface` implements the Brokoli Portable Type Descriptor as
real Go types (`Type`, `Field`, `Variant`, `PortValue` — this is the
first point in the arc where BPTD needed real typed Go structs rather
than the free-form `map[string]interface{}` `Node.Interface`/
`Pipeline.Parameters` use; a discriminated recursive comparison over raw
maps would have been unsafe) and `AssignPort`/`AssignType`, implementing
all twelve directional rules from section 9 — including int64/decimal
canonical-string range comparison via `math/big.Float` (proven against a
2^62-scale boundary case, well past float64's exact-integer limit), the
`required:true`+optional-producer-field rejection, the closed-record
"producer extras" rule, union variant matching, and the exact worked
diagnostic this ADR itself gives as an example
(`publish.orders <- score_orders.result: required field $.score expects
float64 but producer declares string` — pinned as a test). 88.3% test
coverage; every non-trivial rule verified load-bearing by mutation
(temporarily inverting the rule, confirming the expected test fails,
reverting).

Deliberately not in this PR, matching every prior step's staging
discipline: **wiring `AssignPort` into `engine/validate.go`'s edge
checking**, so a real pipeline's connected node interfaces actually get
checked at save/deploy time, and **typed run-parameter validation and
snapshotting** (ADR-032 section 3's other step-4 half) — both real,
separate slices, tracked in issue #439. Today, nothing calls this
package outside its own tests.

## Update (2026-09-05) — assignability wired into engine/validate.go

`engine/validate.go`'s `validateEdgeAssignability` now calls
`pkg/taskinterface.AssignPort` for every edge whose two nodes both have a
known interface (the node's own `Interface` if set — nothing populates
that yet — else `models.NodeTypeInterfaces`'s reference entry). Only
`Incompatible` is a hard validation error; `Unverified` is left
non-blocking, since `ValidationError` has no warning channel yet and
`Unverified` is explicitly not a proven violation.

This required moving `nodeTypeInterfaces` out of `api/capabilities.go`
into `models.NodeTypeInterfaces`: `engine` cannot import `api` (`api`
already imports `engine`), so the reference table needed a home both
packages can reach. `api/capabilities.go` now reads
`models.NodeTypeInterfaces` for the same `GET /api/capabilities`
response it always exposed.

**Why this is safe to land now, verified rather than assumed:** every
reference-table interface declares `row: unknown` except `migrate`'s
(which can never appear as an edge's producer — `migrate` cannot have
outgoing edges, checked earlier in the same function) — and two unknown
rows are always `Assignable` by rule 12's principle. So this wiring
cannot reject any pipeline reachable through today's SDKs; the full
existing `engine` test suite (including every `ValidatePipeline` test)
passes unchanged. What it *does* do is make the mechanism genuinely
live: an explicit, incompatible `Node.Interface` (the shape a future SDK
inference step, or a hand-authored pipeline, could set today) is caught
at validate time, with a test proving it, and mutation-proving the
wiring itself is load-bearing (temporarily removed the call, confirmed
the expected test fails, restored).

Still not done: typed run-parameter validation/snapshotting (ADR-032
section 3's other half of step 4) and `Unverified` has no surfaced
warning path yet. Tracked in issue #439.

# Cross-SDK differential fixtures (ADR-032 section 14, rollout step 3)

These vectors are the "native-language mapping guidance" the core repo
owns per ADR-032 section 14: each one pairs an illustrative Python
declaration and an illustrative TypeScript declaration with the single
normalized JSON both must compile to. They exist to pin *cross-language
agreement*, not type-system coverage -- the step-1 fixtures in
`../positive`/`../negative` already exhaustively cover the BPTD type
descriptor language and the schema's structural rules.

Every official SDK's own test suite loads this directory and asserts its
actual compiled output matches the relevant `expected_*` field exactly
(after JSON key-order normalization). `models/task_interface_differential_fixtures_test.go`
in this repo validates the vectors themselves structurally, as a
drift guard: each non-null `expected_node_interface` must validate
against `task-interface-v1.json`'s root `task_interface` shape, and each
non-null `expected_pipeline_parameters` against its `pipeline_parameters`
`$def`.

## Fixture shape

```json
{
  "description": "what this vector exercises, in plain language",
  "python": "an illustrative (not executed) Python declaration",
  "typescript": "an illustrative (not executed) TypeScript declaration",
  "expected_node_interface": { ... } | null,
  "expected_pipeline_parameters": { ... } | null
}
```

A vector sets exactly one of `expected_node_interface` /
`expected_pipeline_parameters` to a real value and the other to `null` --
each one exercises either row-schema inference or parameter inference,
never both, so a failure names its cause unambiguously.

## Why `expected_node_interface` never has its own `parameters` key

ADR-032 section 6's worked example shows a task interface with its own
`parameters` block (task-local declarations, bound per node invocation
via section 7's `parameter_bindings`). Rollout step 3 deliberately does
not populate that block: `parameter_bindings`/`resource_bindings` don't
exist anywhere in the core IR or engine yet, so a task-interface-level
parameter declaration would have no binding mechanism to ever reach a
running task -- an orphaned, schema-valid but semantically dead field.
Section 7 belongs with step 5 (ADR-033 runtime adapters), where there's
finally an execution consumer for a resolved binding.

Until then, an SDK-inferred task keyword parameter (with a type
annotation, optionally a default) is emitted as a **pipeline-level**
parameter instead -- the mechanism ADR-032 rollout step 4 already wired
end to end (`Pipeline.Parameters`, resolved and validated at run trigger
time by `taskinterface.ResolveParameters`, issue #439). That's what
`required-parameter.json` and `defaulted-parameter.json` below pin: a
`region: str` keyword becomes a *required pipeline parameter* named
`region`, not an entry in the task's own interface.

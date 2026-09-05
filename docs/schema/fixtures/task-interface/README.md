# Task interface fixtures (ADR-032)

Validated against `../../task-interface-v1.json` by
`models/task_interface_schema_test.go` on every test run.

- `positive/*.json` — every file must **pass** validation. Each one
  exercises a distinct part of the type descriptor language or the
  interface shape, named for what it covers.
- `negative/*.json` — every file must **fail** validation. Each one
  isolates exactly one violation; the filename names the violation, and
  a comment field (`_violation`) states which rule it breaks. `_violation`
  is not part of the schema — it is stripped before validation by the
  test, purely for a human reading the fixture.

A failure in either direction means the schema and this fixture set have
drifted apart: one of them changed on purpose, and the other must catch
up in the same change.

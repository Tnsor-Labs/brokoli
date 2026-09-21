# Native Relational Operators

Native relational nodes keep common row operations in the pipeline IR instead
of routing them through a language runtime.

## Filter

`filter` accepts `expression_version: 1` and a `predicate` expression. Supported
predicate operators are `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `and`, `or`,
`not`, and `is_null`. Scalar expressions also support `case_when` with
`branches` containing `when` and `then`, plus an `else` expression.

Native filtering preserves input columns and treats a null comparison as
false. `is_null` is the explicit null predicate.

## Code Output Schemas

Code nodes may declare `output_schema` using the
`brokoli.dataset-schema/v1` contract. The engine validates the declaration and
propagates its column names and known primitive types to downstream sinks.
Undeclared output columns are controlled by the schema's `additional_columns`
setting.

## Pushdown

A native filter may be compiled into a same-database source-to-sink segment
when its predicate is proven equivalent to SQL for the source column types.
Unsupported predicate shapes remain on the interpreted path rather than
changing results.

# ADR-003: Plugin hybrid model — Go first-class + subprocess long-tail

**Status:** accepted
**Date:** 2026-04-14

## Context

ADR-002 establishes a subprocess plugin protocol that lets us grow
the connector ecosystem. A second question is whether we should
abandon in-core connectors entirely and push everything into
plugins, or keep some connectors first-class.

Plugin subprocess calls have real costs:

1. Spawn cost (~100 ms for a Python plugin)
2. No rich UI integration — the UI renders a config form from the
   JSON Schema in the plugin manifest, but specialized affordances
   (OAuth flows, schema introspection with interactive field
   mapping, live connection testing) are harder to bolt onto a
   subprocess boundary
3. Users can't inspect a plugin's source when debugging — it's
   installed as a binary or a Python wheel

These costs matter for the connectors that are hit every day: the
cloud data warehouses (Snowflake, BigQuery, Redshift, Databricks)
and the blob stores (S3, GCS, Azure Blob). They don't matter for the
SaaS long tail (Stripe, HubSpot, Slack, Notion) where a 100 ms
startup cost rounds to zero against API latency.

## Decision

**Top ~15 connectors live in Go, compiled into the core binary.**
Everything else lives as subprocess plugins via the ADR-002 protocol.

First-class connectors as of this ADR (some implemented, some
planned):

| Category | Connectors |
|---|---|
| Databases | Postgres, MySQL, SQLite (already) |
| Warehouses | Snowflake, BigQuery, Redshift, Databricks SQL (planned) |
| Object stores | S3, GCS, Azure Blob (planned) |
| Streaming | Kafka, Kinesis (planned) |
| Integration | dbt (planned — native, not via the generic `code` node) |
| Web | REST API (already), File (already) |

Everything else is a plugin.

First-class connectors get **tight UI integration**: schema
introspection, OAuth flows, connection test with live feedback,
typed config forms with autocomplete, icon in the node palette.
Subprocess plugins get the **generic plugin UX**: a JSON Schema form
and a "check connection" button that exercises the plugin's `check`
command.

Symmetrically to the plugin protocol, the first-class connectors use
the same `extensions.NodeExecutor` interface internally. There's one
execution path in the engine — the `Executors` loop — and both the
plugin manager and any future "FirstClassConnectorExecutor" plug
into it the same way.

## Consequences

### Positive

- Top-80% connectors are fast, tightly integrated, and can be
  audited by anyone reading the Brokoli source.
- Long-tail connectors don't burden the core: a new Stripe plugin
  version doesn't require a Brokoli release.
- Community contributes to the long tail without touching the core,
  lowering the contribution bar.
- We avoid the Airflow "everything is a provider" trap where the
  core maintainers have to review PRs for 300 integrations they
  don't use.

### Negative

- **Two code paths to maintain.** First-class connectors have rich
  UI; plugins have generic UX. Features added to one have to be
  mirrored in the other or consciously not.
- **The promotion story is unclear.** If a plugin becomes wildly
  popular, does it get "promoted" to a first-class Go connector?
  Who decides? Undefined for now; re-visit when it's a real problem.
- **The first-class list is opinionated.** The list above is our
  current bet on what the 80/20 looks like. Some users will
  disagree. They can override by building their chosen connector
  as a plugin — the downside is just that they don't get the
  first-class UI polish.

### Deferred

- **Actually building the first-class warehouse connectors.** As of
  v0.8.0 the plugin runtime exists but none of the warehouse
  connectors are implemented. Phase 3 work.

## Alternatives considered

- **Everything as plugins.** Rejected because the cold-start cost of
  the most-used connectors compounds into a painful first-run
  experience, and the UI integration for warehouse connectors
  (schema intro, OAuth) doesn't survive a generic JSON Schema form.
- **Everything in Go.** Rejected because it doesn't scale — we'd
  burn the next year writing connectors instead of building product.
- **A three-tier system: core connectors + official plugins + community
  plugins.** More hierarchy than we need right now. The two-tier
  system (first-class Go vs plugin) is simpler and can be
  subdivided later if the plugin ecosystem grows enough to need
  official-vs-community distinctions.

## Follow-ups

- Phase 3: implement Snowflake, BigQuery, S3 as first-class Go
  connectors, each wrapping the plugin manager's NodeExecutor
  interface for consistency.
- Plugin promotion policy — write it when the first community plugin
  hits widespread use.

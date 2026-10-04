# ADR-043: Arrow-native database migration transport

**Status:** proposed
**Date:** 2026-10-04

## Context

`migrate` can now divide an explicit key range into independent, idempotent
upserts. For a PostgreSQL source and destination on the exact same endpoint,
the engine stages the query inside PostgreSQL. For distinct databases, it uses
the portable path:

```text
database/sql cursor -> common.DataSet row maps -> destination bulk writer
```

The portable path now streams bounded batches, rather than materialising the
entire source result. In a real 100,000-row PostgreSQL-to-separate-PostgreSQL
benchmark, four partitions reached 205,339 rows/second, 2.47x the single
stream. The result still pays for scanning values into row maps and for COPY
rendering those maps back to text.

Brokoli already supports Arrow IPC for task and artifact datasets. That codec
does not make the migration path columnar: it infers an Arrow schema from a
`common.DataSet`, and its reader deliberately returns the same row-map shape.
Inserting it here would be:

```text
row maps -> Arrow IPC -> row maps
```

It adds allocation, serialization, and decoding without removing either
database conversion. That is not an Arrow-native transport and must not be
marketed as one.

The desired future path is:

```text
Arrow-native source cursor -> Arrow record batches -> columnar destination writer
```

This ADR decides how to choose a source protocol before adding a dependency or
claiming a performance result.

### Constraints

- Preserve the logical dataset contract: nulls, signed 64-bit integers,
  decimals, timestamps, bytes, and column order cannot change with transport.
- Keep `database/sql` streaming as the portable fallback; Arrow support is an
  optimization, never a new requirement to run a migration.
- Do not ship a CGO-only default path. Release targets include
  `CGO_ENABLED=0` builds.
- Preserve ADR-022 outbound-network policy and ADR-041 credential scoping.
- Keep source credentials off work orders and resolve connection references on
  the worker, as the existing remote migrate path does.
- Benchmark a real independent source and destination before enabling a path
  by default. Two databases on one PostgreSQL server exercise the portable
  path, but do not measure network or independent-server capacity.

## Decision

Do not select ADBC, Flight SQL, or a bridge globally yet. Define an
**optional Arrow source capability** and accept a first implementation only
when it provides a pure-Go production driver, a bounded columnar destination
writer, type-fidelity conformance tests, and an end-to-end benchmark against
the existing streamed path.

The first implementation is chosen per source backend, not per destination:
different databases expose Arrow through materially different protocols. The
portable streaming path remains the default until that backend has earned its
Arrow capability.

### Acceptance gate

A proposed Arrow-native source path must demonstrate all of the following:

| Gate | Required evidence |
| --- | --- |
| Driver | Maintained production driver; license review; pure-Go builds for supported releases, or an explicitly optional deployment component. |
| Credential boundary | Connection reference resolves on the executing worker; no secret enters an Arrow URI, artifact, log, or work order. |
| Network policy | Every control, data, token, and gRPC/HTTP connection observes ADR-022 policy. |
| Fidelity | Corpus proves exact round trips for nulls, `int64` near and above `2^53`, decimals, timestamps with offsets, bytes, Unicode, and driver-specific values. Unsupported types refuse or use the row-stream fallback. |
| Backpressure | Source does not advance beyond a bounded number of Arrow record batches while the destination is blocked. Cancellation releases both cursors. |
| Destination | Writer consumes Arrow arrays directly. Encoding Arrow IPC and immediately decoding rows is not acceptable evidence. |
| Performance | Repeated, end-to-end independent-server benchmark records rows/second, peak memory, and destination correctness against the current streamed COPY path. |
| Operations | Metrics identify selected transport, fallback reason, batch rows/bytes, cancellation, and source/destination time. |

## Consequences

### Positive

- Avoids a cosmetic Arrow change that would slow the current migration path.
- Allows databases that genuinely expose Arrow to remove row-map conversion.
- Keeps the proven bounded streaming path available for every existing driver.
- Makes driver, type, security, and performance obligations reviewable before
  a backend claims Arrow support.

### Negative

- No immediate Arrow IPC speedup is claimed for PostgreSQL through its normal
  wire protocol.
- Backends may need different Arrow integrations, increasing connector
  surface area.
- A columnar destination writer is additional implementation and test work;
  the current PostgreSQL COPY writer accepts row maps.

### Deferred

- Selecting the first Arrow-native source backend.
- A common columnar batch interface between source readers and destination
  writers.
- Distributed benchmark infrastructure with independently provisioned source
  and destination servers.
- Flight endpoint lifecycle, tenant isolation, and observability if a bridge
  becomes necessary.

## Alternatives considered

### ADBC PostgreSQL driver

**Pros**

- Defines a standard columnar database API and returns Arrow record batches.
- Could provide one source abstraction across databases that implement ADBC.
- Fits the desired source-cursor-to-columnar-writer shape.

**Cons**

- PostgreSQL ADBC availability and operational maturity must be verified for
  Brokoli's release targets; some ADBC drivers depend on native libraries.
- Adds driver distribution, version, TLS, and authentication support separate
  from the proven pgx path.
- ADBC alone does not make PostgreSQL COPY columnar; the destination writer is
  still required.

**Status:** viable candidate, not selected until the acceptance gate is met.

### Arrow Flight SQL

**Pros**

- Arrow record batches travel natively over a streaming protocol.
- Strong fit for databases and gateways that already expose Flight SQL.
- Can be a clean remote-worker boundary with explicit backpressure.

**Cons**

- Ordinary PostgreSQL does not expose Flight SQL; it requires a compatible
  service or gateway.
- Introduces endpoint deployment, mTLS/token handling, routing, and a new
  tenant-isolation surface.
- Supporting Flight SQL is not equivalent to supporting every existing SQL
  connection.

**Status:** appropriate for a backend that already offers Flight SQL, not a
general PostgreSQL migration solution.

### Arrow bridge service

**Pros**

- Can expose Arrow from databases whose native drivers do not.
- Separates native or vendor-specific libraries from the Brokoli binary.
- Could run near the source and reduce wide-area row transport.

**Cons**

- Adds a service to deploy, secure, upgrade, monitor, and bill.
- Moves source credentials and outbound policy enforcement to another trust
  boundary.
- A bridge that scans rows then serializes Arrow may still be slower than the
  current direct pgx-to-COPY path unless it is located or implemented
  advantageously.

**Status:** defer until a specific deployment needs a bridge; do not introduce
one only to make Arrow IPC appear in the architecture.

### Encode the existing row stream as Arrow IPC

**Pros**

- Reuses the current Arrow codec and requires little new infrastructure.

**Cons**

- Does not remove row-map scanning or row-map destination writes.
- Adds serialization and decoding work in the same process.
- The existing codec intentionally returns row maps to preserve its artifact
  contract, so it cannot feed a columnar writer.

**Status:** rejected.

## Follow-ups

- Prototype the narrowest pure-Go Arrow-native source available for a real
  target database, behind an explicit capability flag.
- Define a columnar batch interface only after that prototype proves it can
  meet the acceptance gate without weakening `common.DataSet` fallback
  semantics.
- Build an independent-server benchmark environment before comparing a bridge
  or Flight SQL against direct database streaming.

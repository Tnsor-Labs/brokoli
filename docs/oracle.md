# Oracle

Oracle connections use the pure-Go `go-ora` `database/sql` driver:

```text
oracle://user:password@host:1521/service
```

Set `host` to the Oracle listener, `port` to `1521` unless the listener uses a
different port, and `schema` to the service name. The password is resolved
through the normal secret-reference path and is never written to pipeline
configuration.

The `extra` object accepts Oracle connection options including `sid`,
`instance name`, `ssl`, `ssl verify`, `wallet`, `wallet password`,
`connect timeout`, `encryption`, and `data integrity`. Use `ssl` and
`ssl verify` together when the listener requires TCPS. Wallet paths and wallet
passwords must be supplied through the deployment's secret configuration.

Oracle is supported by database nodes for query, append, and overwrite
operations through the generic SQL writer. Upsert, pushdown, and streaming
database reads remain refused until they have Oracle-specific correctness
coverage.

Connection testing opens the same driver and URI used by pipeline execution.
Live Oracle verification is opt-in because the project does not keep a shared
Oracle account in CI.

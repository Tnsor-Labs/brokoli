# Databricks

Databricks connections use the pure-Go Thrift backend of the Databricks SQL
driver:

```text
databricks://token:dapi...@workspace.cloud.databricks.com:443/sql/1.0/warehouses/WAREHOUSE_ID
```

Set `host` to the workspace hostname, `port` to `443` unless the workspace
uses another HTTPS port, and `schema` to the warehouse HTTP path. Set `login`
to `token`; the connection password is the PAT and is resolved through the
normal secret-reference path.

The `extra` object accepts `catalog`, `schema`, `maxRows`, `timeout`,
`userAgentEntry`, `useCloudFetch`, `maxDownloadThreads`, and
`useArrowNativeDecimal`. The optional Databricks kernel backend is not enabled:
it requires CGO and a separate build tag, so this connector deliberately uses
the static pure-Go backend.

Databricks is supported by database nodes for query, append, and overwrite
operations through the generic SQL writer. Upsert, pushdown, and streaming
database reads remain refused until they have Databricks-specific correctness
coverage.

Connection testing opens the same driver and URI used by pipeline execution.
Live Databricks verification is opt-in because the project does not keep a
shared SQL warehouse in CI.

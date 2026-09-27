# Snowflake

Snowflake connections use the native `database/sql` driver:

```text
snowflake://user:password@account/database/schema?warehouse=COMPUTE_WH&role=SYSADMIN
```

Set the connection `host` to the Snowflake account or account hostname and
`schema` to `database/schema`. The connection password is resolved through the
normal secret-reference path and is never written to pipeline configuration.

Supported connection options in `extra` are `warehouse`, `role`,
`authenticator`, `loginTimeout`, and `application`. The driver supports the
standard Snowflake password and authenticator modes exposed by those options;
OAuth and external-browser credentials must be supplied through the driver's
supported secret configuration rather than embedded in a URI.

Snowflake is supported by database nodes for query, append, and overwrite
operations through the generic SQL writer. Upsert, pushdown, and streaming
database reads remain refused until they have Snowflake-specific correctness
coverage.

Connection testing executes `SELECT 1` through the same driver and URI path as
pipeline execution. Live Snowflake verification is opt-in because the project
does not keep a shared Snowflake account in CI.

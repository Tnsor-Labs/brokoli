# BigQuery

BigQuery connections use a secret-free URI:

```text
bigquery://project/dataset?location=EU&billing_project=billing-project
```

Set the connection `schema` to `project.dataset`. Put a service-account JSON
document in the encrypted `extra` value or behind `ExtraRef`. The document must
have `"type": "service_account"`; credentials are never placed in the URI or
pipeline configuration. Without a key, the worker uses its machine identity.
The ambient identity can be denied with `BROKOLI_SECRET_STORE_AMBIENT=deny`.

For workload identity federation, use an `extra` object with no secret
credentials:

```json
{
  "auth_method": "oidc",
  "provider": "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs",
  "service_account": "loader@acme-analytics.iam.gserviceaccount.com",
  "token_audience": "optional-custom-audience"
}
```

`provider` is required. `service_account` and `token_audience` are optional.
The worker's configured OIDC token source supplies the short-lived token; an
OIDC connection fails if the deployment has no token source.

Source queries are standard GoogleSQL query jobs. Every query has a default
`maximum_bytes_billed` of 10 GiB; set `maximum_bytes_billed` in `extra` to
override it. Column discovery uses a free dry run. Query and load jobs carry
Brokoli labels for attribution, and outbound requests use the worker's network
policy.

Sink writes are load jobs:

- `append` uses `WRITE_APPEND`.
- `overwrite` uses `WRITE_TRUNCATE_DATA` and preserves the existing table schema.
- `create_table` is supported through BigQuery's create-if-needed behavior.
- `upsert` is refused by name in this phase.

The local emulator is opt-in. Start it with `docker compose -f
docker-compose.test.yml up -d bigquery`, set
`BROKOLI_BIGQUERY_ENDPOINT=http://localhost:55538` and
`BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true`, then run the BigQuery integration test.
The emulator cannot prove `WRITE_TRUNCATE_DATA`, `maximum_bytes_billed`, or
atomic failure behavior; those require an opt-in live Google Cloud test.

Streaming reads, Storage Read/Write APIs, SQL pushdown, and BigQuery upsert are
not supported in this phase.

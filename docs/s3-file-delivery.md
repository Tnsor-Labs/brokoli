# Reading and writing files in customer-owned S3

`source_file` and `sink_file` can use a customer-owned S3 bucket through an
existing S3 connection. There is no separate S3 node: the file node still
chooses the format, while `conn_id` chooses where the object lives.

This is an OSS data-plane connector. It is not Strata, does not use managed
Brokoli storage, and does not add tenant or retention behavior to the object
store.

- [Connection configuration](#connection-configuration)
- [Python](#python)
- [TypeScript](#typescript)
- [Object keys](#object-keys)
- [Reads and writes](#reads-and-writes)
- [Network policy](#network-policy)
- [Security and lifecycle](#security-and-lifecycle)

## Connection configuration

Create a connection of type **S3**. The connection's encrypted extra settings
are a JSON object:

```json
{
  "bucket": "customer-data",
  "region": "us-east-1",
  "access_key": "AKIA...",
  "secret_key": "...",
  "max_download_bytes": 10737418240
}
```

| Key | Required | Meaning |
| --- | --- | --- |
| `bucket` | yes | The S3 bucket name. It must be a valid DNS-compatible bucket name. |
| `region` | yes | The AWS region used for request signing. |
| `access_key` | no | Access key for static credentials. Omit both credential keys to use the worker's AWS credential chain. |
| `secret_key` | no | Secret key paired with `access_key`. |
| `session_token` | no | Temporary-session token paired with `access_key` and `secret_key`, such as an AWS STS token. |
| `endpoint` | no | Custom S3-compatible endpoint, such as MinIO. Omit for AWS S3. |
| `use_path_style` | no | Use `endpoint/bucket/key` addressing. Usually `true` for S3-compatible services. |
| `max_download_bytes` | no | Maximum source download size in bytes. Defaults to 10 GiB. |

`access_key` and `secret_key` must be provided together. `session_token`, when
present, also requires both static credential fields. Credentials are resolved
per connection and are not written to logs. The connection test uses
the same authenticated AWS SDK client as file execution and checks the bucket
with `HeadBucket`.

For MinIO or another compatible service, the extra settings look like:

```json
{
  "bucket": "customer-data",
  "region": "us-east-1",
  "access_key": "minio-access",
  "secret_key": "minio-secret",
  "endpoint": "https://objects.example.com",
  "use_path_style": true
}
```

## Python

Pass the connection ID as `conn_id` to either file-node factory:

```python
from brokoli import Pipeline, sink_file, source_file

with Pipeline("S3 exchange") as pipeline:
    orders = source_file(
        "Read orders",
        path="incoming/orders.csv",
        format="csv",
        conn_id="customer-s3",
    )
    sink_file(
        "Write archive",
        input=orders,
        path="processed/orders.csv",
        format="csv",
        conn_id="customer-s3",
    )
```

## TypeScript

The TypeScript option is `connId`:

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("S3 exchange");
const orders = pipeline.sourceFile("Read orders", {
  path: "incoming/orders.csv",
  format: "csv",
  connId: "customer-s3",
});
pipeline.sinkFile("Write archive", orders, {
  path: "processed/orders.csv",
  format: "csv",
  connId: "customer-s3",
});
```

The generated IR contains `conn_id` on the file node. The connection is
resolved in the worker's workspace at execution time.

## Object keys

For S3, `path` is a literal object key within the configured bucket:

```json
{
  "type": "sink_file",
  "config": {
    "conn_id": "customer-s3",
    "path": "processed/orders.csv",
    "format": "csv"
  }
}
```

There is no implicit prefix, directory creation, wildcard lookup, delete, or
archive operation. S3 prefixes are represented directly in the key. Path
interpolation supported by the file-node IR can still be used to construct a
key before execution.

## Reads and writes

- `source_file` checks the object size, downloads it to a private worker data
  directory, and then invokes the normal CSV, JSON, XML, Excel, or other file
  loader. The staged copy is removed when the node finishes.
- `sink_file` streams the encoded output through the AWS multipart uploader.
  It does not materialize the complete output in memory.
- A source never deletes or changes the S3 object.
- A sink writes the requested key. Existing objects may be replaced according
  to the S3 service's normal object-write semantics.
- Preview sink nodes do not connect or write. Source previews still read the
  configured object.

## Network policy

S3 requests use the same outbound policy as other server-initiated requests.
Public AWS S3 endpoints need no additional setting. Private endpoints require
an explicit operator policy, preferably a narrow CIDR allowlist:

```sh
BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16
```

Loopback is blocked by default. Local MinIO integration tests explicitly set
`BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true`; this should not be enabled for a normal
multi-tenant deployment.

## Security and lifecycle

- Bucket names are validated before the AWS client is created, preventing
  malformed bucket values from becoming request targets.
- Source downloads are capped by `max_download_bytes` and staged in a private
  directory.
- No tenant fields, managed-bucket assumptions, retention policy, or object
  lifecycle behavior are part of this OSS connector.
- Managed Strata storage and tenant-isolated artifact lifecycle remain
  Enterprise responsibilities.

## Provider compatibility checks

The engine includes a provider-neutral integration smoke test. It runs only
when configured with a pre-created test bucket, so it can safely target hosted
providers as well as local MinIO:

```sh
BROKOLI_TEST_S3_PROVIDER=minio \
BROKOLI_TEST_S3_ENDPOINT=http://127.0.0.1:9000 \
BROKOLI_TEST_S3_BUCKET=brokoli-compat \
BROKOLI_TEST_S3_ACCESS_KEY=brokoli-test \
BROKOLI_TEST_S3_SECRET_KEY=brokoli-test-secret \
BROKOLI_TEST_S3_PATH_STYLE=true \
BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true \
go test ./engine -run TestS3FileProviderCompatibility -v
```

Set `BROKOLI_TEST_S3_PROVIDER` to `aws`, `r2`, `wasabi`, `b2`, `ceph`, or
another provider label. `BROKOLI_TEST_S3_REGION` and
`BROKOLI_TEST_S3_SESSION_TOKEN` are available for provider-specific regions
and temporary credentials. The check performs `HeadBucket`, upload,
`HeadObject`, and download through the same client used by file nodes. The
MinIO integration test separately exercises a large multipart upload.

# Reading and writing files in customer-owned Google Cloud Storage

`source_file` and `sink_file` can read and write objects in a customer-owned
Google Cloud Storage bucket through a `gcs` connection. There is no separate
GCS node: the file node still chooses the format, while `conn_id` chooses
where the object lives.

This is one of four remote locations a file node can use, beside
[SFTP](sftp-file-delivery.md), [customer-owned S3](s3-file-delivery.md) and
[Azure Blob Storage](azure-blob-file-delivery.md). All four sit behind the
same transport, so dry runs, data directories, lineage, and the rule that a
file node with a `conn_id` never falls back to the worker's local disk behave
the same way for each.

- [Connection configuration](#connection-configuration)
- [Authentication](#authentication)
- [Permissions](#permissions)
- [Python](#python)
- [TypeScript](#typescript)
- [Object names](#object-names)
- [Reads and writes](#reads-and-writes)
- [Testing the connection](#testing-the-connection)
- [Network policy](#network-policy)
- [Security and lifecycle](#security-and-lifecycle)
- [Testing against fake-gcs-server](#testing-against-fake-gcs-server)

## Connection configuration

Create a connection of type **Google Cloud Storage**. Its extra settings are
a JSON object, encrypted at rest and never returned by the API once saved:

```json
{
  "bucket": "acme-exports",
  "credentials": "{\"type\":\"service_account\",\"project_id\":\"acme\",...}",
  "max_download_bytes": 10737418240
}
```

| Key | Required | Meaning |
| --- | --- | --- |
| `bucket` | yes | The bucket name: 3-63 lowercase letters, digits, `-`, `_` and `.`, starting and ending with a letter or digit, with no `..`. It must already exist. |
| `credentials` | no | A service-account JSON key, as a string. See [Authentication](#authentication). |
| `auth_method` | no | `oidc` for workload identity federation. Any other value is refused by name. |
| `provider` | with `oidc` | The workload identity pool provider's full resource name. |
| `service_account` | no | With `oidc`, the service account to impersonate after the token exchange. |
| `token_audience` | no | With `oidc`, the audience of the token Brokoli signs, when the provider expects one other than its own resource name. |
| `max_download_bytes` | no | Maximum size of an object `source_file` will download. Defaults to 10 GiB. |

`endpoint` is **not** a setting and is refused by name. Whoever controls the
API endpoint receives the connection's access token, so it is never taken
from connection data. The emulator tests set it in code.

## Authentication

A connection authenticates in exactly one of three ways, the same three
[BigQuery](bigquery.md) uses, through the same code:

- **Workload identity federation** (`"auth_method": "oidc"`). No secret is
  stored. The worker's configured OIDC token source signs a short-lived token
  naming the connection by its immutable ID, and Google exchanges it for an
  access token:

  ```json
  {
    "bucket": "acme-exports",
    "auth_method": "oidc",
    "provider": "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs",
    "service_account": "exports@acme.iam.gserviceaccount.com"
  }
  ```

  The connection fails if the deployment has no token source. Renaming the
  connection does not change what the customer's trust configuration
  matches. See [workload identity](workload-identity.md) for setting up the
  token source and the provider.

- **A service-account key** (`credentials`). The key must have
  `"type": "service_account"`. Any other kind of Google credential file --
  in particular an `external_account` file, which can make Google's library
  fetch an arbitrary URL or run a command -- is refused, both when the
  connection is saved and when a node runs.

- **The machine's own identity**, when the connection has neither. The
  worker uses Application Default Credentials (a GKE workload identity, a
  VM's service account, `GOOGLE_APPLICATION_CREDENTIALS`). A deployment that
  runs pipelines for several workspaces must deny this, since every
  workspace would share the machine's access:

  ```sh
  BROKOLI_SECRET_STORE_AMBIENT=deny
  ```

  With it set, a connection with no key and no `auth_method` fails with a
  message saying the ambient identity is denied. A token source that reads
  tokens from disk (`BROKOLI_OIDC_TOKEN_FILES`) is the machine's identity
  too, and is refused the same way.

Every identity is requested with the
`https://www.googleapis.com/auth/devstorage.read_write` scope and nothing
wider.

## Permissions

Grant the identity a role on the bucket, not the project:

| Pipeline does | Role | Why |
| --- | --- | --- |
| reads (`source_file`) | `roles/storage.objectViewer` | `storage.objects.get`, plus `storage.objects.list` for the connection test |
| writes (`sink_file`) | `roles/storage.objectUser` | `storage.objects.create`; `storage.objects.delete` to replace an existing object; list for the connection test |

`roles/storage.objectCreator` alone is not enough: it has no list, so the
connection test fails, and no delete, so a write over an existing object is
refused. A missing permission fails the node with a message that says the
credentials do not grant the operation.

## Python

Pass the connection ID as `conn_id` to either file-node factory:

```python
from brokoli import Pipeline, sink_file, source_file

with Pipeline("GCS exchange") as pipeline:
    orders = source_file(
        "Read orders",
        path="incoming/orders.csv",
        format="csv",
        conn_id="customer-gcs",
    )
    sink_file(
        "Write archive",
        input=orders,
        path="processed/orders.csv",
        format="csv",
        conn_id="customer-gcs",
    )
```

## TypeScript

The TypeScript option is `connId`:

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("GCS exchange");
const orders = pipeline.sourceFile("Read orders", {
  path: "incoming/orders.csv",
  format: "csv",
  connId: "customer-gcs",
});
pipeline.sinkFile("Write archive", orders, {
  path: "processed/orders.csv",
  format: "csv",
  connId: "customer-gcs",
});
```

The generated IR contains `conn_id` on the file node, and the connection is
resolved in the worker's workspace when the run executes.

## Object names

`path` is the object name within the configured bucket, literally:

```json
{
  "type": "sink_file",
  "config": {
    "conn_id": "customer-gcs",
    "path": "processed/orders.csv",
    "format": "csv"
  }
}
```

Folders are just part of the name. There is no implicit prefix, folder
creation, wildcard lookup, delete, or archive operation. Path interpolation
supported by the file-node IR still applies before execution. In run logs
and lineage the object appears as `gs://<conn_id>/<path>`.

## Reads and writes

- **`source_file`** reads the object's metadata first and refuses an object
  over `max_download_bytes`, then downloads that exact generation to a
  private directory inside the worker's data directories and hands it to the
  normal loader chosen by the file extension. Pinning the generation means an
  object replaced between the two requests cannot slip past the size check;
  the limit is also enforced on the bytes actually read. The staged copy is
  removed when the node finishes. A source never changes or deletes an
  object.
- **`sink_file`** streams its output into the object. It holds at most one
  16 MiB chunk in memory, on the batch path or the streaming one. Outputs
  smaller than a chunk go up in a single request; larger ones use a
  resumable upload.
- **A failed write leaves nothing behind.** Cloud Storage creates or
  replaces an object only when its upload completes. If the node's output
  fails part way, the upload is abandoned: no new object appears, and an
  existing object at that name is left exactly as it was. The run fails with
  the write's own error. This holds for both the single-request and the
  resumable path, for a new object and over an existing one; all four are
  tested.
- **A successful write replaces** an existing object at that name,
  atomically: a reader sees the old content or the new, never a mixture.
- **Dry runs do not connect or write.** A dry run logs the destination it
  would have written, as `gs://<conn_id>/<path>`.

## Testing the connection

The connection test is a real one. It authenticates the way the connection
says and lists at most one object in the bucket, through the same client
file nodes use when a run executes. An `oidc` connection is tested with a
token for the connection's own workspace and ID. It fails when:

| Situation | Message includes |
| --- | --- |
| The bucket does not exist | "not found (404)" |
| The key is revoked, or the token exchange is refused | "credentials were refused (401)", or the token exchange's own error |
| The identity lacks `storage.objects.list` | "do not grant this operation (403)" |
| No key, no `auth_method`, and ambient identity denied | "ambient" |
| An `oidc` connection on a deployment without a token source | the token source error |
| The request is refused by the outbound policy | `blocked` |

## Network policy

Every request goes through the deployment's outbound policy, as SFTP, S3 and
Azure Blob do: the Cloud Storage API, and for `oidc` the token exchange
(`sts.googleapis.com`) and impersonation (`iamcredentials.googleapis.com`)
calls. Google's public endpoints need nothing extra. Loopback and private
ranges are refused by default; do not enable loopback on a multi-tenant
deployment to reach an emulator, the tests below opt into it themselves.

## Security and lifecycle

- The bucket name is validated before any client is created, so a
  malformed value never becomes a request target.
- The API endpoint and the federation endpoints cannot be set from
  connection data, so a connection cannot send its token anywhere but
  Google.
- Keys live only in the connection's encrypted extra settings; with `oidc`
  there is no stored secret at all.
- Downloads are capped by `max_download_bytes` and staged privately.
- Nothing here manages the bucket: no creation, retention, object holds,
  versioning, or lifecycle rules. Configure those on the bucket. A retention
  policy or object hold makes a write over an existing object fail, with the
  service's reason.

## Testing against fake-gcs-server

The transport is tested against
[fake-gcs-server](https://github.com/fsouza/fake-gcs-server), which the test
compose file runs:

```sh
docker compose -f docker-compose.test.yml up -d gcs
BROKOLI_TEST_GCS_ENDPOINT=http://127.0.0.1:55543 \
  go test ./engine -run '^TestGCS' -v
```

CI runs the same tests in the `Test (GCS / fake-gcs-server)` job. Each test
creates its own uniquely named bucket, so an interrupted run leaves nothing
that breaks the next one. The tests authenticate with `auth_method: oidc`
through a local stand-in for Google's token exchange, so they prove the
access token reaches every storage request and that the token names the
connection by its ID.

The tests cover a round trip; a failed write on both upload paths, for a
new object and over an existing one; a missing object; the download limit;
the connection test with a present and a missing bucket; the outbound policy
refusing loopback by default; and a pipeline running `source_file` into
`sink_file` through the connection on both the batch and the streaming sink.

The emulator does not check permissions or signatures, so it cannot prove
IAM behaviour, retention policies or the real token exchange; those need a
real bucket and a real provider, and are not part of the automated suite.
Before relying on a new deployment, run a `source_file` into `sink_file`
pipeline against a scratch bucket with the identity the connection will use,
and check both **Test connection** and a write over an existing object.

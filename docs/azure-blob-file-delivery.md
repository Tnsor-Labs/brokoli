# Reading and writing files in customer-owned Azure Blob Storage

`source_file` and `sink_file` can read and write blobs in a customer-owned
Azure Blob Storage container through an `azure_blob` connection. There is no
separate Azure node: the file node still chooses the format, while `conn_id`
chooses where the blob lives.

This is the third remote location a file node can use, beside
[SFTP](sftp-file-delivery.md) and [customer-owned S3](s3-file-delivery.md).
All three sit behind the same transport, so dry runs, data directories,
lineage, and the rule that a file node with a `conn_id` never falls back to
the worker's local disk behave the same way for each.

- [Connection configuration](#connection-configuration)
- [Authentication](#authentication)
- [Python](#python)
- [TypeScript](#typescript)
- [Blob names](#blob-names)
- [Reads and writes](#reads-and-writes)
- [Testing the connection](#testing-the-connection)
- [Network policy](#network-policy)
- [Security and lifecycle](#security-and-lifecycle)
- [Testing against Azurite](#testing-against-azurite)

## Connection configuration

Create a connection of type **Azure Blob Storage**. Its extra settings are a
JSON object, encrypted at rest and never returned by the API once saved:

```json
{
  "account": "acmestorage",
  "container": "exports",
  "key": "...",
  "max_download_bytes": 10737418240
}
```

| Key | Required | Meaning |
| --- | --- | --- |
| `account` | yes | The storage account name: 3-24 lowercase letters and digits. |
| `container` | yes | The container: 3-63 lowercase letters, digits and single hyphens, starting and ending with a letter or digit. It must already exist. |
| `key` | one of `key` / `sas_token` | The storage account access key. |
| `sas_token` | one of `key` / `sas_token` | A shared access signature. A leading `?` is accepted and dropped. |
| `endpoint` | no | The Blob service URL. Omit for the public cloud, where it is `https://<account>.blob.core.windows.net/`. Set it for a sovereign cloud, a private endpoint, or the Azurite emulator. It must not carry a query string: a SAS token goes in `sas_token`, never in the URL, so it cannot reach a log line. |
| `max_download_bytes` | no | Maximum size of a blob `source_file` will download. Defaults to 10 GiB. |

These are the field names the connection catalogue documented before any
node could use this connection type, so a connection saved against that
example works unchanged.

## Authentication

Exactly one of:

- **An account key** (`key`). Full access to the account. Simple, and the
  right choice for a dedicated account; prefer a SAS for anything shared.
- **A SAS token** (`sas_token`). Scope it to the container and to the
  permissions the pipeline needs:

  | Pipeline does | SAS permissions |
  | --- | --- |
  | reads (`source_file`) | read, list |
  | writes (`sink_file`) | create, write, list |
  | both | read, create, write, list |

  List is needed by the connection test. A token without write permission
  lets a pipeline read and makes a `sink_file` fail with a message that says
  the credentials do not grant the operation.

**Service principals (Entra ID) are not supported yet** and are refused by
name: a connection carrying `tenant_id`, `client_id` or `client_secret` fails
validation with a message saying so, rather than being half-supported. It
needs an Entra ID tenant to test against, and the emulator used for every
other test here cannot stand in for one.

## Python

Pass the connection ID as `conn_id` to either file-node factory:

```python
from brokoli import Pipeline, sink_file, source_file

with Pipeline("Azure exchange") as pipeline:
    orders = source_file(
        "Read orders",
        path="incoming/orders.csv",
        format="csv",
        conn_id="customer-azure",
    )
    sink_file(
        "Write archive",
        input=orders,
        path="processed/orders.csv",
        format="csv",
        conn_id="customer-azure",
    )
```

## TypeScript

The TypeScript option is `connId`:

```typescript
import { Pipeline } from "brokoli";

const pipeline = new Pipeline("Azure exchange");
const orders = pipeline.sourceFile("Read orders", {
  path: "incoming/orders.csv",
  format: "csv",
  connId: "customer-azure",
});
pipeline.sinkFile("Write archive", orders, {
  path: "processed/orders.csv",
  format: "csv",
  connId: "customer-azure",
});
```

The generated IR contains `conn_id` on the file node, and the connection is
resolved in the worker's workspace when the run executes.

## Blob names

`path` is the blob name within the configured container, literally:

```json
{
  "type": "sink_file",
  "config": {
    "conn_id": "customer-azure",
    "path": "processed/orders.csv",
    "format": "csv"
  }
}
```

Virtual directories are just part of the name. There is no implicit prefix,
directory creation, wildcard lookup, delete, or archive operation. Path
interpolation supported by the file-node IR still applies before execution.

## Reads and writes

- **`source_file`** reads the blob's size first and refuses one over
  `max_download_bytes`, then downloads it to a private directory inside the
  worker's data directories and hands it to the normal loader chosen by the
  file extension. The limit is enforced again on the bytes actually read, in
  case the blob was replaced between the two requests. The staged copy is
  removed when the node finishes. A source never changes or deletes a blob.
- **`sink_file`** streams its output as a block blob. It never holds the
  whole output in memory, on the batch path or the streaming one.
- **A failed write leaves nothing behind.** Blocks are staged and committed
  only after the node's output has been written to the end. If the write
  fails part way, nothing is committed: no new blob appears, and an existing
  blob at that name is left exactly as it was. The run fails with the
  write's own error. Blocks staged before the failure stay uncommitted and
  are discarded by the service. This holds for both the single-request path
  the SDK uses for small outputs and the staged-block path for larger ones;
  both are tested.
- **A successful write replaces** an existing blob at that name, atomically:
  a reader sees the old content or the new, never a mixture.
- **Dry runs do not connect or write.** A dry run logs the destination it
  would have written, as `azblob://<conn_id>/<path>`.

## Testing the connection

The connection test is a real one. It authenticates with the configured
credential and lists the container, through the same client file nodes use
when a run executes. It fails, with the storage error code first, when:

| Situation | Message includes |
| --- | --- |
| The container does not exist | `ContainerNotFound` |
| The key or SAS token is wrong or expired | `AuthenticationFailed` from Azure; the Azurite emulator reports the same thing as `AuthorizationFailure` |
| The SAS token lacks list permission | "do not grant this operation" |
| The endpoint is refused by the outbound policy | `blocked` |

## Network policy

Every request goes through the deployment's outbound policy, as SFTP and S3
do. Public Azure endpoints need nothing extra. A private endpoint needs an
explicit, preferably narrow, allowlist:

```sh
BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16
```

Loopback is refused by default. Do not enable it on a multi-tenant
deployment to reach an emulator; the tests below opt into it themselves.

## Security and lifecycle

- The account name, container name and endpoint are validated before any
  client is created, so a malformed value never becomes a request target.
- Credentials live only in the connection's encrypted extra settings. The
  endpoint may not carry a query string, so a SAS token cannot be pasted into
  a value that is shown or logged.
- Downloads are capped by `max_download_bytes` and staged privately.
- Nothing here manages the container: no creation, retention, soft delete,
  or lifecycle rules. Configure those on the storage account.

## Testing against Azurite

The transport is tested against Azurite, Microsoft's own emulator, which the
test compose file runs:

```sh
docker compose -f docker-compose.test.yml up -d --wait azurite
BROKOLI_TEST_AZURE_BLOB_ENDPOINT=http://127.0.0.1:55537/devstoreaccount1 \
  go test ./engine -run 'TestAzureBlob' -v
BROKOLI_TEST_AZURE_BLOB_ENDPOINT=http://127.0.0.1:55537/devstoreaccount1 \
  go test ./api -run 'TestAzureBlobConnectionTest' -v
```

`preflight.sh` sets that endpoint whenever it starts the compose services,
and CI runs the same tests in the `Test (Azure Blob / Azurite)` job. Each
test creates its own uniquely named container and deletes it, so an
interrupted run leaves nothing that breaks the next one.

The tests cover a round trip; a failed write on both upload paths, for a
new blob and over an existing one; a missing blob; the download limit; a
read-write and a read-only SAS; the outbound policy refusing loopback by
default; the connection test with the right key, a wrong key and a missing
container; and a pipeline running `source_file` into `sink_file` through the
connection on both the batch and the streaming sink.

To use a real Azure account, create a container and point a connection at
it with an account key or a container-scoped SAS; the same tests run
against it by setting the endpoint to `https://<account>.blob.core.windows.net`
and adjusting the account and key constants in the test fixture.

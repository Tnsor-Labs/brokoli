# Secret references

A connection's password and extra settings can be stored in Brokoli, or
kept in a secret store and referenced. A reference is a URI in the
connection's `password_ref` or `extra_ref` field; the credential is read
from the store each time a run needs it, and never written to Brokoli's
database.

| Scheme | Reads from | Allowed by default |
| --- | --- | --- |
| `encrypted://...` | Brokoli's own database, encrypted with `BROKOLI_ENCRYPTION_KEY` | yes: this is what a password typed into Brokoli becomes |
| `env://NAME` | an environment variable of the Brokoli process | no: `BROKOLI_SECRET_ENV_ALLOW` |
| `vault://path#key` | HashiCorp Vault (KV version 2) | no: `BROKOLI_SECRET_VAULT_ALLOW` |
| `k8s://[namespace/]secret/key` | a Kubernetes Secret | no: `BROKOLI_SECRET_K8S_ALLOW` |

## Why the external schemes are denied by default

Brokoli reads each store with its own credentials: its process
environment, its Vault token, its Kubernetes service account. Those
reach far more than any one connection should, usually including
Brokoli's own database URL, signing secret and encryption key. A
connection is something a workspace editor can create and point at a
server of their choosing, so a reference that could name anything would
let an editor send any secret Brokoli can read to a server they control,
simply by testing or running the connection.

So each external scheme resolves only what the operator has listed. A
reference to anything else fails with a message naming the setting, and
the store is never contacted.

## `env://NAME`

```sh
BROKOLI_SECRET_ENV_ALLOW=WAREHOUSE_PASSWORD,PARTNER_API_TOKEN
```

Comma-separated exact variable names. These are never readable, whatever
the list says, because a slip there is unrecoverable:
`BROKOLI_ENCRYPTION_KEY`, `BROKOLI_JWT_SECRET`, `BROKOLI_DB_URL`,
`BROKOLI_LICENSE_SIGNING_SECRET`.

This list is separate from the one that governs `${env.*}` in pipeline
configuration: a variable holding a database password has no business
being interpolated into a node's config, and the reverse.

## `vault://path#key`

```sh
VAULT_ADDR=https://vault.internal:8200
VAULT_TOKEN=...                      # or VAULT_ROLE for Kubernetes auth
BROKOLI_SECRET_VAULT_ALLOW=secret/data/brokoli,kv/data/partners
```

A reference names a KV version 2 path and a key in it:
`vault://secret/data/brokoli/warehouse#password` reads the `password`
key of `secret/data/brokoli/warehouse`.

`BROKOLI_SECRET_VAULT_ALLOW` lists path prefixes, comma-separated,
matched on whole path segments: `secret/data/brokoli` allows
`secret/data/brokoli/warehouse` but not
`secret/data/brokoli-old/warehouse`. Paths containing `..`, `%`, `?` or
a backslash are refused outright, so an encoded `..` cannot walk out of
an allowed prefix after the check.

Give Brokoli's Vault token access only to what pipelines need; the
allowlist is a second line, not a replacement for the token's policy.

## `k8s://[namespace/]secret/key`

```sh
BROKOLI_K8S_NAMESPACE=brokoli        # the default namespace; "default" when unset
BROKOLI_SECRET_K8S_ALLOW=brokoli/warehouse-creds,brokoli/partner-sftp
```

`k8s://warehouse-creds/password` reads the `password` key of the
`warehouse-creds` Secret in `BROKOLI_K8S_NAMESPACE`; the three-part form
names the namespace. Only that one namespace is reachable at all.
`BROKOLI_SECRET_K8S_ALLOW` then lists the Secrets references may read,
as exact `namespace/secret` names.

The resolver runs `kubectl`, which must be on the Brokoli process's
`PATH`, with the service account's permissions. Never list the Secret
holding Brokoli's own configuration.

## Where references are resolved

Runs resolve references on the machine that runs the node, so the
allowlists must be set there too (workers included). The connection
test in the UI uses only credentials stored in Brokoli: a connection
whose credentials are references tests as missing them, while runs
using it work.

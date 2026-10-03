# Secret stores

A secret store is a workspace's connection to its own secret manager:
HashiCorp Vault, AWS Secrets Manager, and others. A connection's credential
can then be a reference into it, `secret://<store>/<path>#<field>`. The value
is fetched from the secret manager on the machine that runs the node, when the
node runs, and is never written to Brokoli's database, a log or a run record.

Use a secret store when your security policy says credentials live in your
secret manager and nowhere else. With the `ambient` or `oidc` identity
methods, Brokoli stores the location of a secret and nothing that opens it.

This is different from the operator-level references (`env://`, `vault://`,
`k8s://`) described in [Secret references](secret-references.md). Those are
read with the *server's* own credentials, behind an allowlist the operator
keeps. A secret store belongs to a workspace, authenticates as itself, and
needs no operator allowlist.

- [Creating a store](#creating-a-store)
- [Identity: how a store authenticates](#identity-how-a-store-authenticates)
- [References](#references)
- [Where a reference may appear](#where-a-reference-may-appear)
- [Testing](#testing)
- [When a reference cannot be resolved](#when-a-reference-cannot-be-resolved)
- [What Brokoli keeps and what it never keeps](#what-brokoli-keeps-and-what-it-never-keeps)
- [Workspaces](#workspaces)
- [API](#api)
- [Providers](#providers)

## Creating a store

A store has:

| Field | Meaning |
| --- | --- |
| `name` | Unique in the workspace; what references use. Lowercase letters, digits and `-`, at most 63 characters. |
| `provider` | Which secret manager. `GET /api/secret-stores/providers` lists the ones this server has. |
| `settings` | The provider's non-secret settings: an address, a region, a mount. |
| `auth_method` | `ambient`, `oidc` or `token` (next section). |
| `auth_settings` | That method's non-secret settings: a role, a role ARN. |
| `credential` | Only for `auth_method: token`: the static token. Encrypted at rest; never returned. |

Creating, editing, deleting and testing a store take the same permission as
doing that to a connection. A store is part of connection configuration.

A store that a connection refers to cannot be deleted or renamed. The refusal
names the connections, so you can change them first.

## Identity: how a store authenticates

| Method | What the store holds | Who can use it | Stored credential |
| --- | --- | --- | --- |
| `ambient` | Nothing, or a role to assume | The machine that runs the node, with its own environment identity: an AWS instance role or IRSA, a Kubernetes service account | none |
| `oidc` | An audience and a role | Any machine with an OIDC token source | none |
| `token` | A static token | Anyone who can run the node | yes, encrypted |

**`ambient`** is the simplest, and the right choice on a single-team server
or on a worker that runs inside your own network with its own identity there.
On a server that runs pipelines for several teams, the machine's identity
belongs to the operator. Such a deployment sets:

```sh
BROKOLI_SECRET_STORE_AMBIENT=deny
```

and a store that asks for `ambient` there fails with a message saying the
method is disabled on this deployment. It never reaches the secret manager
with the operator's identity.

**`oidc`** exchanges a short-lived token for the provider's own session. Your
secret manager is configured to trust the issuer of that token: Vault's JWT
auth method, AWS `AssumeRoleWithWebIdentity`, and so on. The token Brokoli
requests names:

- the workspace;
- the store, by its immutable `id` (never its name, so renaming a store does
  not change what your trust policy matches);
- the run and node the value is for.

A token source that issues one token per request puts these in the token's
claims, so you can bind trust to a workspace and see each run in your own
audit log. The server's token source is configured once
(`BROKOLI_OIDC_TOKEN_FILES` for tokens a platform writes to disk; see
[Workload identity](workload-identity.md)). A token read from disk is the
machine's identity, so it falls under `BROKOLI_SECRET_STORE_AMBIENT` too.

**`token`** stores a static token, a Vault token or a client secret,
encrypted like a connection password. It is the least safe method: a dump of
the database plus the encryption key opens your secret manager. Use it only
where the provider or your setup offers nothing else.

## References

```
secret://<store>/<path>[?version=<v>][#<field>]
```

- `<store>` is a store name in the connection's workspace.
- `<path>` is the secret's name as your secret manager spells it, unchanged,
  so you can copy it from its console. A leading `/` (an SSM parameter) is
  kept: `secret://ssm//prod/warehouse/password`.
- `#<field>` selects one field of a secret that is a map of named fields
  (Vault KV, a JSON AWS secret). It is **required** for a map-shaped secret
  and **refused** for a single value, so a reference never silently yields a
  whole document where a password was expected.
- `?version=<v>` pins a version, in the provider's own syntax. The default is
  the current version.

Examples:

```
secret://vault-prod/warehouse/loader#password
secret://aws/prod/warehouse#password
secret://ssm//prod/warehouse/password
secret://aws/prod/warehouse?version=AWSPREVIOUS#password
```

## Where a reference may appear

Anywhere a connection holds a credential:

- **The password.** Set `password_ref` to the reference.
- **The whole extra document.** Set `extra_ref` to a reference whose value is
  the JSON document.
- **Any single string inside extra.** This is the usual one. A connection's
  extra settings mix credentials with plain settings, and each credential can
  be its own reference while the rest stay as they are:

  ```json
  {
    "bucket": "exports",
    "region": "eu-west-1",
    "access_key": "AKIA...",
    "secret_key": "secret://aws/prod/s3-loader#secret_key"
  }
  ```

  The same works for SFTP's `private_key`, Azure Blob's `key`, or a header
  value in an HTTP connection's `headers`.

A value is a reference only when it starts with `secret://`.

**When a connection is saved,** each reference is checked without fetching
anything:

- the syntax;
- that the store exists in the connection's workspace;
- that `#field` is present for a provider whose secrets are maps, and absent
  for one whose secrets are single values.

A failed check is a 400 naming the field: `extra.secret_key: secret://nope/x:
no secret store named "nope" in this workspace`.

## Testing

**Test a store** with `POST /api/secret-stores/{id}/test` and a path to read.
The test authenticates and reads that path. It reports:

- success;
- the version it saw;
- for a map-shaped secret, the field names.

Never the value.

**Test a connection** that uses references. The test resolves them through
the same path a run uses, then tests the connection.

**Where the test runs matters.** A store using `ambient`, or a token read from
disk, is tested with the *server's* identity. A worker inside your network
resolves with its own identity, which may differ. A green test on the server
is not proof that a worker can read the secret, and the test result says so.

## When a reference cannot be resolved

The node fails before it runs, and the error names the connection, the field,
the reference and the reason, never the value:

```
connection "warehouse": password: could not resolve secret://vault-prod/warehouse/loader#password:
secret store "vault-prod" (vault): permission denied
```

Other reasons you may see:

| Message | Meaning |
| --- | --- |
| `no secret store named "x" in this workspace` | The store was not found in the run's workspace. A store in another workspace is never used. |
| `name one with #<field>` / `remove #x` | The `#field` does not suit the secret's shape. |
| `the secret has no field "x"` | The secret exists, but not that field; the error lists the ones it has. |
| `secret not found` | The path does not exist in the secret manager. |
| `ambient identity is disabled on this deployment` | The store uses `ambient`, and the operator denies it. |
| `this deployment has no OIDC token source` | The store uses `oidc`, and the server has no token source. |

## What Brokoli keeps and what it never keeps

**Kept:** the store's name, provider, settings, auth method and auth settings;
for `token`, the encrypted token.

**Never kept:** a value fetched from a secret manager. It is not written to:

- the database;
- a work order;
- a log or a run record;
- a preview or an artifact;
- the pipeline IR.

During a run, values are cached in memory for that run only, so forty nodes
on one connection make one request. The cache is dropped when the run ends.
Every value a run fetches is masked as `[redacted]` in its node logs, errors,
events and recorded SQL. Values shorter than 8 bytes are not masked, because
masking every occurrence of a short string makes a log unreadable.

## Workspaces

A store belongs to one workspace. A reference resolves only against the
stores of the run's own workspace, never another's. Two workspaces can each
have a store called `prod` that points at different secret managers, and
neither can reach the other's.

## API

| Method and path | Does |
| --- | --- |
| `GET /api/secret-stores/providers` | The providers this server has, with each one's secret shape and auth methods |
| `GET /api/secret-stores` | The workspace's stores |
| `POST /api/secret-stores` | Create a store |
| `GET /api/secret-stores/{id}` | One store |
| `PUT /api/secret-stores/{id}` | Update a store; an omitted `credential` is kept |
| `DELETE /api/secret-stores/{id}` | Delete a store no connection refers to |
| `POST /api/secret-stores/{id}/test` | Read one path: `{"path": "...", "version": "", "field": ""}` |

A store as returned never contains its credential. `has_credential: true`
says one is stored.

**Updates and the stored token:**

- An update that omits `credential` keeps the stored token.
- An update that moves the store to another provider or address drops it:
  a token entered for one secret manager never follows the store to
  another, and the update fails until a new token is given.

```sh
curl -X POST https://brokoli.example.com/api/secret-stores \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "vault-prod", "provider": "vault",
       "settings": {"address": "https://vault.internal:8200", "mount": "secret"},
       "auth_method": "oidc", "auth_settings": {"role": "brokoli-loader"}}'
```

## Providers

`GET /api/secret-stores/providers` lists the providers this server offers. A
provider knows one secret manager's API: its settings, how it logs in with
each identity method, and how it reads. Each provider's settings are documented
on its own page as it is added. A distribution can add providers of its own.

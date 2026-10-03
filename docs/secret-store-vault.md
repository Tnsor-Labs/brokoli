# Secret store provider: HashiCorp Vault and OpenBao

The `vault` provider reads secrets from a **KV version 2** secrets engine in
HashiCorp Vault or OpenBao. A connection then refers to one field of one
secret:

```
secret://vault-prod/warehouse/loader#password
```

which reads the field `password` of the secret `warehouse/loader` in the
store's KV mount, on the machine that runs the node, when the node runs.

This page covers what is specific to Vault. What a secret store is, where a
reference may appear, and what Brokoli keeps and never keeps are in
[Secret stores](secret-stores.md).

- [Requirements](#requirements)
- [Settings](#settings)
- [References](#references)
- [Authentication](#authentication)
  - [token](#token-a-static-vault-token)
  - [oidc](#oidc-vault-jwt-auth)
  - [ambient](#ambient-vault-kubernetes-auth)
- [A policy for the store](#a-policy-for-the-store)
- [Complete examples](#complete-examples)
- [Testing a store](#testing-a-store)
- [Network](#network)
- [Error messages](#error-messages)
- [How it talks to Vault](#how-it-talks-to-vault)

## Requirements

- Vault or OpenBao with a KV **version 2** mount. KV version 1 is not
  supported: its read path and response differ, and it has no versions.
  `vault secrets enable -version=2 -path=secret kv` creates one; a Vault dev
  server has one at `secret/` already.
- An auth method for Brokoli to log in with (next sections), and a policy
  that grants `read` on the secrets the store will be used for.
- The machine that runs the node must be able to reach Vault's address
  through the deployment's outbound policy ([Network](#network)).

Every command on this page is shown with the `vault` CLI. With OpenBao, run
the same command with `bao` in place of `vault`; the arguments are the same.

## Settings

A store's `settings`:

| Setting | Required | Default | Meaning |
| --- | --- | --- | --- |
| `address` | yes | | The server, as `https://host[:port][/path]`, for example `https://vault.example.com:8200`. Plain `http://` is accepted only for a loopback address (`localhost`, `127.0.0.1`, `::1`), for a local dev server; any other `http://` address is refused, because the token travels in a header. No user name, password, query or fragment. |
| `namespace` | no | none | A Vault Enterprise or OpenBao namespace, such as `team-a` or `team-a/prod`. Sent as the `X-Vault-Namespace` header on every request, the login included, so the auth method and the KV mount are both looked up inside it. |
| `mount` | no | `secret` | The path the KV version 2 engine is mounted at, without the `data/` segment: `secret`, `kv`, `apps/kv`. |

A store's `auth_settings`, by method:

| Auth setting | Methods | Default | Meaning |
| --- | --- | --- | --- |
| `role` | `oidc`, `ambient` (required) | | The role to log in as: a JWT auth role for `oidc`, a Kubernetes auth role for `ambient`. |
| `auth_mount` | `oidc`, `ambient` | `jwt` for `oidc`, `kubernetes` for `ambient` | The path the auth method is mounted at, if not the default: `jwt-brokoli`, `kubernetes-prod`. |
| `audience` | `oidc` | `vault` | The audience the OIDC token must carry. The JWT role's `bound_audiences` must include it. |

`mount`, `namespace` and `auth_mount` may contain letters, digits, `-`, `_`,
`.` and `/`, and never `..`.

Settings are checked when the store is saved, without contacting Vault. A
store that saves can still fail to log in or read: test it ([Testing a
store](#testing-a-store)).

## References

```
secret://<store>/<path>[?version=<n>]#<field>
```

- `<path>` is the secret's path inside the KV mount, as `vault kv get`
  takes it after the mount: for `vault kv get -mount=secret warehouse/loader`,
  the path is `warehouse/loader`. Do not include the mount or `data/`.
- `#<field>` is **required**. A KV secret is a map of fields, and a
  reference must say which one it means. A field whose value is not a string
  (a number, a boolean, an object) is returned as its JSON text: `5432`,
  `true`, `{"a":1}`.
- `?version=<n>` reads version `n`, a positive whole number, as
  `vault kv get -version=n` does. Without it, the current version is read.

Examples:

```
secret://vault-prod/warehouse/loader#password
secret://vault-prod/warehouse/loader?version=3#password
secret://vault-prod/aws/s3-exports#secret_access_key
secret://vault-team-a/sftp/partner#private_key
```

A soft-deleted or destroyed version is not found, the same as a path that
does not exist. An older version that was not deleted can still be read
with `?version=`.

## Authentication

| `auth_method` | Vault auth method | What Brokoli stores | Use it when |
| --- | --- | --- | --- |
| `oidc` | JWT auth | the role, and optionally the audience and auth mount | Brokoli has an OIDC token source and Vault can be told to trust its issuer |
| `ambient` | Kubernetes auth | the role, and optionally the auth mount | The node runs in a Kubernetes pod whose service account Vault can verify |
| `token` | none (a token you issue) | the token, encrypted | Nothing else is possible; never the default |

With `oidc` and `ambient`, Brokoli logs in once per store per run, uses the
session token it gets for that run's reads, and revokes it
(`auth/token/revoke-self`) when the reads are done, so a session does not
outlive the run that needed it. A `token` store's token is yours and is never
revoked.

### token: a static Vault token

The store presents a token you create, in the `X-Vault-Token` header. It is
stored encrypted, like a connection password, and is never returned by the
API.

Create a policy ([A policy for the store](#a-policy-for-the-store)), then a
periodic token holding only that policy:

```sh
vault token create \
  -policy=brokoli-warehouse-read \
  -no-default-policy \
  -orphan \
  -period=768h \
  -display-name=brokoli-vault-prod
```

`-period` makes it a periodic token, which does not reach a maximum TTL as
long as it is renewed within the period; Brokoli does not renew it, so pick a
period you will renew or rotate within, or use `-ttl` and replace the token
before it expires. `-orphan` keeps it alive if the token that created it is
revoked.

Store it:

```json
{
  "name": "vault-prod",
  "provider": "vault",
  "settings": {"address": "https://vault.example.com:8200"},
  "auth_method": "token",
  "credential": "hvs.CAESIJ..."
}
```

To rotate, create a new token, then update the store with
`{"credential": "<new token>"}` (an update without `credential` keeps the
stored one), then revoke the old token with `vault token revoke <old token>`.

This is the least safe method: anyone holding a dump of Brokoli's database
and its encryption key holds the token.

### oidc: Vault JWT auth

The store exchanges a short-lived OIDC token from the deployment's token
source for a Vault session:

```
POST <address>/v1/auth/<auth_mount>/login
{"role": "<role>", "jwt": "<token>"}
```

The token is requested for the store's audience (`auth_settings.audience`,
default `vault`). Where it comes from is configured once for the deployment
([Workload identity](workload-identity.md)). With tokens on disk
(`BROKOLI_OIDC_TOKEN_FILES`), project a service-account token whose audience
is `vault`:

```yaml
volumes:
  - name: vault-token
    projected:
      sources:
        - serviceAccountToken:
            path: token
            audience: vault
            expirationSeconds: 3600
containers:
  - name: brokoli
    env:
      - name: BROKOLI_OIDC_TOKEN_FILES
        value: /var/run/secrets/tokens/vault/token
    volumeMounts:
      - name: vault-token
        mountPath: /var/run/secrets/tokens/vault
        readOnly: true
```

A token from disk is the machine's identity, so it falls under
`BROKOLI_SECRET_STORE_AMBIENT=deny` as well. A token source that issues a
token per request puts the workspace, the store's `id`, the run and the node
in its claims, and a role can be bound to those claims.

**Configure Vault.** Enable JWT auth and tell it how to verify the tokens.
For a Kubernetes cluster whose service-account issuer is published (the
usual case on EKS, GKE and AKS), point it at the issuer's discovery document:

```sh
vault auth enable jwt

vault write auth/jwt/config \
  oidc_discovery_url="https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE0123456789" \
  bound_issuer="https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE0123456789"
```

For an issuer whose discovery document Vault cannot reach, give it the
issuer's public key directly instead (for a cluster:
`kubectl get --raw /openid/v1/jwks`, converted to PEM):

```sh
vault write auth/jwt/config \
  jwt_validation_pubkeys=@issuer-public-key.pem \
  bound_issuer="https://kubernetes.default.svc.cluster.local"
```

Then a role. With service-account tokens from disk, bind the role to the
service account Brokoli runs as, through its `sub` claim:

```sh
vault write auth/jwt/role/brokoli-loader \
  role_type=jwt \
  bound_audiences=vault \
  user_claim=sub \
  bound_subject="system:serviceaccount:brokoli:brokoli" \
  token_policies=brokoli-warehouse-read \
  token_ttl=10m \
  token_max_ttl=30m
```

With a token source that names the workspace in a claim, bind the role to it
as well, so another workspace's token cannot log in as this role:

```sh
vault write auth/jwt/role/brokoli-loader - <<'JSON'
{
  "role_type": "jwt",
  "bound_audiences": ["vault"],
  "user_claim": "sub",
  "bound_claims_type": "string",
  "bound_claims": {"workspace_id": "<your workspace id>"},
  "token_policies": ["brokoli-warehouse-read"],
  "token_ttl": "10m"
}
JSON
```

`bound_claims` is a map, so this role is written from JSON on standard input.

`token_ttl` only has to cover one run's reads: Brokoli revokes the session
when it is done with it. If the role uses a different audience, set
`auth_settings.audience` to it; `bound_audiences` and the store's audience
must match.

The store:

```json
{
  "name": "vault-prod",
  "provider": "vault",
  "settings": {"address": "https://vault.example.com:8200"},
  "auth_method": "oidc",
  "auth_settings": {"role": "brokoli-loader"}
}
```

With the auth method mounted elsewhere (`vault auth enable -path=jwt-brokoli jwt`),
add `"auth_mount": "jwt-brokoli"` to `auth_settings`.

### ambient: Vault Kubernetes auth

The store logs in with the Kubernetes service account of the pod that runs
the node. Brokoli reads the pod's service-account token from
`/var/run/secrets/kubernetes.io/serviceaccount/token` and presents it:

```
POST <address>/v1/auth/<auth_mount>/login
{"role": "<role>", "jwt": "<service-account token>"}
```

Vault verifies the token with the cluster's TokenReview API and checks the
service account and namespace against the role. The token file's path is
fixed; it is not a setting, so a store can never make Brokoli send another
file to an address the store names.

This is the machine's identity, so `BROKOLI_SECRET_STORE_AMBIENT=deny`
refuses it. On a worker inside your own cluster, it is the simplest method
there is: nothing to store, and the worker reads with its own service
account, which may differ from the server's.

**Configure Vault.** Enable Kubernetes auth. When Vault runs in the same
cluster, it can use its own pod's service account to call TokenReview:

```sh
vault auth enable kubernetes

vault write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc:443"
```

When Vault runs outside the cluster, give it the API server, its CA, and a
token allowed to call TokenReview (a service account bound to the
`system:auth-delegator` cluster role):

```sh
vault write auth/kubernetes/config \
  kubernetes_host="https://k8s-api.example.com:6443" \
  kubernetes_ca_cert=@cluster-ca.crt \
  token_reviewer_jwt=@token-reviewer.jwt
```

Then a role bound to the service account and namespace the worker runs as:

```sh
vault write auth/kubernetes/role/brokoli-worker \
  bound_service_account_names=brokoli-worker \
  bound_service_account_namespaces=brokoli \
  token_policies=brokoli-warehouse-read \
  token_ttl=10m \
  token_max_ttl=30m
```

The role's optional `audience` parameter restricts which token audience it
accepts. Brokoli presents the pod's default service-account token, whose
audience is the API server's, so leave `audience` unset or set it to that
audience.

The store:

```json
{
  "name": "vault-cluster",
  "provider": "vault",
  "settings": {"address": "https://vault.vault.svc:8200"},
  "auth_method": "ambient",
  "auth_settings": {"role": "brokoli-worker"}
}
```

With the auth method mounted elsewhere (`vault auth enable -path=kubernetes-prod kubernetes`),
add `"auth_mount": "kubernetes-prod"` to `auth_settings`.

## A policy for the store

Every method ends in a Vault token holding the policies you attached. Grant
`read` on the KV **data** path of exactly the secrets the store is for. In a
KV version 2 mount, a secret at `warehouse/loader` is read at
`<mount>/data/warehouse/loader`:

```hcl
# brokoli-warehouse-read.hcl
path "secret/data/warehouse/*" {
  capabilities = ["read"]
}
```

```sh
vault policy write brokoli-warehouse-read brokoli-warehouse-read.hcl
```

Brokoli needs nothing else: not `list`, not the `metadata/` path, and not
`update` or `delete`. Reading an older version with `?version=` uses the same
`read` capability. With `token` auth, a token created with
`-no-default-policy` cannot even look itself up, and does not need to.

In a namespace, write the policy inside it:

```sh
vault policy write -namespace=team-a brokoli-warehouse-read brokoli-warehouse-read.hcl
```

## Complete examples

### Kubernetes worker reading a warehouse password

Vault, once:

```sh
vault secrets enable -version=2 -path=secret kv   # skip if it exists
vault kv put -mount=secret warehouse/loader user=loader password='s3cr3t-pa55'

cat > brokoli-warehouse-read.hcl <<'HCL'
path "secret/data/warehouse/*" {
  capabilities = ["read"]
}
HCL
vault policy write brokoli-warehouse-read brokoli-warehouse-read.hcl

vault auth enable kubernetes
vault write auth/kubernetes/config kubernetes_host="https://kubernetes.default.svc:443"
vault write auth/kubernetes/role/brokoli-worker \
  bound_service_account_names=brokoli-worker \
  bound_service_account_namespaces=brokoli \
  token_policies=brokoli-warehouse-read \
  token_ttl=10m
```

Brokoli, the store:

```sh
curl -X POST https://brokoli.example.com/api/secret-stores \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "vault-cluster", "provider": "vault",
       "settings": {"address": "https://vault.vault.svc:8200"},
       "auth_method": "ambient",
       "auth_settings": {"role": "brokoli-worker"}}'
```

The connection, with the password as a reference and the user name as is:

```sh
curl -X POST https://brokoli.example.com/api/connections \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"conn_id": "warehouse", "type": "postgres",
       "host": "warehouse.internal", "port": 5432, "schema": "analytics",
       "login": "loader",
       "password_ref": "secret://vault-cluster/warehouse/loader#password"}'
```

If the Vault service's address is private (a ClusterIP is), allow it on every
machine that runs nodes, for example `BROKOLI_OUTBOUND_ALLOW_CIDRS=10.43.0.0/16`
([Network](#network)).

### OIDC with a namespace and custom mounts

Vault, inside namespace `analytics`, KV mounted at `kv`, JWT auth mounted at
`jwt-brokoli`:

```sh
export VAULT_NAMESPACE=analytics

vault secrets enable -version=2 -path=kv kv
vault kv put -mount=kv aws/s3-exports access_key_id=AKIAEXAMPLE secret_access_key='wJalrXUtnFEMI/EXAMPLE'

cat > brokoli-s3-read.hcl <<'HCL'
path "kv/data/aws/s3-exports" {
  capabilities = ["read"]
}
HCL
vault policy write brokoli-s3-read brokoli-s3-read.hcl

vault auth enable -path=jwt-brokoli jwt
vault write auth/jwt-brokoli/config \
  oidc_discovery_url="https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE0123456789" \
  bound_issuer="https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE0123456789"
vault write auth/jwt-brokoli/role/brokoli-s3 \
  role_type=jwt \
  bound_audiences=vault-analytics \
  user_claim=sub \
  bound_subject="system:serviceaccount:brokoli:brokoli" \
  token_policies=brokoli-s3-read \
  token_ttl=10m
```

The store:

```json
{
  "name": "vault-analytics",
  "provider": "vault",
  "settings": {
    "address": "https://vault.example.com:8200",
    "namespace": "analytics",
    "mount": "kv"
  },
  "auth_method": "oidc",
  "auth_settings": {
    "role": "brokoli-s3",
    "auth_mount": "jwt-brokoli",
    "audience": "vault-analytics"
  }
}
```

The token file for this store must carry the audience `vault-analytics`, so
project one with that audience and add its path to
`BROKOLI_OIDC_TOKEN_FILES`.

An S3 connection whose extra settings mix a plain key ID with a referenced
secret key:

```json
{
  "bucket": "exports",
  "region": "eu-west-1",
  "access_key": "AKIAEXAMPLE",
  "secret_key": "secret://vault-analytics/aws/s3-exports#secret_access_key"
}
```

### A local dev server

For trying the provider out, a dev server on the same machine:

```sh
docker run -d --name openbao --cap-add IPC_LOCK -p 127.0.0.1:8200:8200 \
  openbao/openbao:2.4.1 server -dev -dev-root-token-id=dev-root \
  -dev-listen-address=0.0.0.0:8200

export BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=dev-root
bao kv put -mount=secret demo/api token=demo-api-token-0123
```

```json
{
  "name": "vault-dev",
  "provider": "vault",
  "settings": {"address": "http://127.0.0.1:8200"},
  "auth_method": "token",
  "credential": "dev-root"
}
```

Plain `http` is accepted here only because the address is loopback, and the
outbound policy still blocks loopback unless the server runs with
`BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true`. Never use a root token outside a dev
server.

## Testing a store

```sh
curl -X POST https://brokoli.example.com/api/secret-stores/$STORE_ID/test \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"path": "warehouse/loader", "field": "password"}'
```

```json
{
  "success": true,
  "version": "3",
  "shape": "map",
  "fields": ["password", "user"],
  "note": "tested from this server, with its identity; a worker resolves with its own, which may differ"
}
```

The test logs in exactly as a run would, reads the path (and `"version"`, if
given), and reports the version it read and the secret's field names, never a
value. An `ambient` store is tested with the server's service account, which
may not be the worker's: a green test on the server does not prove a worker
can log in, and a failed one does not prove it cannot.

## Network

Every request to Vault goes through the deployment's outbound policy, like
any other request a pipeline causes. By default that policy refuses private
and loopback addresses, and most Vault servers are on a private network. On
every machine that runs nodes, allow the range Vault is in:

```sh
BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16
```

`BROKOLI_OUTBOUND_ALLOW_PRIVATE=true` allows every private range. It is
coarser, and unsafe on a server that runs pipelines for several teams, where
a pipeline author is not necessarily trusted with the internal network;
prefer naming the range. Loopback stays blocked unless
`BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true`, which is for local development only.
Cloud metadata addresses stay blocked whatever is allowed. A blocked address
fails with the policy's own message, which says the target is a blocked
private or internal address.

Redirects are followed only within the configured server (same scheme, host
and port). A redirect to any other host is refused before it is followed,
because the token header would go with it. Vault does not redirect in normal
operation: a standby node forwards requests to the active one.

## Error messages

A reference that cannot be resolved fails the node before it runs, and the
error names the connection, the field, the reference and the store. The
part from the Vault provider follows `secret store "<name>" (vault):`. None
of these messages contains a token or a secret value.

**When saving a store** (HTTP 400):

| Message | Cause and fix |
| --- | --- |
| `settings.address is required: the Vault server, https://vault.example.com:8200` | No `address`. |
| `settings.address "x" is not a URL` | The address does not parse, or has no host. |
| `settings.address "x" may not carry credentials, a query or a fragment` | Remove `user:pass@`, `?...` or `#...` from the address. |
| `settings.address "http://vault.example.com": plain http is allowed only for a loopback dev server; use https` | Use `https://`. |
| `settings.address "x": the scheme must be https` | Any other scheme, such as `vault://`. |
| `settings.mount "x" is invalid: letters, digits, '-', '_', '/', no '..'` | Fix the mount path. |
| `settings.namespace "x" is invalid` | The namespace has a character outside the allowed set, or `..`. |
| `auth_settings.auth_mount "x" is invalid` | Same rule as the mount. |
| `auth_settings.role is required for auth_method "oidc": the Vault role to log in as` | Set `auth_settings.role` (also for `"ambient"`). |

**When logging in:**

| Message | Cause and fix |
| --- | --- |
| `vault: oidc login at auth/jwt as role "brokoli-loader": permission denied (status 400: ...)` | Vault refused the token. The reason after the status is Vault's own. `error validating token: invalid audience (aud) claim` means `bound_audiences` does not include the store's audience. `error validating claims: claim "workspace_id" does not match any associated bound claim values` means a `bound_claims` mismatch, and `error validating token: invalid subject (sub) claim` a `bound_subject` mismatch. `role "x" could not be found` means the role name or `auth_mount` is wrong. `could not load configuration` means the JWT mount has no `config` yet. |
| `vault: kubernetes login at auth/kubernetes as role "brokoli-worker": permission denied (status 400: ...)` or `(status 403: ...)` | Vault refused the service-account token, and its reason follows. `invalid role name "x"` means the role or `auth_mount` is wrong; a reason naming the service account or namespace means they are not bound to the role; a TokenReview failure means Vault cannot verify tokens with the cluster (check `auth/kubernetes/config`). |
| `vault: oidc login at auth/jwt as role "x": permission denied (status 403: permission denied)` | Nothing is mounted at that `auth_mount` (Vault answers an unknown auth path with 403). |
| `vault: ambient auth reads this machine's Kubernetes service-account token, and there is none (open /var/run/secrets/kubernetes.io/serviceaccount/token: no such file or directory)` | The node does not run in a Kubernetes pod, or the pod has `automountServiceAccountToken: false`. |
| `vault: oidc login has no token to present` | The token source returned an empty token. |
| `vault: oidc login at auth/jwt returned no client token` | The login answered 200 without a session token; check the auth method. |
| `vault: oidc login at auth/jwt as role "x" failed with status 500: ...` | Any other status: Vault's own reason follows. A 503 is a sealed server. |
| `vault: no token to present` | A `token` store whose stored token decrypted to nothing. |
| `ambient identity is disabled on this deployment` | The store uses `ambient`, or `oidc` with a token from disk, and the operator set `BROKOLI_SECRET_STORE_AMBIENT=deny`. |

**When reading:**

| Message | Cause and fix |
| --- | --- |
| `secret not found: secret/data/warehouse/loader (vault: no such secret, or this version is deleted)` | No secret at that path, the version asked for does not exist, or it was deleted or destroyed. Check the path with `vault kv get -mount=secret warehouse/loader`. |
| `secret not found: kv/data/warehouse/loader (vault: no handler for route "kv/data/warehouse/loader". route entry not found.)` | Nothing is mounted at `settings.mount` (or not in this namespace). A token without access to the path may get `permission denied` instead, since Vault checks policy first. |
| `secret not found: secret/data/x has no data at this version (deleted or destroyed)` | A server answered a read with an empty version. |
| `permission denied on secret/data/warehouse/loader (vault: permission denied; the token's policies do not allow reading it, or the token is invalid or expired)` | The token's policies do not grant `read` on that data path, or the token is not valid (expired, revoked, or from another server or namespace). With `token` auth, check `vault token lookup <token>`; with the others, check the role's `token_policies`. |
| `vault: version "latest" is not a positive whole number` | `?version=` must be `1`, `2`, ... |
| `vault: invalid secret path "x"` | An empty path, or one containing `..`. |
| `vault: read secret/data/x: status 503: Vault is sealed` | Any other status, with Vault's own reason. |
| `vault: read secret/data/x: not a KV version 2 response` | The server answered the read with something that is not a KV version 2 secret. A KV version 1 mount does not get this far: it has no `data/` path, so a read of it is `secret not found`. |
| `the secret has no field "x"` | The secret exists without that field; the message lists the fields it has. |
| `vault: GET /v1/secret/data/x: request target is a blocked private/internal address: 10.0.0.5 (private/link-local)` | The outbound policy refused Vault's address ([Network](#network)). |
| `vault: GET /v1/secret/data/x: dial tcp 10.0.0.5:8200: connect: connection refused` | Vault is not reachable from the machine that runs the node. |
| `vault: refusing a redirect away from https://vault.example.com:8200` | The server redirected to another host; the token was not sent there. |

## How it talks to Vault

The provider uses Vault's HTTP API directly, with no Vault client library:
one login (`POST /v1/auth/<mount>/login`), one read per secret
(`GET /v1/<mount>/data/<path>[?version=n]`), and one revoke
(`POST /v1/auth/token/revoke-self`) when a logged-in session is done. A run
reads each `secret://` reference once and caches the value in memory for that
run only ([Secret stores](secret-stores.md#what-brokoli-keeps-and-what-it-never-keeps)).
Requests time out after 30 seconds, and a response larger than 1 MiB is
refused.

The provider is tested against OpenBao's dev server, which implements the
same API.

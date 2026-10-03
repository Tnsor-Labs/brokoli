# ADR-041: External secret stores — credentials stay in the customer's secret manager

**Status:** proposed
**Date:** 2026-09-27

## Context

A connection's credentials are stored in Brokoli's database today:
encrypted with AES-256-GCM under one key per deployment
(`crypto/crypto.go:23-86`), decrypted at run time, never returned by the
API. That is the right default for a laptop or a single-team server. It
is the wrong one for anyone whose security policy says credentials live
in their secret manager and nowhere else. For them every copy is a copy
too many, and a database dump plus the key file is every credential of
every workspace.

Airflow solved this years ago with secrets backends. Dagster and
Terraform Cloud have their own versions. The idea is simple: the
orchestrator stores a *reference*, and fetches the value from the
customer's store when a run needs it.

### What already exists

Brokoli has half of this, and the half it has does not fit the need.

- **References exist.** A connection stores `password_ref` and
  `extra_ref` (migration `008_credential_refs.sql`). A value like
  `encrypted://…`, `env://NAME`, `vault://path#key` or `k8s://ns/secret/key`
  is resolved by `pkg/secrets.Chain` at run time
  (`engine/connection_resolver.go:208-239`).
- **But the backends belong to the operator, not the workspace.** The
  chain is built once per process from environment variables
  (`pkg/secrets/factory.go:14-47`, `cmd/serve.go:387`). `vault://` reads
  with the server's own `VAULT_TOKEN`. `Resolver.Resolve(ctx, ref)`
  carries no workspace, so it cannot choose a workspace's store, and
  every reference passes through one operator allowlist
  (`pkg/secrets/refallow.go`). One team cannot bring its own Vault, and
  a second team cannot bring a different one.
- **Only one Vault shape.** The Vault resolver reads KV v2 and nothing
  else. There is no AWS, GCP, Azure or Infisical backend.
- **The seam is closed.** Adding a backend means editing
  `NewDefaultChain`. `extensions.SecretProvider`
  (`extensions/interfaces.go:216-228`) was meant for this and has no
  caller.

### What is wrong with the path a reference takes

These matter to this ADR because an external store makes each of them
worse. They are also bugs today.

1. **A failed resolution is silent.** `resolveCredentials` writes the
   error to the server log and carries on
   (`connection_resolver.go:217-219`, `:229-231`). The node runs with an
   empty password, and the author sees an authentication failure from
   the database, with nothing in the run log to say the reference
   failed.
2. **The connection test bypasses references.** It decrypts `Password`
   and `Extra` directly (`api/handlers_connection.go:332-354`), so a
   connection whose password lives behind a reference tests with an
   empty password. `docs/secret-references.md` admits it.
3. **References are not validated when saved.** An unknown scheme or a
   typo is found on the first run.
4. **Resolved values travel in work orders.** `executeNode` writes the
   resolved password and headers into `node.Config` (`engine/runner.go:779`,
   `connection_resolver.go:202`). That config is what remote
   `source_api` pagination puts in its `InstanceWorkOrder`
   (`engine/pagination_remote.go:155-157`). ADR-033 says queue messages
   "do not contain … secret values" (033:367-369).
5. **Nothing redacts a resolved value from node logs.** Recorded SQL
   masks secret variables (`engine/sql_record.go`), `run.Error` masks
   database URLs in API responses (`api/handlers_run.go:26-37`), and
   that is all.
6. **One credential per connection is a JSON blob.** `extra_ref` must
   resolve to the whole `Extra` document. For S3 that holds the access
   key *and* the secret key; for SFTP, the host key *and* the private
   key; for Azure Blob, the account name *and* the account key. A
   customer's secret manager holds one secret per value, not a Brokoli
   JSON document.

### What the providers have in common

The research behind this ADR covered Vault, OpenBao, Infisical, AWS
Secrets Manager and SSM Parameter Store, GCP Secret Manager, Azure Key
Vault, 1Password, Doppler, Bitwarden, Akeyless and CyberArk Conjur,
from each vendor's own documentation.

- **There is no shared read protocol.** Each has its own API, its own
  path syntax and its own versioning. The Kubernetes External Secrets
  Operator, Dapr and the Secrets Store CSI driver are adapter layers
  over the vendors' SDKs, not protocols. Only OpenBao speaks Vault's API.
- **Values come in two shapes.** A single string (Azure, SSM,
  Infisical, Doppler, Conjur) or a map of named fields (Vault KV, AWS
  secrets created as key/value, 1Password items).
- **The shared protocol is on the authentication side.** Vault,
  OpenBao, Infisical, AWS, GCP, Azure, Doppler, Akeyless and Conjur all
  accept a signed OIDC JWT from an issuer the customer trusts, and
  exchange it for their own short-lived session: Vault's JWT auth
  method, AWS `AssumeRoleWithWebIdentity`, GCP Workload Identity
  Federation, Azure federated identity credentials, Infisical OIDC Auth.
  This is how GitHub Actions and Terraform Cloud reach clouds without
  storing a credential.

So adding a provider does not mean writing everything from scratch. The
read is small and provider-specific. The authentication, which is where
the hard and dangerous part lives, is one mechanism shared by all of
them.

### Two ways a pipeline runs

A node runs on the server itself, on a worker next to the server, or on
a worker the customer runs inside their own network. The same store
must work for all three, and they reach it differently:

- A worker in the customer's network may be the only machine that can
  reach the customer's Vault at all. It usually already has an identity
  there: an IAM role, a Kubernetes service account, a managed identity.
- A server or worker someone else operates has no identity of the
  customer's. It has to be given one, and the safest one to give is a
  short-lived token, not a stored password.

## Decision

Brokoli gains **secret stores**: named, workspace-scoped connections to
a customer's secret manager. A credential anywhere in a connection can
be a reference into a store, `secret://<store>/<path>#<field>`. The
value is fetched on the machine that runs the node, when the node runs,
and is never written anywhere. Providers implement a small read
interface. Authentication is shared across providers and has three
methods: the machine's own identity, a short-lived OIDC token, or a
stored token as the last resort.

All of this is core. Section 9 says why, and what a distribution may add.

### 1. A secret store is a workspace object

| Field | Meaning |
| --- | --- |
| `id` | Immutable, assigned on create. What an identity token names (section 4), so renaming a store never changes who it trusts. |
| `name` | Unique within the workspace. What references use. Lowercase letters, digits, `-`. Can be renamed. |
| `provider` | `vault`, `aws_secrets_manager`, `aws_ssm`, `gcp_secret_manager`, `azure_key_vault`, `infisical`, `http`. |
| `settings` | The provider's non-secret settings: Vault's address, namespace and mount; an AWS region; a GCP project; an Azure vault URL; an Infisical project and environment. |
| `auth` | One of `ambient`, `oidc`, `token` (section 4), with that method's non-secret settings: a Vault role, an AWS role ARN, an audience. |
| `credential` | Only for `auth: token`. Encrypted at rest exactly like a connection password, and never returned by the API. |

A store's scope is the workspace, the same scope as connections and
variables. A reference resolves against the stores of the run's own
workspace. There is no lookup across workspaces, so a store name cannot
leak access from one workspace to another, and two workspaces can each
have a store called `prod` pointing at different Vaults.

A store holds how to reach a secret manager. It never holds a value
fetched from one.

### 2. References

```
secret://<store>/<path>[?version=<v>][#<field>]
```

- `<store>` is a store name in the run's workspace.
- `<path>` is the provider's own name for the secret, unchanged, so a
  customer copies it from their secret manager's console:

  | Provider | `<path>` | Example |
  | --- | --- | --- |
  | `vault` | Secret path under the configured mount | `secret://vault-prod/warehouse/loader#password` |
  | `aws_secrets_manager` | Secret name or ARN | `secret://aws/prod/warehouse#password` |
  | `aws_ssm` | Parameter name | `secret://ssm/prod/warehouse/password` |
  | `gcp_secret_manager` | Secret name in the configured project | `secret://gcp/warehouse-password` |
  | `azure_key_vault` | Secret name in the configured vault | `secret://kv/warehouse-password` |
  | `infisical` | Secret path and name | `secret://infisical/warehouse/PASSWORD` |
  | `http` | Appended to the configured URL | `secret://internal/warehouse` |

- `#<field>` selects one field of a map-shaped secret (Vault KV, a JSON
  AWS secret). It is required for a map-shaped secret and refused for a
  single-string one, so a reference never silently yields a whole JSON
  document where a password was expected.
- `?version=` pins a version, in the provider's own syntax. The default
  is the current version.

One new scheme rather than one scheme per store: store names are chosen
by users, and would collide with `env`, `vault`, `k8s` and any scheme
added later.

**Where a reference may appear.** Everywhere a connection holds a
credential:

- the password, as today (`password_ref`);
- the whole extra document, as today (`extra_ref`);
- **any single string value inside extra.** This is new, and it is the
  one that makes the feature usable. S3's `secret_key`, SFTP's
  `private_key` and Azure Blob's `key` each become a reference to one
  secret, while `access_key`, `host` and `account` stay plain values
  beside them.

A value is a reference only when it starts with `secret://`. A literal
credential that happens to look like one is a thing nobody has, and
the form (section 8) never produces one by accident.

The existing `env://`, `vault://` and `k8s://` schemes stay as they
are: operator-level references, resolved with the server's own
credentials, behind the operator's allowlists. They suit a single-team
deployment where the operator is the only user. `secret://` is the
mechanism for everyone else.

### 3. Providers implement a read, nothing more

```go
package secretstore

// A Provider knows one secret manager's API.
type Provider interface {
	// Name is the provider value a store names: "vault", "aws_ssm", ...
	Name() string
	// ValidateSettings checks a store's settings when it is saved. It
	// does not touch the network.
	ValidateSettings(s Settings) error
	// Open returns a client for one store, authenticated with id.
	Open(ctx context.Context, s Settings, id Identity) (Store, error)
}

// A Store reads secrets from one configured secret manager.
type Store interface {
	Get(ctx context.Context, path, version string) (Secret, error)
	Close() error
}

// A Secret is a single string or a map of named fields, never both.
type Secret struct {
	Value   []byte
	Fields  map[string][]byte
	Version string
}
```

Authentication is not the provider's business. `Identity` (section 4)
hands the provider what it needs: a JWT to exchange, a static token, or
nothing, meaning use the environment. A provider maps that onto its
vendor's login call. In practice that is one function per provider, for
example Vault `POST auth/<mount>/login` with the JWT and role, or
`AssumeRoleWithWebIdentity` for AWS.

Each provider is a vendor SDK plus a few hundred lines: settings, one
login mapping, one `Get`, and the vendor's error codes translated into
messages that name the cause (`permission denied on secret/warehouse`,
`secret not found`). A new provider is one new file and a test against
the vendor's emulator or dev server.

**The `http` provider covers the long tail** without a Go change: a URL
template, a method, headers, and a JSON pointer to the value in the
response, authenticated with the same three methods. Anything with an
HTTP API that returns JSON works: an internal secrets service, a
provider not yet built in.

**Registration follows the existing pattern** (ADR-024:214-218). Core
holds a plain list of providers. A distribution appends its own through
a field on `extensions.Registry`. There is no `init()` self-registration.

**Dependencies are a release constraint, as for Azure Blob (#687).**
Every vendor SDK grows the single binary. Each provider PR reports the
binary delta and the license gate's result, and a provider whose SDK is
disproportionate talks to the vendor's REST API directly. AWS is already
a dependency (S3). GCP's and Azure's official clients are the ones to
measure.

**Not built in:** Bitwarden, because its SDK license allows use only
with Bitwarden's own services and forbids offering applications built
on it to third parties. HashiCorp Vault's *server* is BSL-licensed, but
its Go API client is MPL-2.0, and only the client is imported.

### 4. Identity: three methods, shared by every provider

| Method | What the store holds | Who can use it | Stored credential |
| --- | --- | --- | --- |
| `ambient` | Nothing, or a role to assume | The machine that runs the node, with its own environment identity: AWS default chain (instance role, IRSA), GCP application default credentials, Azure managed identity, Vault Kubernetes auth with the pod's service account | None |
| `oidc` | An audience and a provider role | Any machine that holds a token source (below) | None |
| `token` | A static token: a Vault token, an Infisical client secret, a Doppler service token | Anyone who can run the node | Yes, encrypted |

`token` exists because some providers and some small setups have
nothing else. It is the least safe method, and the form says so. It is
never the default.

**An operator can switch `ambient` off on a machine**
(`BROKOLI_SECRET_STORE_AMBIENT=deny`). On a single-team server the
machine's identity is the team's, and `ambient` is the simplest method
there is. On a server that runs pipelines for several teams, or for
customers, the machine's identity belongs to the operator: a workspace
that could choose `ambient` there would read the operator's secrets with
the operator's role. A deployment like that must deny it on every
machine it runs, and a store that asks for it fails with a message
saying the method is disabled here, not with a permission error from
the cloud.

**Token sources.** `oidc` needs a JWT that the customer's secret manager
trusts:

```go
// A TokenSource issues a short-lived OIDC token for one store, on
// behalf of the run the node belongs to.
type TokenSource interface {
	Token(ctx context.Context, req TokenRequest) (string, error)
}

type TokenRequest struct {
	Audience    string // from the store's auth settings
	WorkspaceID string
	StoreID     string // the immutable ID, never the name
	RunID       string
	NodeID      string
}
```

Core ships one token source: a file, re-read on every request. It is
the shape of a Kubernetes projected service-account token, which AWS,
GCP, Azure and Vault all accept when the cluster's issuer is published.
A distribution may provide another. A hosted service, for example, can
act as an OIDC issuer itself and mint a token per run, the way Terraform
Cloud does. Core defines the interface and the request, not who signs.

### 5. Resolution happens where the node runs, scoped to its workspace

The rule already in `docs/secret-references.md` stays: a reference is
resolved on the machine that runs the node. What changes:

- **Resolution gets a scope.** The resolver interface becomes
  `Resolve(ctx, scope Scope, ref string)`, with
  `Scope{WorkspaceID, RunID, NodeID}`. The operator-level schemes
  ignore it. `secret://` uses the workspace to find the store, and the
  run and node to request a token.
- **A worker in the customer's network fetches the secret itself**, with
  its own identity or its own token source. The value never passes
  through the server. This is the reason resolution stays on the
  executing machine, and it is what makes the feature possible for a
  store only reachable from inside the customer's network.
- **Work orders carry references, not values.** Remote dispatch sends
  the connection ID and lets the worker resolve it. Finding 4 is fixed
  as part of this ADR, not left for later, because a design that
  resolves on the worker gains nothing if the server resolves first and
  ships the plaintext anyway.
- **Every read goes through the outbound policy** (ADR-022), like SFTP,
  S3 and Azure Blob. A store at a private address needs the operator's
  allowlist. The operator-configured `VAULT_ADDR` exemption in ADR-022
  applies to the operator-level `vault://` scheme only.

**Caching.** Values are cached in memory for the lifetime of one run,
keyed by store, path and version, so a pipeline with forty nodes on one
connection makes one request. The cache is dropped when the run ends. A
provider session (a Vault token, AWS temporary credentials) is cached
per store up to its own expiry and renewed before it.

**Nothing is persisted.** A fetched value is never written to the
database, a work order, a log, a run record, a preview, an artifact or
the IR.

### 6. Failure is loud, and it names the reference

A reference that cannot be resolved fails the node. The error names the
connection, the field, the store, the path and the provider's reason,
never the value:

```
connection "warehouse": password: secret://vault-prod/warehouse/loader#password:
permission denied (Vault: 403 on secret/data/warehouse/loader)
```

This applies to every scheme, including the existing ones, and it is a
behaviour change. A run that today proceeds with an empty password after
a failed `env://` reference will fail instead, at the step that caused
it. The release notes say so.

### 7. Resolved values are redacted

Every value a run resolves is added to that run's redaction set. Node
log lines, `run.Error`, recorded SQL and validation-failure messages
pass through it. Values shorter than 8 bytes are not redacted, because
masking every `yes` in a log would make it unreadable, and the form
warns when a secret is that short. (8, not the 6 first written here: the
same floor recorded SQL already used, so the two masks agree.)

### 8. What a user does

**Create a store** (workspace settings, "Secret stores"): pick the
provider, fill in its two or three settings, pick an identity method,
and press **Test**. The test authenticates and reads a path the user
names. It reports success and the version it saw, never the value.

**Use it in a connection:** every credential field in the connection
form gets a choice, "Stored in Brokoli" or "From a secret store". The
second shows a store picker, a path field and, for map-shaped secrets, a
field name. **Test connection** resolves the references through the same
path a run uses (finding 2 fixed), then tests the connection.

**Where the test runs is shown.** A store using `ambient` or a file
token source is tested with the *server's* identity. A worker in the
customer's network uses its own. When they differ, the test result says
which one it used, so a green test on the server is not mistaken for
proof that the worker can read the secret.

**References are validated on save** (finding 3): the syntax, that the
store exists in the workspace, and that `#field` is present exactly
when the provider's secrets are map-shaped. Nothing is fetched on save.

### 9. This is core

ADR-004 listed "secret injection from vaults" among enterprise plugin
features. This ADR moves fetching credentials from a customer's secret
manager into core, and ADR-004 gets an update saying so.

The reason is ADR-004's own argument: "Connectors that only work for
paying customers is not a connector ecosystem; it's a demo." A self-hosted
team whose policy forbids credentials in application databases cannot
adopt Brokoli at all without this. Airflow ships its secrets backends in
the open-source distribution.

What core does not decide, and a distribution may add, is operational:
issuing identities (a `TokenSource` that signs per-run tokens), guided
setup that writes the customer's trust policy for them, governance over
which stores and providers a workspace may use, and audit of every
store change and token issued.

## Consequences

### Positive

- A deployment can hold no customer credential at all. With `ambient` or
  `oidc`, Brokoli stores the location of a secret and nothing that opens
  it.
- A worker inside the customer's network reads their store directly. The
  secret never crosses the server.
- A customer's own audit log (CloudTrail, Vault's audit device) records
  every read, attributed to a run when the token carries it.
- Rotating a credential is a change in the secret manager, and the next
  run picks it up, with no edit in Brokoli.
- Three existing bugs get fixed: silent resolution failures, a test that
  ignores references, and plaintext in work orders.
- A new provider is one file against a stable interface, and `http`
  covers what is not built in.

### Negative

- Runs depend on the customer's secret manager being up. A Vault outage
  fails runs that would otherwise have succeeded. That is the point of
  the customer's policy, but it is a new failure mode.
- Every vendor SDK grows the binary and the dependency surface.
- Failing closed on unresolvable references breaks runs that were
  silently passing on empty credentials. They were already broken; now
  they say so.
- `ambient` means the same store resolves differently on different
  machines. The test result has to say where it ran.
- Per-field references change the connection model's contract: `Extra`
  is no longer a document that is either all plaintext or all one
  reference.

### Deferred

- **Dynamic secrets.** Vault's database engine issues a credential per
  lease, which could be one per run, revoked when the run ends. `Secret`
  gains a TTL and a revoke hook when this is built. The interface above
  leaves room for it.
- **Secret variables from stores.** `${var.X}` of type secret stays
  encrypted in the database for now. Letting a variable hold a
  `secret://` reference is a follow-up, once connections have proven the
  resolution path.
- **1Password, Doppler, Akeyless, Conjur** as built-in providers.
  Doppler, Akeyless and Conjur accept OIDC. 1Password accepts only
  static tokens. Until they are built in, the `http` provider reaches
  those with an HTTP API.
- **Writing secrets.** Brokoli reads; it never creates or rotates a
  secret in a customer's store.
- **Replacing the operator-level schemes.** `vault://` with a process
  token is kept for single-team deployments. Whether it becomes a
  deployment-wide default store is a later decision.

## Alternatives considered

- **One scheme per store (`vault-prod://…`).** Shorter, and what some
  tools do. Rejected because store names are user-chosen and collide
  with existing and future schemes, and because the scheme is how the
  chain dispatches today (ADR-024).
- **Import the External Secrets Operator's or Dapr's provider code.** ESO
  is Apache-2.0 and covers about 49 providers, but its clients are built
  around the Kubernetes API. Dapr's components are one Go module with
  every component in it, which is a heavy dependency tree. The interface
  shape is borrowed from ESO; the code is not.
- **`gocloud.dev/secrets`.** An encrypt/decrypt abstraction over KMS
  services, not a named-secret store.
- **Sync into Kubernetes secrets with ESO, then read `k8s://`.** Works
  for teams on Kubernetes who run ESO, and is possible today. It copies
  every secret into the cluster, requires Kubernetes, and gives one
  operator-wide set of secrets rather than one per workspace.
- **Always resolve on the server and ship values to workers.** Simpler.
  Rejected: a store reachable only inside the customer's network would
  be unreachable, and the plaintext would cross the server and the queue,
  which is what this ADR exists to avoid.
- **Store a bootstrap token per store as the default method.** Easiest
  to set up, and still what `token` offers. Rejected as the default
  because a dump of the database would again hold a key to every
  customer's secret manager.
- **Keep this an enterprise feature (ADR-004's line).** Section 9.

## Follow-ups

Prerequisites. These are bugs today, independent of the feature:

- #751: fail the node on an unresolvable reference, and say why in the
  run log (finding 1).
- #752: resolve references in the connection test (finding 2).
- Validate references on save (finding 3).
- #753: send references, not resolved values, in remote work orders
  (finding 4).
- Redact resolved values from node logs and run errors (finding 5).
- #754: when the encryption key cannot be loaded, the server falls back
  to an all-zero key and only logs a warning (`cmd/serve.go:377-380`), and
  a damaged key file is silently overwritten with a new key. Both were
  reproduced. It should refuse to start.
- #755: `api/connection_masking.go:14-17` says references are hidden
  everywhere. `maskRef` (`api/handlers_connection.go:732-740`) hides only
  `encrypted://`. The comment and the code have to agree, one way or the
  other.

Phases:

1. **Groundwork**: the prerequisites above, plus `Scope` on the resolver
   interface. No new feature, and every one of them is worth shipping on
   its own.
2. **Stores and references**: the store model and API, `secret://` in
   password, extra and extra fields, `vault` and `aws_secrets_manager`
   / `aws_ssm` providers, `ambient` and `token` identity, the form and
   the store page. Tested in CI against OpenBao's dev server (MPL-2.0,
   the same API as Vault) and LocalStack's Secrets Manager and SSM.
3. **OIDC and the remaining providers**: the file token source,
   `gcp_secret_manager`, `azure_key_vault`, `infisical`, `http`.
4. **Deferred items** as demand appears: dynamic secrets, secret
   variables from stores.

## Update (2026-09-27)

The token-source half of section 4 is built, ahead of the stores
themselves, because ADR-042 (BigQuery) needs it first.

- **It lives in `pkg/identity`, not in the secret-store package**, since
  backends that are not secret stores use it too. `TokenRequest` names its
  subject as `SubjectKind` and `SubjectID` (`"store"` or `"connection"` and
  an immutable ID) instead of a store ID.
- **The file token source is the machine's identity.** Section 4 presented
  it as a way to use `oidc` anywhere. A token a platform projects onto the
  machine, such as a Kubernetes service-account token, identifies the pod,
  not the workspace, so it is ambient identity in another form. It
  implements `identity.Machine`, and `identity.Token` refuses it where
  `BROKOLI_SECRET_STORE_AMBIENT=deny`. `oidc` that is safe on a server
  running work for several workspaces needs a source that issues a token
  per workspace, which a distribution provides through
  `extensions.Registry.TokenSource`.
- **The ambient switch exists** (`identity.AmbientAllowed`), and the file
  source is its first user.
- **Google workload identity federation is built on it**
  (`pkg/identity/gcpfederation`). Brokoli builds the external-account
  configuration itself, as ADR-042 section 2 requires.

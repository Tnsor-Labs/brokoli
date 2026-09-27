# Workload identity: reaching a customer's cloud without a stored key

A connection can authenticate to a customer's cloud with a **short-lived
token** that the customer's side trusts, instead of a key stored in Brokoli.
The customer configures their cloud once to trust an issuer. From then on,
each time a connection needs access, Brokoli presents a token from that
issuer and exchanges it for temporary access. Nothing that opens the
customer's resources is stored.

This page is for **operators**: where the tokens come from, and the switch
that governs them. The connection types that use them document their own
settings. BigQuery is the first (ADR-042).

- [Where tokens come from](#where-tokens-come-from)
- [Tokens on disk](#tokens-on-disk)
- [The ambient switch](#the-ambient-switch)
- [Google Cloud](#google-cloud)
- [Network policy](#network-policy)

## Where tokens come from

A deployment has at most one token source:

1. **One provided by the distribution**, if it has one. A hosted service can
   issue a token per workspace and run, with the workspace in its claims, so
   a customer trusts their own workspace and nobody else's.
2. Otherwise, **tokens a platform writes to disk** (`BROKOLI_OIDC_TOKEN_FILES`).
3. Otherwise, **none**. A connection set to use a token fails with a message
   saying the deployment has no token source.

The server logs which one it uses at startup:

```
OIDC token source: BROKOLI_OIDC_TOKEN_FILES (the machine's identity; honours BROKOLI_SECRET_STORE_AMBIENT)
```

## Tokens on disk

`BROKOLI_OIDC_TOKEN_FILES` lists token files, comma-separated. Each holds one
JWT for one audience. A request is served from the file whose token carries
the requested audience.

On Kubernetes, project a service-account token for each audience your
connections need:

```yaml
volumes:
  - name: gcp-token
    projected:
      sources:
        - serviceAccountToken:
            path: token
            audience: https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/cluster
            expirationSeconds: 3600
containers:
  - name: brokoli
    env:
      - name: BROKOLI_OIDC_TOKEN_FILES
        value: /var/run/secrets/tokens/gcp/token
    volumeMounts:
      - name: gcp-token
        mountPath: /var/run/secrets/tokens/gcp
        readOnly: true
```

The file is read again on every request, because the platform rotates it in
place. A token past its expiry is refused, with a message saying the platform
has stopped refreshing it, rather than being sent to be refused later.

The customer's side must trust the cluster's service-account issuer. That
issuer must be publicly reachable for Google, AWS and Azure. Vault can also
be given the issuer's keys directly.

## The ambient switch

A token from disk identifies **the machine**, meaning the pod's service
account, not the workspace whose pipeline asked for it. The same holds for
any identity the machine already has: a cloud instance role, or Application
Default Credentials.

On a server that runs pipelines for one team, that is exactly right. On a
server that runs pipelines for several teams or customers, the machine's
identity is the operator's, and a workspace able to use it would reach the
operator's resources. Switch it off there:

```sh
BROKOLI_SECRET_STORE_AMBIENT=deny
```

Every use of the machine's identity is then refused, with a message saying
it is disabled on this deployment, not with a permission error from the
cloud. Set it on every machine that runs pipelines, workers included. A token
source that issues a token per workspace is not affected.

## Google Cloud

Google trusts an external issuer through a workload identity pool provider.
For the token-on-disk case, where the issuer is your cluster:

```sh
gcloud iam workload-identity-pools create brokoli --location=global
gcloud iam workload-identity-pools providers create-oidc cluster \
  --location=global --workload-identity-pool=brokoli \
  --issuer-uri=<your cluster's service-account issuer> \
  --attribute-mapping=google.subject=assertion.sub
```

A connection then names:

| Setting | Example |
| --- | --- |
| Provider | `//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/cluster` |
| Service account (optional) | `loader@acme-analytics.iam.gserviceaccount.com`, impersonated after the exchange, so roles are granted to it |
| Token audience (optional) | Defaults to Google's default for the provider: the provider name with an `https:` scheme |

Brokoli builds the rest of the configuration itself. It does not accept an
external-account file from a connection: such a file can make Google's
library fetch any URL or run a local command.

## Network policy

The token exchange and the service-account impersonation go through the
deployment's outbound policy, like every other request. Google's public
endpoints need nothing extra.

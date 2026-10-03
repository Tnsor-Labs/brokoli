# AWS secret stores: Secrets Manager and SSM Parameter Store

Two [secret store](secret-stores.md) providers read credentials from AWS:

- **`aws_secrets_manager`**: AWS Secrets Manager. A secret is a string or a
  JSON object of key/value pairs.
- **`aws_ssm`**: SSM Parameter Store. A parameter is one value, decrypted
  when it is a `SecureString`. It calls SSM's `GetParameter` API directly,
  signed with the AWS SDK's own SigV4 signer, rather than linking the whole
  SSM SDK, which added about 3 MB to the binary for this one call.

Both authenticate as an IAM role, with no stored AWS key: the machine's own
identity (`ambient`), or an OIDC token exchanged for a role (`oidc`).

- [Creating a store](#creating-a-store)
- [Authentication](#authentication)
- [References](#references)
- [Versions](#versions)
- [IAM permissions](#iam-permissions)
- [The OIDC trust policy](#the-oidc-trust-policy)
- [Errors](#errors)
- [Network](#network)
- [Testing against LocalStack](#testing-against-localstack)

## Creating a store

```json
{
  "name": "aws-prod",
  "provider": "aws_secrets_manager",
  "settings": { "region": "eu-west-1" },
  "auth_method": "oidc",
  "auth_settings": {
    "role_arn": "arn:aws:iam::123456789012:role/brokoli-secrets-reader"
  }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `settings.region` | yes | The AWS region the secrets live in, such as `eu-west-1`. The only setting. |
| `auth_settings.role_arn` | for `oidc`; optional for `ambient` | The IAM role to assume. |
| `auth_settings.audience` | no, `oidc` only | The audience of the token Brokoli requests. Defaults to `sts.amazonaws.com`. Set it only if your trust policy names another. |

**There is no `endpoint` setting.** A store's settings come from a workspace
editor, and whoever controls the endpoint receives the session's
credentials and the OIDC token. A store therefore always talks to AWS's own
endpoints for its region.

**There is no `token` method.** These AWS APIs have no static bearer token,
and storing a long-lived access key pair in Brokoli is exactly what secret
stores exist to avoid. Use `ambient` or `oidc`.

## Authentication

### `oidc`: no AWS credential anywhere

Brokoli asks the deployment's OIDC token source for a short-lived token, then
calls `sts:AssumeRoleWithWebIdentity` with it to get temporary credentials for
`role_arn`. The call is made without any credentials of the machine, so no
identity of the server can enter this path, even through a misconfigured
environment.

The token names:

- the workspace;
- the store, by its immutable ID;
- the run;
- the node.

Each assumed-role session is named after them: `brokoli-run-<run>-<node>`,
or `brokoli-store-<store>` for a store test. Every read shows up in CloudTrail
under that name.

### `ambient`: the machine's own identity

The machine that runs the node reads with its own AWS identity: environment
variables, the shared configuration, IRSA on EKS, or an EC2 or ECS instance
role. With `role_arn`, the machine's credentials first assume that role, so
one machine identity can read each workspace's secrets as a different role.

Two things to know:

- **Deny it on shared servers.** On a server that runs pipelines for several
  teams, the machine's identity is the operator's. Set
  `BROKOLI_SECRET_STORE_AMBIENT=deny` there, and a store using `ambient` is
  refused by name before it reaches AWS.
- **The credentials themselves are fetched outside the outbound policy.**
  Loading the machine's credentials uses the AWS SDK's own client, because
  the instance role is served from the instance metadata endpoint, which the
  outbound policy always refuses. Every read of a secret still goes through
  the outbound policy (see [Network](#network)).

## References

**Secrets Manager:** the secret's name or ARN, then the field of a key/value
secret.

```
secret://aws-prod/prod/warehouse#password
secret://aws-prod/arn:aws:secretsmanager:eu-west-1:123456789012:secret:prod/warehouse-AbCdEf#password
secret://aws-prod/prod/plain-api-token
```

A secret's shape is decided per secret:

| SecretString | Shape | Reference |
| --- | --- | --- |
| A JSON object whose values are strings, numbers or booleans (what the console creates for key/value pairs) | fields | `#field` required |
| Anything else: plain text, a JSON array, an object with nested values | one value | no `#field` |
| SecretBinary | one value (the bytes) | no `#field` |

A `#field` on a plain-text secret is refused, and so is a key/value secret
without one. The error lists the secret's field names, never their values.

**SSM:** the parameter name, with its leading `/`. A parameter is always one
value, so a `#field` is refused when the connection is saved.

```
secret://ssm-prod//prod/warehouse/password
```

The doubled `/` is the separator followed by the parameter's own leading `/`.

## Versions

`?version=` pins a version.

**Secrets Manager:**

- a UUID is a **VersionId**: `?version=01234567-89ab-cdef-0123-456789abcdef`;
- anything else is a **staging label**: `?version=AWSPREVIOUS`, `?version=AWSPENDING`, or a custom label.

Without `?version=`, the read is `AWSCURRENT`. A rotation is picked up by the
next run, with no change in Brokoli.

**SSM:** the parameter's own selector, a version number or a label.
`?version=3` reads `/prod/warehouse/password:3`, and `?version=release`
reads `/prod/warehouse/password:release`.

## IAM permissions

The role needs read access to the secrets it serves and nothing else. Scope
the resources to a path prefix per workspace.

**Secrets Manager:**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "secretsmanager:GetSecretValue",
      "Resource": "arn:aws:secretsmanager:eu-west-1:123456789012:secret:brokoli/analytics/*"
    },
    {
      "Effect": "Allow",
      "Action": "kms:Decrypt",
      "Resource": "arn:aws:kms:eu-west-1:123456789012:key/<the secrets' KMS key id>",
      "Condition": { "StringEquals": { "kms:ViaService": "secretsmanager.eu-west-1.amazonaws.com" } }
    }
  ]
}
```

**SSM:**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "ssm:GetParameter",
      "Resource": "arn:aws:ssm:eu-west-1:123456789012:parameter/brokoli/analytics/*"
    },
    {
      "Effect": "Allow",
      "Action": "kms:Decrypt",
      "Resource": "arn:aws:kms:eu-west-1:123456789012:key/<the parameters' KMS key id>",
      "Condition": { "StringEquals": { "kms:ViaService": "ssm.eu-west-1.amazonaws.com" } }
    }
  ]
}
```

The `kms:Decrypt` statement is needed only for secrets and parameters
encrypted with a customer-managed KMS key. With the default `aws/...` keys,
`GetSecretValue` and `GetParameter` are enough.

## The OIDC trust policy

For `oidc`, the role trusts the issuer of your deployment's tokens. First,
register the issuer in IAM as an OpenID Connect identity provider:

```sh
aws iam create-open-id-connect-provider \
  --url https://oidc.brokoli.example.com \
  --client-id-list sts.amazonaws.com \
  --thumbprint-list <the issuer certificate's SHA-1 thumbprint>
```

Then give the role a trust policy that accepts tokens from it, for this
audience, and, when the token source puts it in the subject, only for this
store:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/oidc.brokoli.example.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "oidc.brokoli.example.com:aud": "sts.amazonaws.com"
        },
        "StringLike": {
          "oidc.brokoli.example.com:sub": "store:<the store's id>*"
        }
      }
    }
  ]
}
```

What the `sub` claim contains depends on the token source:

- **Per-request tokens:** a token source that issues a token per request names
  the store by its immutable ID, which `GET /api/secret-stores/{id}` returns.
  Pin the condition to it, so renaming the store never changes who the role
  trusts.
- **Platform tokens on disk:** with tokens a platform writes to disk
  (`BROKOLI_OIDC_TOKEN_FILES`, such as a Kubernetes projected service-account
  token), the subject is the machine's service account. That token is the
  machine's identity, so it falls under `BROKOLI_SECRET_STORE_AMBIENT` too.
  See [Workload identity](workload-identity.md).

## Errors

| Message contains | Cause |
| --- | --- |
| `secret not found` | No secret or parameter by that name, or no such version, in the store's region. |
| `permission denied (AccessDeniedException: ...)` | The role may not read it; the message carries AWS's own reason. |
| `permission denied: the KMS key could not decrypt the secret` | The role lacks `kms:Decrypt` on the secret's key. |
| `permission denied (InvalidIdentityToken ...)` / `(IDPRejectedClaim ...)` | AWS refused the OIDC token: the issuer, the audience, or a trust-policy condition does not match. |
| `no OIDC token to exchange` | The store uses `oidc` and the deployment issued no token. |
| `ambient identity is disabled on this deployment` | The store uses `ambient` where the operator denies it. |

No error contains a secret's value. AWS's request ID stays in the message for
your own support case.

## Network

Reads of secrets and the `oidc` token exchange go through the deployment's
outbound policy, like every other request a pipeline causes. AWS's public
endpoints need nothing extra.

A VPC interface endpoint is reached through AWS's own private DNS for the
service, which resolves to a private address. Allow that range explicitly:

```sh
BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16
```

## Testing against LocalStack

Both providers are tested against LocalStack's Secrets Manager, SSM and STS,
pinned by digest in `docker-compose.test.yml`:

```sh
docker compose -f docker-compose.test.yml up -d --wait localstack
BROKOLI_TEST_AWS_ENDPOINT=http://127.0.0.1:55545 \
  go test ./pkg/secretstore ./engine -run AWS -v
```

CI runs the same tests in the `Test (secret stores / LocalStack)` job, which
fails if any of them skip. A recording proxy in front of LocalStack checks
the exchange itself, since LocalStack does not enforce IAM:

- `AssumeRoleWithWebIdentity` is called with the token, the role and the run's
  session name;
- the read is signed with the assumed role's credentials, not the machine's.

LocalStack cannot prove IAM policies, KMS permissions or a real trust policy.
Check those against a real account with the store's **Test** button.

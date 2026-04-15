# ADR-004: Plugin runtime lives in OSS, not EE

**Status:** accepted
**Date:** 2026-04-14

## Context

When we started designing the plugin runtime, the first instinct —
born from a session of living in `ee/` for a week — was to put the
plugin manager under `ee/plugins/manager.go` alongside the other
enterprise infrastructure. That instinct was wrong, and the OSS/EE
separation rule in our team memory
(`feedback_oss_ee_separation.md`) caught it before a line of code
was written.

The underlying question is where the split should fall: what part
of the plugin story is OSS, and what part is enterprise?

## Decision

**Plugin infrastructure is 100% OSS.** That includes:

- The protocol definition (`pkg/plugins/protocol.go`)
- The subprocess runner (`pkg/plugins/runner.go`)
- The plugin manager + NodeExecutor integration (`pkg/plugins/manager.go`)
- The CLI (`cmd/plugins.go` — `brokoli plugins list/install/remove/inspect/test`)
- The Python authoring SDK on PyPI (`brokoli-connector-sdk`, future Phase 2)
- The curated community plugin index (future Phase 4)

**Enterprise-only plugin features** live in `ee/plugins/` and layer
*on top of* the OSS runtime without replacing any of it:

- **Sandboxed execution** — running plugin subprocesses inside
  firejail/bubblewrap with seccomp filters
- **Plugin signature verification** — Sigstore/cosign, with an
  allowlist of trusted signing keys
- **Secret injection from vaults** — Vault / AWS Secrets Manager /
  GCP Secret Manager, so plugin configs don't have to store
  credentials in plaintext
- **Private plugin registries** — pointing at a company's internal
  artifactory instead of the public community index
- **Per-org governance** — plugin allowlists/denylists, install
  audit, usage quotas, hooks into the EE audit layer

## Consequences

### Positive

- **Community can contribute.** The entire plugin authoring surface
  — protocol, SDK, runtime, examples — is readable and runnable
  without a license. Connectors that only work for paying customers
  is not a connector ecosystem; it's a demo.
- **The OSS value proposition stays honest.** "Single binary, real
  plugin system, 15+ connectors" is a marketing line that isn't
  true if the connectors are locked behind the paywall.
- **Enterprise still has real differentiation.** Security (sandbox,
  signing, vault) and governance (audit, allowlists, private
  registries) are things enterprise ops teams *actually* want and
  are willing to pay for — and they map naturally onto the
  audit/SSO/RBAC machinery they're already licensing.
- **No feature-flag-shaped conditionals in the core.** The OSS
  runtime is unaware of the EE layer; the EE layer registers itself
  via the same `extensions.NodeExecutor` interface everything else
  uses.

### Negative

- We give up a potential enterprise moat. If we locked the runtime
  itself behind the paywall, we'd have a stronger short-term sales
  hook — but at the cost of the connector ecosystem that justifies
  Brokoli's long-term position.
- The OSS user can technically run any plugin, including
  malicious ones. The responsibility for what they install is
  theirs; the curated community index (Phase 4) mitigates this for
  the "default" path.

### Deferred

- Enterprise plugin features (sandbox, signing, vault, private
  registry, governance) are all deferred to later phases. OSS is
  shipping first; EE extends on top.

## Alternatives considered

- **Plugin runtime in `ee/plugins/`** — rejected. This was the
  initial instinct, immediately overridden by our team policy. Would
  have killed the ecosystem before it started.
- **Protocol in OSS, manager in EE** — considered, rejected. Splits
  the implementation in a way that makes the OSS runtime useless
  without the EE layer, which is effectively the same as putting
  everything in EE.
- **Open plugin API but closed plugin registry** — viable alternate
  design. Under this model the runtime and SDK are OSS, but the
  official plugin index that users install from is EE-gated. We
  rejected it because community plugin ecosystems rely on a public
  place to find and share plugins — `pip install brokoli-connector-X`
  has to work for anyone.

## Follow-ups

- Write `ee/plugins/` ADRs as the EE-side features come online.
  Each one should reference this ADR to explain why it's additive
  rather than replacing the OSS runtime.
- Team memory entry reinforcing this rule already exists as
  `feedback_oss_ee_separation.md` — this ADR is its code-level
  counterpart.

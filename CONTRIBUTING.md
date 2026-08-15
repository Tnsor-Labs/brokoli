# Contributing to Brokoli

Welcome — this is the canonical contributing guide for the Brokoli OSS
core. `brokoli-sdk` and `brokoli-ee` have their own short guides that
link back here for anything shared.

## Where to start

- [`docs/adr/README.md`](./docs/adr/README.md) explains the architecture
  and, importantly, how to propose changing it. Read the ADR for the
  area you're touching before writing code.
- [`RELEASING.md`](./RELEASING.md) explains the release cadence and
  where a given issue sits in it.
- Issues carrying the current milestone are the best place to start —
  they're written with enough context to implement from, and usually
  reference the ADR that motivated them.

## Claiming an issue

Comment on the issue to claim it. Substantial issues (the kind with a
companion ADR) are usually split into a few independently-landable
milestones inside the issue body — pick one at a time rather than trying
to land the whole thing in one PR. If you're not sure where to start on
an issue, ask on the issue itself; that's what it's there for.

## Branches, commits, PRs

- Branch names: `feat/short-description` or `fix/short-description`.
- PR titles follow Conventional Commits (`feat: ...`, `fix: ...`,
  `docs: ...`) — the title becomes the squash-merge commit message, so
  write it as you'd want it to read in `git log`.
- No AI co-authorship on commits, issues, or PRs in this project —
  attribute authorship to yourself.
- Reference the issue the PR closes (`Closes #NN`) so it closes
  automatically on merge.

## What "done" means

- `gofmt` clean, `go vet ./...` clean.
- `go test -race ./...` passing.
- If you touched `ui/`, the Playwright e2e suite (`ui/`'s test script)
  passing too.
- CI green on the PR.

## Code review

There's no required-approval branch protection on this repo today —
that's a deliberate choice for now, not an oversight. In its place:

> Tag a maintainer for a second pair of eyes on anything non-trivial —
> a new node type, a protocol change, or anything touching `engine/`,
> `store/`, or the plugin protocol. Small, obvious fixes can self-merge.

If you're ever unsure whether something counts as non-trivial, tag
someone — that's cheaper than guessing wrong in either direction.

## Keeping docs honest

If your change affects behavior an ADR describes, update that ADR in
the same PR (a Deferred item filled in, a dated `## Update` section
appended) rather than leaving it to drift. See
[`docs/adr/README.md`](./docs/adr/README.md) for the exact convention.

## Local-first workflow

CI exists to confirm what you already verified — not to discover failures.
Before every push:

1. `./preflight.sh` — replicates CI exactly (gofmt unfiltered, race tests,
   UI build, gosec against the baseline via the CI-pinned image,
   govulncheck, licenses) plus two local-only strictness additions
   (`go vet`, `svelte-check`). `PREFLIGHT_FAST=1` skips `npm ci`;
   `PREFLIGHT_COVERAGE=1` adds the CI coverage pass.
2. For UI work, deploy to the evaluation server and review visually
   before pushing.
3. Push only on local green. CI failures after a green preflight are
   treated as preflight bugs — fix the script alongside the code.

Node: CI pins v20. `preflight.sh` switches via nvm when available and
warns otherwise — a build under a different major may pass locally and
fail in CI (or vice versa).

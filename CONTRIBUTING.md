# Contributing to Brokoli

Welcome — this is the canonical contributing guide for the Brokoli OSS
core. `brokoli-sdk` and `brokoli-typescript` have their own short guides
that link back here for anything shared.

## Why contribute here

Brokoli is a data platform that starts as a single ~30MB binary: a
visual editor, parallel DAG execution, data quality checks, real-time
monitoring and a Python SDK, with no infrastructure required beyond the
binary itself. That constraint is the interesting part. Most tools in
this space assume a cluster, a scheduler and a warehouse before they do
anything at all; Brokoli has to earn each of those, and only when the
user actually needs it.

Some concrete reasons this is a good codebase to spend time in:

- **The problems are real ones, not glue.** Recent work here has
  included a spill-capable streaming data path, a polyglot task runtime
  that runs Python, Node and JVM code under one protocol, capability-
  based access control for a distributed data plane, and a pushdown
  compiler that decides when work belongs in the database instead of the
  engine. If you want to write systems code with observable
  consequences, there is a lot of it.
- **You can run the whole thing on a laptop.** No cluster, no cloud
  account, no seat license, no mock environment that behaves differently
  from production. `go build`, then `./brokoli serve`, and you have the
  same binary a user runs.
- **Decisions are written down.** [`docs/adr/`](./docs/adr/) holds the
  architecture decision records — not just what the design is, but what
  was rejected and why. When you disagree with something, there is a
  documented position to argue against, and a documented process for
  changing it. Reviews here argue about the design, not about style.
- **The review culture is technical and specific.** See
  [Engineering values](#engineering-values) below. Nobody will
  rubber-stamp your PR, and nobody will bikeshed it either.
- **Apache 2.0**, so what you write stays available to you and everyone
  else.

You do not need distributed-systems experience to start. A good number
of the most useful contributions have been error messages that finally
said which thing failed, tests that closed a gap someone assumed was
covered, and documentation that stopped a user from guessing.

## Where to start

**New here? Start with the
[good first issues](https://github.com/Tnsor-Labs/brokoli/contribute).**
Those are deliberately kept to work that is self-contained, has the
relevant file and function named in the issue body, and does not require
reading an ADR first. If an issue carries that label and turns out not
to match that description, say so on the issue — that is a bug in our
labelling and we want to know.

Beyond that:

- Issues labelled [`help
  wanted`](https://github.com/Tnsor-Labs/brokoli/issues?q=is%3Aopen+label%3A%22help+wanted%22)
  are work we would genuinely like help with, at any experience level.
  Several are substantial.
- [`docs/adr/README.md`](./docs/adr/README.md) explains the architecture
  and, importantly, how to propose changing it. Read the ADR for the
  area you're touching before writing code.
- [`RELEASING.md`](./RELEASING.md) explains the release cadence and
  where a given issue sits in it.
- Issues carrying the current milestone are written with enough context
  to implement from, and usually reference the ADR that motivated them.

## Getting set up

**Prerequisites**

| Tool | Version | Why |
| --- | --- | --- |
| Go | 1.25 or newer (`go.mod` pins the toolchain to 1.26.6) | the engine, API and CLI |
| Node.js | v20 — the version CI pins | the Svelte UI |
| Docker | any recent version | Postgres and MySQL for the live tests |
| Python 3 | 3.9 or newer | code nodes, the Python task harness, and the SDK |

**Build and run**

The UI must be built before the Go binary. `web/embed.go` embeds
`web/dist` into the binary, and `ui/vite.config.ts` builds directly into
that directory — so building Go first gives you a binary with a stale or
empty UI, which looks like a broken frontend rather than a build-order
mistake.

```bash
git clone https://github.com/Tnsor-Labs/brokoli.git
cd brokoli

# 1. UI first -- vite writes into web/dist, which the binary embeds
cd ui && npm ci && npm run build && cd ..

# 2. then the binary -- the main package is the repo root, not cmd/
CGO_ENABLED=0 go build -o brokoli .

# 3. run it
./brokoli serve
```

Open `http://localhost:8080` and create the admin account. Data goes
into a local SQLite file by default, so there is nothing else to
configure.

**Working on the UI**

`npm run dev` in `ui/` gives you Vite's dev server with hot reload,
proxying API calls to a `brokoli serve` running separately on 8080. You
do not need to rebuild the Go binary for frontend work — only for a
build that ships.

**Finding your way around**

| Directory | What lives there |
| --- | --- |
| `engine/` | DAG execution, node logic, expansion, retries, the run lifecycle |
| `api/` | HTTP handlers, auth, the REST surface |
| `store/` | persistence; every backend implemented for both SQLite and Postgres |
| `models/` | the pipeline IR and its schema |
| `pkg/` | self-contained libraries: `netguard`, `codeexec`, `taskharness`, `artifact`, `datacap`, `plugins`, and more |
| `ui/` | the Svelte editor and monitoring UI |
| `docs/adr/` | architecture decision records — read before changing a design |
| `docs/schema/` | canonical JSON schemas and their fixtures |
| `cmd/` | the CLI |

## Claiming an issue

Comment on the issue to claim it. Substantial issues (the kind with a
companion ADR) are usually split into a few independently-landable
milestones inside the issue body — pick one at a time rather than trying
to land the whole thing in one PR. If you're not sure where to start on
an issue, ask on the issue itself; that's what it's there for.

Asking a question on an issue is always welcome, including "is this
still accurate?" — issues do drift, and we would rather correct one than
have you build against a stale description.

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

## Engineering values

These are the things review actually asks about. They are written down
because they are more specific than "write good code", and because
knowing them in advance makes a first PR go much more smoothly.

- **Fix the cause, not the symptom.** A workaround that makes a test
  pass while the underlying defect survives is worse than the failing
  test, because it removes the signal. If the real fix is too large for
  your PR, say so in the PR and open an issue for it.
- **A check that cannot fail is not a check.** This project has been
  bitten repeatedly by gates that passed because they inspected nothing
  — a license scan that checked zero dependencies, a security baseline
  that could not tell a clean scan from no scan at all, a CI job that
  skipped its own tests silently. When you add a guard, verify it fails
  when it should, not only that it passes.
- **Validate a guard in both directions.** Prove the good input is
  accepted *and* the bad input is rejected. One half alone is how a
  permanently-open gate gets merged.
- **Live tests catch what CI does not.** Some questions only a real
  server can answer: what a string literal means, how a driver converts
  a value, whether a collation orders the way Go does. See
  [below](#tests-that-need-a-real-database).
- **Measure, don't assume.** Benchmark against the real thing. Isolated
  microbenchmarks and careful code reading have both given confidently
  wrong answers here.
- **Errors should name the thing that failed.** "Deadline exceeded" that
  cannot say which of two deadlines fired is a bug worth fixing, not a
  cosmetic complaint.

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
   govulncheck, licenses, the netguard outbound-client check) plus two
   local-only strictness additions (`go vet`, `svelte-check`).
   `PREFLIGHT_FAST=1` skips `npm ci`; `PREFLIGHT_COVERAGE=1` adds the CI
   coverage pass.
2. For UI work, deploy to the evaluation server and review visually
   before pushing.
3. Push only on local green. CI failures after a green preflight are
   treated as preflight bugs — fix the script alongside the code.

Node: CI pins v20. `preflight.sh` switches via nvm when available and
warns otherwise — a build under a different major may pass locally and
fail in CI (or vice versa).

### Tests that need a real database

Some tests answer questions only a server can: what a string literal means,
how a driver converts a value, whether a collation orders the way Go does.
A fake would define exactly those away, so they run against Postgres and
MySQL and skip when neither is configured.

```bash
docker compose -f docker-compose.test.yml up -d
export BROKOLI_TEST_POSTGRES_URL='postgres://brokoli:brokoli@localhost:55532/brokoli_test?sslmode=disable'
export BROKOLI_TEST_MYSQL_URL='mysql://brokoli:br@k:li/pw#1@tcp(localhost:55533)/brokoli_test'
```

`preflight.sh` does this for you when the variables are unset and docker
compose is available, and says so loudly when it cannot.

A skipped test looks identical to a passing one in the summary line. If you
are changing anything that touches SQL generation, value encoding, or the
pushdown compiler, check the output actually says `PASS` for the live tests
rather than assuming — the differential tests are the only thing standing
between a plausible change and a silently wrong one.

## Code of conduct and licence

Be straightforward and civil; argue about the work, not the person.
Contributions are made under the [Apache 2.0 licence](./LICENSE) that
covers the project.

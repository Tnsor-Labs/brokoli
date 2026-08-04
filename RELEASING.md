# Release process

Brokoli ships on a fixed **4-week cycle**, loosely modeled on the Linux
kernel's merge-window/rc/release rhythm, scaled down for a project this
size. The goal isn't process for its own sake — it's a predictable rhythm
so contributors know when a change needs to land, and so "what's in the
next release" is always answerable by looking at a milestone, not by
asking someone.

## The cycle

| Phase | Length | What happens |
|---|---|---|
| **Open development** | 2 weeks | PRs merge freely against issues carrying the current milestone. |
| **Scope lock** | instant, end of week 2 | The milestone's issue list freezes. "In scope" means actively in flight (an open PR, or clearly claimed with visible progress) — not necessarily merged yet. |
| **RC / stabilization** | 1 week | Tag `vX.Y.0-rc1`. Bugfixes only from here — no new features, no scope additions. |
| **Soak** | 1 week | Run the rc build, watch `/metrics` (leader status, execution-attempt counters) and `run_events` for anything the rc week didn't catch. This is intentionally lightweight for a small team — it'll get more formal as the project and the team grow. |
| **Release** | tag day | Tag `vX.Y.0`. `.github/workflows/release.yml` (GoReleaser) builds and publishes automatically. Move `CHANGELOG.md`'s `[Unreleased]` section under the new version heading. |

A cycle is 4 weeks end to end; the next one's open-development phase
starts the same day the previous release tags.

## If an issue doesn't make scope lock

It slips — quietly, not as a judgment on the work. The maintainer removes
the milestone label and leaves one comment:

> Moving this to `vX.(Y+1).0` — didn't make scope lock, no reflection on
> the work, just cadence.

It goes back on as soon as the next milestone exists. Nothing gets closed
for slipping.

## RC bugs

If stabilization week turns up a real bug, whether that costs a fresh
week of rc/soak or just a respin is a maintainer call, made per-bug based
on severity — not a fixed rule. For a team this size, a hard "any rc bug
restarts the whole clock" rule would grind releases to a halt over minor
issues.

## Per-repo version tracks

Each repo tags independently — there's no single "Brokoli vX.Y.0" that
spans all three:

- **`brokoli`** (this repo) tags real semver releases (`v0.10.7` as of
  this writing) and is the only repo GoReleaser publishes automatically
  for.
- **`brokoli-sdk`** has shipped zero tags so far. Its first tag happens
  the cycle it needs one — most likely once the SDK/backend
  capability-negotiation work lands.
- **`brokoli-ee`** keeps its own separate, currently-behind tag line and
  its own `deploy.yml`.

Because of that, `brokoli-sdk` and `brokoli-ee` milestones are named by
cycle (e.g. `2026-08 cycle`) rather than a version number that would
misleadingly imply they track `brokoli`'s semver — the *cadence* is
shared across the three repos, the version number is not.

## Where to look

- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — how to claim an issue, PR
  conventions, what "done" means.
- [`docs/adr/README.md`](./docs/adr/README.md) — how architecture
  decisions get made and how to weigh in on one.
- `CHANGELOG.md` — what's actually shipped, release by release.

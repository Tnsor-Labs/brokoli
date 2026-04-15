# ADR-007: Install UX — one-liner with interactive admin setup

**Status:** accepted
**Date:** 2026-04-14

## Context

The original install docs had a decision-paralysis problem: five
tarball URLs (one per platform variant), a Docker path, a
`go install` path, a build-from-source path, and a subsequent
manual admin-user creation step. New users had to read ~250 lines
of install docs and make five decisions before seeing Brokoli run.
That's too many steps between "I heard about this" and "wow it
works."

The industry baseline for dev tools in 2026 is the single-command
installer — rustup, bun, deno, Homebrew, Claude Code. All of them
detect platform, fetch the right binary, install it, and print next
steps. Brokoli wasn't doing that.

## Decision

Ship a one-liner installer at `https://docs.brokoli.orkestri.site/install.sh`
that:

1. **Detects OS + arch** via `uname -sm`, picks the matching
   goreleaser asset (Linux/Darwin × x86_64/arm64).
2. **Resolves the latest release tag** via GitHub's redirect for
   `/releases/latest` — no GitHub API call, no rate limit.
3. **Downloads, verifies, extracts, and installs** to
   `/usr/local/bin/brokoli` if the user has sudo, or
   `$HOME/.local/bin/brokoli` if not (with a PATH-add hint).
4. **Optionally starts the server and creates an admin user** via
   the `/dev/tty` trick that rustup / homebrew / bun all use: even
   when run via `curl | sh` (where stdin is piped and can't be
   read from), `/dev/tty` is still the real terminal and can take
   interactive input. The installer prompts for a username and a
   password (hidden via `stty -echo`, enforces the server's 10+
   char minimum), starts the server in the background, waits for
   `/health`, POSTs to `/api/auth/users` to create the first
   admin, prints the URL + credentials.
5. **Supports unattended mode** via environment variables:
   `BROKOLI_VERSION`, `BROKOLI_INSTALL_DIR`, `BROKOLI_NO_SETUP`,
   `BROKOLI_YES`, `BROKOLI_PORT`.

The one-liner is:

```
curl -fsSL https://docs.brokoli.orkestri.site/install.sh | sh
```

The script is written in POSIX sh (no bashisms), works with either
curl or wget (first one found), uses only `tput` for colors (falls
back cleanly when stdout isn't a TTY), and is tracked in the OSS
repo at `install.sh` for review.

## Consequences

### Positive

- **Install → first pipeline is ~3 minutes** instead of ~15. Direct
  improvement to the acquisition funnel.
- **No install-methods decision paralysis.** There's still a full
  reference page at `/deployment/install-methods` covering
  tarballs, Docker, `go install`, build-from-source — but it's not
  on the critical path for first-time users.
- **The script is tracked** in the OSS repo, so users can read it
  before running it, and the hosted copy at
  `docs.brokoli.orkestri.site/install.sh` is a rebuild away from
  being freshly copied.
- **`/dev/tty` interactive prompt** means the admin password setup
  flow isn't broken by the `curl | sh` pipe — a first-class UX
  detail most one-liner installers get wrong.

### Negative

- **`curl | sh` has reputation issues.** Some users (rightly) don't
  pipe arbitrary scripts into their shell. We document the
  inspect-before-running alternate in the docs, and the hosted
  script is short (~260 lines, auditable in one sitting).
- **TLS dependency.** The installer is currently hosted at
  `docs.brokoli.orkestri.site` (not the more natural
  `brokoli.orkestri.site`) because the landing vhost doesn't have
  TLS yet. Documented in the ADR's follow-up list.
- **Environment variable sprawl.** Five `BROKOLI_*` env vars to
  cover the reasonable customization points. Documented in the
  CLI `--help` output of the script and in the install-methods
  docs page.

### Deferred

- **`install.ps1`** for native Windows. Windows users currently go
  through WSL. Deferred per the Windows compatibility analysis.
- **Move install.sh back to `brokoli.orkestri.site`** (the product
  landing domain) once certbot lands on that vhost. Single-line
  docs update when it happens.

## Alternatives considered

- **Keep the manual install docs** — rejected. Decision paralysis.
- **Use `go install` as the canonical path** — rejected. Requires
  Go toolchain installed, which is not a reasonable ask for a
  first-run user.
- **Homebrew / apt / yum / winget** — deferred. These require
  maintaining a package in each repo. Good to have, not a Phase 1
  priority.
- **An `install` subcommand built into an existing package manager
  like pip** — rejected. Brokoli is a Go binary, not a Python
  package (though the SDK is).
- **A custom domain `brokoli.sh`** to get the shortest possible
  one-liner — deferred. Not worth the DNS/TLS overhead yet.

## Follow-ups

- Move the install script from `docs.brokoli.orkestri.site/install.sh`
  to `brokoli.orkestri.site/install.sh` once certbot is wired for
  the landing vhost. Update every doc reference in one sweep.
- Package managers (brew, apt, yum, winget, chocolatey) when
  there's a community ready to maintain them.
- `install.ps1` when Windows-native support is a real priority.

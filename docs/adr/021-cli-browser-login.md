# ADR-021: Browser-based login for the CLI and SDK (`brokoli auth login`)

**Status:** proposed
**Date:** 2026-08-19

## Context

Every credentialed path into the SDK today requires the caller to already
hold a secret: `Client(api_key=...)`, `BROKOLI_TOKEN`, or a `brokoli.yaml`
`token_env` pointing at one. Getting that first secret means either (a)
`username`/`password` in plain text on the command line or in a script,
or (b) `brokoli generate-key` + `serve --api-key` — an operator-only path
that only ever authenticates the single process that started the server
(see ADR fix in `fix/static-api-key-auth`, which made that path work at
all; it still requires the operator to hand-copy a key out of band).
There is no equivalent of `gh auth login`, `wrangler login`, or `vercel
login`: open a browser, log in the way the web UI already supports, and
have the CLI walk away with a real credential — no copy-pasting a secret
between a browser tab and a terminal.

This gap is more visible now than it was: SDK v0.5.0 added `AsyncClient`
and `live_pipeline()`, both aimed at making the SDK a first-class way to
operate against the hosted platform (`https://in-brokoli.orkestri.site`,
now the default `server` — see the `Client`/`AsyncClient`/CLI default
change landing alongside this ADR), not just self-hosted scripts. A
credential-acquisition story that starts with "paste your password into
a shell command" doesn't fit that positioning.

The pieces this can be built from already exist, just aimed at the
browser-only web session, not a CLI process:

- `POST /api/auth/login` (core, `api/users.go`) — username/password,
  returns a JWT. This already works standalone; a login *page* is a
  thin wrapper over it, not new server logic.
- `ee/platform/handlers_oauth.go` (Enterprise-only) — a complete
  state-cookie-based OAuth flow for GitHub, Google, and Keycloak,
  ending in `redirectWithSession`, which sets an httpOnly
  `brokoli_session` cookie and redirects the browser into the web app.
  The state machinery (random state, 10-minute expiry, constant-time
  compare, redirect-URL origin pinning) is exactly the shape a CLI-login
  state needs — it just currently never hands the resulting JWT to
  anything other than a cookie in that same browser.

Neither path gets a token into a **separate CLI process** waiting on a
different machine or terminal. That handoff — browser session in, JWT
out to an unrelated process — is what's actually missing, and it's core
plumbing: it must work identically whether the browser flow behind it is
core's plain password login or Enterprise's SSO, so it can't live
entirely inside Enterprise.

## Decision

Add a **local-callback browser login flow**, split the way ADR-020 draws
the OSS/Enterprise line: the CLI-facing contract and the core
callback/exchange endpoints live in `brokolisql-go`; Enterprise's
existing OAuth providers plug into the same flow as an alternative login
method on the same login page, unchanged in how they themselves work.

Flow:

1. `brokoli auth login` starts a short-lived HTTP listener on
   `127.0.0.1:<ephemeral port>` and opens the user's default browser to
   `<server>/cli-login?callback=http://127.0.0.1:<port>/callback&state=<random>`.
2. The server serves a login page at `/cli-login` — the existing
   username/password form, plus whatever OAuth providers
   `GET /api/auth/methods` reports as configured (unchanged Enterprise
   behavior; core deployments just see password login).
3. On success, instead of (or in addition to) setting the
   `brokoli_session` cookie, the server redirects the browser to the
   callback URL carrying a single-use, short-lived (60s) authorization
   code — not the JWT itself, so it never sits in browser history or a
   redirect log: `http://127.0.0.1:<port>/callback?code=<code>&state=<state>`.
4. The CLI's local listener validates `state`, then makes a direct
   server-to-server call, `POST /api/auth/cli/token {code}`, exchanging
   the code for the real JWT. The browser tab shows "You can close this
   window."
5. The CLI stores the token in `~/.brokoli/credentials.json` (mode
   `0600`), alongside the server URL it's scoped to — extending the
   `~/.brokoli/` convention the plugin loader already uses
   (`~/.brokoli/plugins`).
6. `Client.from_env()` / `AsyncClient.from_env()` and the CLI's
   `_resolve_target` gain a new lowest-priority fallback: if no
   `api_key`/`BROKOLI_TOKEN`/`--api-key` is given, check
   `~/.brokoli/credentials.json` for a token scoped to the resolved
   server before falling back to `DEFAULT_SERVER`'s unauthenticated
   request.

Companion commands, matching `gh auth`'s naming (not a bare
`brokoli login` — see below):

- `brokoli auth login [--server URL]`
- `brokoli auth status` — which identity, which server, whether the
  stored token is still valid (a cheap `GET /api/auth/me`).
- `brokoli auth logout` — deletes the local credential and, best-effort,
  calls `POST /api/auth/logout` to invalidate server-side state if any.

No browser available (SSH session, CI, container): `brokoli auth login`
detects this (no `$DISPLAY`/no `xdg-open`/`open`/`start` success) and
prints the `/cli-login` URL for the user to open on any other device,
then polls `GET /api/auth/cli/status?state=<state>` until the browser
flow completes elsewhere — the same state/code exchange, without the
local listener. This is deliberately not a full RFC 8628 device-code
flow (no separate device code shown/typed) — the state token from step 1
already serves that role, and a second code adds a manual-entry step
users of `gh`/`wrangler` don't expect for the common case.

## Consequences

### Positive

- Closes the actual gap: today there is no way to get a credential into
  the SDK without a plaintext secret changing hands somewhere. This adds
  one, without requiring Enterprise or SSO — plain core password login
  is a complete backend for it.
- Enterprise's SSO providers become usable *from the CLI* for free: they
  already render on any login page that calls
  `GET /api/auth/methods`; `/cli-login` is just another such page.
- `~/.brokoli/credentials.json` composability: `Client.from_env()`
  gaining this fallback means scripts and notebooks that already call
  `Client.from_env()` start working after `brokoli auth login` with zero
  code changes — no `api_key=` to thread through.

### Negative

- A new state-store is needed for the code-exchange step
  (`/api/auth/cli/token`), separate from Enterprise's existing
  `OAuthHandler.states` map (that one is keyed to the OAuth
  provider round-trip, not the CLI callback round-trip, and is
  Enterprise-only besides). In-memory + single-replica is fine for a
  60-second-lived code; a distributed deployment needs it in the same
  Redis-backed store as the rest of distributed auth state, not a new
  bespoke mechanism — sizing that store is a follow-up, not decided
  here.
- `~/.brokoli/credentials.json` is a new plaintext-on-disk secret
  surface (mode `0600`, but not encrypted at rest) — consistent with
  how `gh`/`wrangler`/`vercel` all do it, but worth stating plainly
  rather than assuming.
- The CLI's local HTTP listener is a new local attack surface (any local
  process can hit `127.0.0.1:<port>/callback` while the flow is open).
  Mitigated by the state token and the code's 60-second expiry, but this
  needs a real security review before Status moves to accepted, not an
  assumption that "it's how everyone else does it" is sufficient on its
  own.

### Deferred

- Token refresh / long-lived sessions: the JWT from this flow has the
  same `exp` as password login (24h, `GenerateToken`). Whether
  `brokoli auth login` should also store a refresh mechanism, or whether
  users are expected to re-run it daily, isn't decided here.
- Revoking a specific device's stored credential from the web UI (an
  Enterprise "Connected CLIs" settings page) — plausible, not designed.
- The distributed state-store choice for `/api/auth/cli/token` codes
  (see Negative, above).

## Alternatives considered

- **Full RFC 8628 device-code flow** (a separate short code the user
  types in manually, like `docker login` / `gh auth login --web` in
  some modes) — more standard, but adds a manual-entry step for the
  common case (browser available on the same machine) where the local
  callback listener needs zero manual entry at all. Kept as the
  no-browser fallback instead of the default.
- **Hand a long-lived API key back instead of a JWT** — would let the
  stored credential outlive the 24h JWT expiry, but static keys are the
  thing this whole ADR exists to avoid making people manage by hand
  again; revisit if Deferred's refresh-token question lands on "no,
  don't re-prompt daily."
- **Put this entirely in Enterprise**, since the OAuth machinery it
  extends already lives there — rejected because the useful case (core,
  password-only, hosted platform) has no Enterprise dependency at all,
  and the SDK must work identically against both editions.

## Follow-ups

- Security review of the local-listener/state/code exchange before this
  moves to accepted.
- Implementation: `POST /api/auth/cli/token`, `GET /api/auth/cli/status`,
  and the `/cli-login` page (core); `brokoli auth login/status/logout`
  and the `~/.brokoli/credentials.json` fallback in `Client.from_env()`
  (SDK, `brokoli-sdk#57` or a new tracking issue).
- Decide the distributed code-store mechanism (see Consequences →
  Negative) before a Kubernetes/multi-replica Enterprise deployment
  relies on this.

# ADR-021: Session and token model — revocable sessions, refresh tokens, and browser-based CLI login (`brokoli auth login`)

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

There's a second, independent gap this ADR's first draft under-scoped:
**every login path today — password, OAuth, and the CLI flow this ADR
adds — produces one bare 24h JWT with no way to see it, no way to kill
it, and no way to renew it without re-entering credentials.**
`GenerateToken` (`api/users.go`) signs `exp: now+24h` and that's the
entire lifecycle: no server-side record of the token exists, so a leaked
token is live until it expires on its own, an operator has no page
listing "what's currently logged in as me," and there's no distinction
between a browser tab and a CLI process holding a credential — both are
just an opaque bearer string nothing tracks. Reviewing this ADR's first
draft surfaced that a CLI-specific credential store
(`~/.brokoli/credentials.json`) makes this worse, not better, if it's
built on the same untracked, unrevocable token: now there are *more*
places a leaked 24h credential can live, with the same zero visibility
and zero kill switch. Fixing that for the CLI path alone while leaving
browser sessions on the old model would also make "all my sessions" an
impossible page to build — it needs one session model both surfaces
create rows in, not two.

## Decision

Three decisions, in dependency order: a session/token model every login
path shares (Decision A), the sessions/devices page it makes possible
(Decision B), and the CLI browser-login flow that's a *consumer* of that
model, not a special case of it (Decision C).

### Decision A — access tokens, opaque refresh tokens, and a `sessions` table

Replace the single bare 24h JWT with a standard access/refresh pair,
backed by one server-side `sessions` table that every login path
(password, OAuth, CLI) inserts a row into. This is the part that makes
"see everything logged in as me, kill any one of them" possible at all.

- **Access token**: still a JWT, still `exp: now+24h`, unchanged claims
  (`sub`/`username`/`role`/...) plus a new `sid` (session id) claim.
  Prefixed `brk_` on the wire so it's visually the same shape as a
  static API key — one recognizable token format across the SDK, not
  two. Verified the same way as today (signature + expiry,
  stateless) **plus** one cheap addition: `JWTAuth` checks `sid` against
  the `sessions` table's `revoked_at` before accepting the request. This
  is a single indexed lookup (or a hot in-memory/Redis revocation set,
  distributed deployments use the same store the rest of distributed
  auth state lives in — sizing that is a Follow-up, not decided here),
  not a full token-opaque lookup — the JWT's signature still does the
  expensive verification work; the session check only answers "has this
  been revoked since it was issued," which a pure stateless JWT
  structurally cannot answer before its `exp`.
- **Refresh token**: opaque — a random 32-byte token, `brk_r_` prefixed,
  never a JWT and never decodable. Stored **hashed** (same treatment as
  password hashes; a stolen `sessions` table doesn't hand out live
  refresh tokens) alongside its session row. Sliding expiry, 30 days
  extended on each use, hard cap 90 days absolute — long enough that a
  browser session or a CLI login doesn't demand re-auth constantly, short
  enough that an abandoned session dies on its own.
- **`POST /api/auth/refresh {refresh_token}`**: validates the hash
  against the `sessions` table (not expired, not revoked), then
  **rotates**: issues a new access token *and* a new refresh token,
  marks the old refresh token's row `revoked_at`, and writes the new
  hash into the same session id (the session persists across rotations;
  only the token material changes). The old refresh token is dead the
  moment the new one is issued.
- **Reuse detection**: if an already-rotated-away refresh token is
  presented again, that's not an expired-token error — it's a signal
  the token was copied and is now racing its legitimate holder. Revoke
  the entire session immediately (not just the one refresh token) and
  flag it as revoked-for-reuse (surfaced on the sessions page — see
  Decision B's device page — as "signed out: possible token compromise"
  rather than a silent expiry).
- **`sessions` table**: `id`, `user_id`, `refresh_token_hash`,
  `device_label` (human-readable — "Chrome on macOS", "CLI on
  jdoe-laptop"), `user_agent`, `ip`, `created_at`, `last_used_at`,
  `expires_at`, `revoked_at` (nullable), `revoked_reason` (nullable —
  `"user"` / `"reuse_detected"` / `"expired"`). One row per login, for
  the life of that login (rotation updates the row; it doesn't create a
  new one).
- **Password and OAuth login both move onto this model in the same
  change** — `GenerateToken` stops being the whole story; both
  `POST /api/auth/login` and every `ee/platform/handlers_oauth.go`
  callback create a `sessions` row and return/cookie an access+refresh
  pair instead of a bare JWT. This is required, not optional, for the
  "all sessions" requirement below — a page that only shows CLI logins
  because that's the only path wired up isn't the page being asked for.

### Decision B — sessions/devices management page

`GET /api/auth/sessions` (the caller's own, from their access token's
`sub` — never cross-user) returns every non-expired row from the
`sessions` table: `id`, `device_label`, `ip`, `created_at`,
`last_used_at`, `revoked_at`/`revoked_reason` if applicable, and whether
it's the session making this very request (`is_current`). A web UI
settings page (`/settings/sessions`) renders this as a list — every
browser session and every CLI login side by side, because they're rows
in the same table, not because the UI special-cases CLI entries.

- `DELETE /api/auth/sessions/{id}` — revoke one. Immediate: the access
  token's next request fails the `sid` check (see Decision A) even
  though it hasn't expired, and the refresh token can no longer rotate.
- `DELETE /api/auth/sessions` — revoke every session except the one
  making the request ("sign out everywhere else"), the response to
  "I think something leaked and I don't know which one."
- Revoking a session the CLI is holding doesn't require the CLI to be
  running or reachable — it's a server-side row flip. The CLI just
  fails its next request with 401 and `brokoli auth status` reports it
  plainly instead of silently retrying forever.

### Decision C — the CLI browser-login flow itself

Unchanged in shape from the first draft of this ADR, now producing a
Decision A token pair and a labeled `sessions` row instead of a bare
JWT:

1. `brokoli auth login` starts a short-lived HTTP listener on
   `127.0.0.1:<ephemeral port>` and opens the user's default browser to
   `<server>/cli-login?callback=http://127.0.0.1:<port>/callback&state=<random>`.
2. The server serves a login page at `/cli-login` — the existing
   username/password form, plus whatever OAuth providers
   `GET /api/auth/methods` reports as configured (unchanged Enterprise
   behavior; core deployments just see password login).
3. On success, the server creates a `sessions` row labeled from the
   login context (hostname the CLI reports in its initial request, e.g.
   `"CLI — jdoe-laptop (macOS)"`) and redirects the browser to the
   callback URL carrying a single-use, short-lived (60s) authorization
   code — not the token pair itself, so neither ever sits in browser
   history or a redirect log: `http://127.0.0.1:<port>/callback?code=<code>&state=<state>`.
4. The CLI's local listener validates `state`, then makes a direct
   server-to-server call, `POST /api/auth/cli/token {code}`, exchanging
   the code for the real access+refresh pair. The browser tab shows "You
   can close this window."
5. The CLI stores both tokens in `~/.brokoli/credentials.json` (mode
   `0600`), alongside the server URL and session id — extending the
   `~/.brokoli/` convention the plugin loader already uses
   (`~/.brokoli/plugins`).
6. `Client`/`AsyncClient` transparently call `POST /api/auth/refresh`
   when the stored access token is expired or about to be, write the
   rotated pair back to `~/.brokoli/credentials.json`, and retry —
   invisible to the caller of `client.run(...)` unless the refresh
   itself fails (session revoked), which surfaces as `AuthError` same as
   today's 401-on-expired-static-key case.
7. `Client.from_env()` / `AsyncClient.from_env()` and the CLI's
   `_resolve_target` gain a new lowest-priority fallback: if no
   `api_key`/`BROKOLI_TOKEN`/`--api-key` is given, check
   `~/.brokoli/credentials.json` for a token scoped to the resolved
   server before falling back to `DEFAULT_SERVER`'s unauthenticated
   request.

Companion commands, matching `gh auth`'s naming (not a bare
`brokoli login` — see below):

- `brokoli auth login [--server URL]`
- `brokoli auth status` — which identity, which server, whether the
  stored session is still valid (a cheap `GET /api/auth/me`, which now
  also reports if the session was server-side revoked rather than just
  failing opaquely).
- `brokoli auth logout` — calls `DELETE /api/auth/sessions/{id}` for its
  *own* session id (a real server-side revoke, not just forgetting the
  local file) and then deletes `~/.brokoli/credentials.json`.

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

- **A leaked token is now containable.** Today, nothing short of
  waiting out a 24h expiry stops a leaked JWT — there's no revoke path
  at all. `DELETE /api/auth/sessions/{id}` (or "sign out everywhere
  else") makes a leak a solved problem instead of a countdown.
- **Real visibility.** "What's currently authenticated as me" is an
  actual, answerable question for the first time — one page, every
  browser session and every CLI login, because they write to the same
  table instead of two different (or no) mechanisms.
- **Reuse detection turns a stolen refresh token into a loud signal**
  instead of a silent extra login living alongside the legitimate one —
  rotation means only one copy of a refresh token is ever valid at a
  time, so a copy racing the original is detected, not just tolerated.
- Closes the actual CLI gap too: today there is no way to get a
  credential into the SDK without a plaintext secret changing hands
  somewhere. `brokoli auth login` adds one, without requiring Enterprise
  or SSO — plain core password login is a complete backend for it.
- Enterprise's SSO providers become usable *from the CLI* for free: they
  already render on any login page that calls
  `GET /api/auth/methods`; `/cli-login` is just another such page, and
  every OAuth callback now creates the same kind of revocable session a
  password or CLI login does.
- `~/.brokoli/credentials.json` composability: `Client.from_env()`
  gaining this fallback, plus transparent refresh, means scripts and
  notebooks that already call `Client.from_env()` start working after
  `brokoli auth login` with zero code changes and don't break at the
  24h mark either.

### Negative

- **Real added complexity**: two token types instead of one, a
  `sessions` table every login path must write to correctly, rotation
  logic, and reuse-detection edge cases (what if two requests race to
  refresh the same token concurrently — legitimate double-tab use, not
  theft? needs a grace window, not a hair-trigger revoke — a design
  detail for implementation, not decided here). This is a materially
  bigger lift than the original bare-JWT CLI flow; worth being honest
  that "higher standards" costs real engineering, not just a design
  paragraph.
- The `sid` revocation check on every `JWTAuth` request trades pure
  stateless JWT verification for one cheap-but-nonzero lookup per
  request. Acceptable (this is exactly the "real revocability" the
  positive side asks for), but it's a deliberate move away from "JWTs
  don't need a database hit," worth naming explicitly rather than
  letting it slide in unexamined.
- A new state-store is needed for the CLI code-exchange step
  (`/api/auth/cli/token`), separate from Enterprise's existing
  `OAuthHandler.states` map (that one is keyed to the OAuth
  provider round-trip, not the CLI callback round-trip, and is
  Enterprise-only besides). In-memory + single-replica is fine for a
  60-second-lived code; a distributed deployment needs it in the same
  store the `sessions` table and the rest of distributed auth state
  live in — sizing that is a Follow-up, not decided here.
- `~/.brokoli/credentials.json` is a new plaintext-on-disk secret
  surface (mode `0600`, but not encrypted at rest) — consistent with
  how `gh`/`wrangler`/`vercel` all do it, but worth stating plainly
  rather than assuming. The refresh token stored there is exactly as
  sensitive as the access token now (it can mint new access tokens
  indefinitely until revoked or it hits its 90-day cap) — losing the
  file is a real credential leak, not a 24h-bounded inconvenience,
  which is precisely why Decision B's revoke path exists.
- The CLI's local HTTP listener is a new local attack surface (any local
  process can hit `127.0.0.1:<port>/callback` while the flow is open).
  Mitigated by the state token and the code's 60-second expiry, but this
  needs a real security review before Status moves to accepted, not an
  assumption that "it's how everyone else does it" is sufficient on its
  own.

### Deferred

- New-login notification (email/webhook on a new session being
  created) — valuable, not designed here.
- Anomaly signals beyond reuse detection (impossible-travel IP jumps,
  new-device fingerprinting) — plausible follow-on, out of scope for
  the baseline model this ADR establishes.
- The distributed state-store choice for `/api/auth/cli/token` codes and
  the `sid` revocation check (see Negative, above).
- Whether `brokoli auth login`'s device label should be user-editable
  from the sessions page (renaming "CLI on jdoe-laptop" to something
  clearer) — cosmetic, not blocking.

## Alternatives considered

- **Leave the bare 24h JWT model alone; only add the CLI flow** — the
  first draft of this ADR. Rejected on review: it makes the credential
  surface area worse (now the JWT can also live in
  `~/.brokoli/credentials.json`) without adding the one thing that
  actually matters once you're storing more copies of a bearer token in
  more places — a kill switch.
- **Short access-token expiry, no refresh tokens** (e.g. 15 minutes,
  force re-login after) — would bound a leak's blast radius without any
  new token type, but forces re-authentication constantly for the exact
  workflows (long-running scripts, notebooks, CI) this SDK exists to
  support, and still doesn't produce a sessions page — a page needs
  something durable to list.
- **Full RFC 8628 device-code flow** (a separate short code the user
  types in manually, like `docker login` / `gh auth login --web` in
  some modes) — more standard, but adds a manual-entry step for the
  common case (browser available on the same machine) where the local
  callback listener needs zero manual entry at all. Kept as the
  no-browser fallback instead of the default.
- **Put this entirely in Enterprise**, since the OAuth machinery it
  extends already lives there — rejected because the useful case (core,
  password-only, hosted platform) has no Enterprise dependency at all,
  and the SDK must work identically against both editions. The
  `sessions` table and revocation model are core plumbing for the same
  reason.

## Follow-ups

- Security review of the local-listener/state/code exchange, the
  refresh-token rotation/reuse-detection logic, and the `sid`
  revocation check, before this moves to accepted.
- Implementation, roughly in dependency order:
  1. `sessions` table + `POST /api/auth/refresh` + `sid` claim/check
     (Decision A) — migrate `POST /api/auth/login` and every
     `ee/platform/handlers_oauth.go` callback onto it.
  2. `GET/DELETE /api/auth/sessions` + the `/settings/sessions` web UI
     page (Decision B).
  3. `POST /api/auth/cli/token`, `GET /api/auth/cli/status`, and the
     `/cli-login` page (core); `brokoli auth login/status/logout`,
     transparent refresh, and the `~/.brokoli/credentials.json` fallback
     in `Client.from_env()` (SDK, `brokoli-sdk#57` or a new tracking
     issue) (Decision C).
- Decide the distributed code/session store mechanism (see Consequences
  → Negative) before a Kubernetes/multi-replica Enterprise deployment
  relies on this.
- Decide the concurrent-refresh race behavior (see Consequences →
  Negative) before implementing rotation.

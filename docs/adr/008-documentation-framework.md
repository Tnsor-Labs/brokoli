# ADR-008: Documentation framework — fumadocs over MkDocs

**Status:** accepted
**Date:** 2026-04-14

## Context

The project previously had two documentation trees side-by-side:

1. `docs/` — MkDocs + Material theme (Python). Served at a stale
   `docs.brokoli.dev` URL that no longer resolved.
2. `docs-new/` — Next.js 15 + fumadocs-mdx. No live URL.

Neither was the canonical source of truth. Contributors edited one
or the other based on personal preference, and the two had drifted
to the point of contradicting each other.

We need to pick one, delete the other, and document the choice so
we don't end up with three doc sites.

## Decision

**Canonical docs are `docs-new/` — Next.js 15 + fumadocs-mdx.**
The `docs/` MkDocs tree will be deleted. Docs are served at
`https://docs.brokoli.orkestri.site` via a plain nginx static
deployment (certbot-managed TLS).

## Consequences

### Positive

- **Modern MDX authoring.** Docs pages can embed React components,
  typed code samples, interactive examples — things MkDocs can't
  do natively.
- **Static export.** `next build` produces a plain static site
  that drops into any nginx / S3 / Netlify / Cloudflare Pages
  deploy. No server-side rendering at runtime, so hosting is
  trivial and there's nothing to break in prod.
- **Search is built in via fumadocs.**
- **Authoring ergonomics.** MDX with front-matter works with any
  editor, and `meta.json` for navigation is explicit in ways
  MkDocs' nested YAML isn't.

### Negative

- **Node.js dependency for contributors** who want to preview
  changes locally. MkDocs only needed Python. Contributors without
  Node installed either rely on CI previews or accept that they
  can't render the site locally.
- **Bundle size.** A fumadocs site build is a few MB of JS chunks
  shipped to the browser. Acceptable for a static docs site with
  an expected audience of a few thousand readers.
- **TypeScript footprint.** `docs-new/` has its own `tsconfig`,
  `node_modules`, and build toolchain. Adds surface area to CI.

### Deferred

- Delete the old `docs/` tree. For now it's kept as a historical
  reference (and because it's still git-tracked); a follow-up
  commit will `rm -rf` it once we're confident nothing links at
  the stale URLs from outside our control.

## Alternatives considered

- **Keep MkDocs** — rejected. Can't render MDX / React; search
  is weaker; the Material theme is fine but not aesthetically
  competitive with modern docs frameworks.
- **Docusaurus** — considered and rejected. Similar capabilities
  to fumadocs but heavier and more opinionated about its
  directory structure.
- **Astro + Starlight** — considered. Good choice, smaller
  footprint than Next, but the team already had fumadocs bootstrap
  in `docs-new/`, which tipped the scale.
- **Plain markdown + GitHub Pages** — rejected. Acceptable for a
  tiny project; too limiting for one with ~40 pages of reference
  docs + SDK docs + ADRs.

## Follow-ups

- Delete `docs/` in a follow-up commit once we've grep'd for
  external links to the stale URLs.
- Move `install.sh` from `docs.brokoli.orkestri.site` to
  `brokoli.orkestri.site` when certbot is wired for the landing
  vhost (see ADR-007 follow-ups).
- Put the ADR series under `docs-new/content/docs/adr/` as well as
  in `docs/adr/` in each repo, so they're reachable from the
  public docs site too.

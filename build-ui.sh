#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# Core UI build selector
#
# Fills web/dist (the //go:embed target) from one of two UIs:
#
#   BROKOLI_UI=svelte  (default)  build the Svelte app in ui/
#   BROKOLI_UI=react              build the React community app in ui-react/
#
# The embed target is identical either way, so switching UIs is a
# build-step swap, not a code change. Svelte remains the default until the
# React migration flips it. The archived Svelte UI is tagged
# ui-svelte-final.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
UI="${BROKOLI_UI:-svelte}"
DIST="$ROOT/web/dist"

case "$UI" in
  svelte)
    echo "→ Building Svelte UI (ui/) into web/dist"
    cd "$ROOT/ui"
    npm ci
    npm run build            # vite outDir is ../web/dist
    ;;
  react)
    echo "→ Building React community UI (ui-react/) into web/dist"
    cd "$ROOT/ui-react"
    npm ci
    npm run check            # typecheck, tests, colors, build, boundaries
    # The community app builds to its own dist; copy it onto the embed
    # target so the app stays unaware of core's layout.
    rm -rf "$DIST"
    mkdir -p "$DIST"
    cp -r "$ROOT/ui-react/apps/community/dist/." "$DIST/"
    ;;
  *)
    echo "ERROR: BROKOLI_UI must be 'svelte' or 'react' (got '$UI')" >&2
    exit 1
    ;;
esac

echo "✓ web/dist populated from the $UI UI"

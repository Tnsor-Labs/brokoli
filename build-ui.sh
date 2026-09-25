#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# Core UI build
#
# Builds the React community app in ui/ and copies it onto web/dist, the
# //go:embed target. The community app builds to its own dist and stays
# unaware of the repo layout; this script does the placement.
#
# This produces the embed artifact only. The full UI gate (typecheck,
# tests, colour tokens, boundary check) runs as its own CI job, not here,
# so the server build stays fast.
#
# The previous Svelte UI is archived at the tag ui-svelte-final; restore it
# by checking that out.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
DIST="$ROOT/web/dist"

echo "→ Building React community UI (ui/) into web/dist"
cd "$ROOT/ui"
npm ci
npm run build            # builds @brokoli/community to ui/apps/community/dist

rm -rf "$DIST"
mkdir -p "$DIST"
cp -r "$ROOT/ui/apps/community/dist/." "$DIST/"

# An index.html on its own is not a UI: the bundle has to carry the assets
# that page asks for. This is the check the committed placeholder would
# have failed for as long as it existed.
bash "$ROOT/scripts/check-ui-bundle.sh" "$DIST" || exit 1

# web/dist is otherwise gitignored, and .gitkeep is the one tracked file
# in it, there so //go:embed all:dist compiles in a checkout that has not
# built the UI. The rm -rf above takes it with the rest, so put it back:
# without this, every build leaves the tree dirty with a deleted file.
touch "$DIST/.gitkeep"

echo "✓ web/dist populated from the React community UI"

#!/usr/bin/env bash
# Verify that a built enterprise UI bundle is internally consistent.
#
# An index.html on its own is not a UI. Before this existed, a built
# index.html was committed to web/dist while .gitignore excluded
# web/dist/assets/, so the published module carried a page asking the
# browser for two files that were not in it. Anything built from source
# started, logged "Serving embedded UI", and served a blank screen.
#
# Usage: bash scripts/check-ui-bundle.sh <dist-dir>
set -uo pipefail

DIST=${1:?usage: check-ui-bundle.sh <dist-dir>}

if [ ! -f "$DIST/index.html" ]; then
  echo "ERROR: $DIST has no index.html" >&2
  exit 1
fi

refs=$(grep -oE '(src|href)="/[^"]+"' "$DIST/index.html" | sed -E 's/.*"(\/[^"]+)"/\1/')

# No references at all is not a pass. A built bundle always asks for its
# script and stylesheet, so an empty list means the page is not a build
# output or the pattern above stopped matching -- either way this check
# would be answering a question it never actually asked.
if [ -z "$refs" ]; then
  echo "ERROR: $DIST/index.html references no assets at all; a built bundle always does" >&2
  exit 1
fi

missing=0
while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  if [ ! -f "$DIST/${ref#/}" ]; then
    echo "ERROR: index.html references $ref, which is not in the bundle" >&2
    missing=$((missing + 1))
  fi
done <<REFS
$refs
REFS

if [ "$missing" -gt 0 ]; then
  echo "ERROR: the bundle is incomplete; $missing referenced asset(s) are missing" >&2
  exit 1
fi

echo "✓ bundle is self-consistent ($(printf '%s\n' "$refs" | grep -c .) assets referenced, all present)"

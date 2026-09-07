#!/usr/bin/env bash
# Forbidden-license gate over the module dependencies actually compiled
# into this repo's binaries.
#
# Why this exists instead of `go-licenses report ./...`: go-licenses
# (v1.6.0 and @latest alike) cannot load a Go 1.24+ standard library --
# every stdlib package trips "does not have module info", and the run
# ends in a fatal error having printed ZERO report lines. Both the old
# preflight step and the old CI step piped that into `|| true` and then
# grepped the empty output for GPL/AGPL, so the gate passed
# unconditionally while inspecting nothing. An empty scan must never
# read as a clean scan, so this script fails when it finds no modules at
# all, and fails when a module has no license file to classify.
#
# Scope is deliberately `go list -deps ./...` (modules whose packages are
# genuinely reachable from this module's own packages), not `go list -m
# all` (the far larger build-list closure, including modules nothing
# imports). Compliance follows what ships, not what the graph mentions.
set -euo pipefail

cd "$(dirname "$0")/.."

# The policy list this repo has always declared. LGPL is deliberately
# absent -- it was absent from the original gate too, and this script
# reimplements that policy rather than quietly tightening it.
#
# A license file's identity is the license it NAMES FIRST. Everything
# after that first name is that license's own body, which is free to
# mention other licenses: MPL-2.0 section 1.12 defines "Secondary
# License" as "the GNU General Public License, Version 2.0, the GNU
# Lesser General Public License, Version 2.1, the GNU Affero General
# Public License, Version 3.0". A whole-file grep therefore reports every
# MPL-2.0 dependency as GPL -- go-sql-driver/mysql, a real dependency
# here, is exactly that case. A fixed line window is no better: it only
# works while the mention happens to fall outside it.
#
# So both sets are matched together and the earliest hit decides. LGPL is
# listed as permitted rather than omitted, both because the declared
# policy has always allowed it and because it must claim its own line
# before the GPL pattern can match the same text.
FORBIDDEN_RE='GNU AFFERO GENERAL PUBLIC LICENSE|GNU GENERAL PUBLIC LICENSE|SERVER SIDE PUBLIC LICENSE|EUROPEAN UNION PUBLIC LIC[EN]NSE'
#
# The permitted set matches grant clauses as well as titles, because the
# BSD family never names itself: golang.org/x/*, google/uuid and
# modernc.org/* all open with a bare copyright line followed by
# "Redistribution and use in source and binary forms". Identifying those
# by name alone leaves 28 of this repo's 88 dependencies unclassified,
# which this script (correctly) treats as a failure.
PERMITTED_RE='GNU LESSER GENERAL PUBLIC LICENSE|MOZILLA PUBLIC LICENSE|APACHE LICENSE|MIT LICENSE|ISC LICENSE|CC0|THE UNLICENSE|BLUE OAK|REDISTRIBUTION AND USE IN SOURCE AND BINARY FORMS|PERMISSION IS HEREBY GRANTED, FREE OF CHARGE|PERMISSION TO USE, COPY, MODIFY, AND/OR DISTRIBUTE'

# BROKOLI_LICENSE_MODULES_FILE injects the "path|dir" list instead of
# resolving it from the build graph. It exists so the gate's own test
# (scripts/check-licenses_test.sh) can run the real classifier over
# fixture license files -- a forbidden-license gate that has never been
# observed to fail is indistinguishable from the vacuous one this
# replaced.
if [ -n "${BROKOLI_LICENSE_MODULES_FILE:-}" ]; then
  mods=$(sort -u "$BROKOLI_LICENSE_MODULES_FILE" | grep . || true)
else
  mods=$(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}' ./... 2>/dev/null | sort -u | grep . || true)
fi

if [ -z "$mods" ]; then
  echo "ERROR: no dependency modules resolved -- the scan inspected nothing." >&2
  echo "This is the failure mode that made the previous gate vacuous; refusing to report success." >&2
  exit 1
fi

violations=0
unknown=0
scanned=0

while IFS='|' read -r path dir; do
  [ -n "$path" ] || continue
  scanned=$((scanned + 1))

  if [ ! -d "$dir" ]; then
    echo "UNKNOWN  $path (module not present in the module cache)"
    unknown=$((unknown + 1))
    continue
  fi

  # -maxdepth 2 catches both LICENSE at a module root and the
  # licenses/COPYING layout some modules use one level down.
  lic=$(find "$dir" -maxdepth 2 \
    \( -iname 'LICENSE' -o -iname 'LICENSE.*' -o -iname 'LICENCE' \
       -o -iname 'LICENCE.*' -o -iname 'COPYING' -o -iname 'COPYING.*' \) \
    -type f 2>/dev/null | sort | head -1)

  if [ -z "$lic" ]; then
    echo "UNKNOWN  $path (no license file found)"
    unknown=$((unknown + 1))
    continue
  fi

  # The first line naming any known license decides. grep -m1 over the
  # union of both patterns finds it in one pass, so "earliest wins" is
  # the matching itself rather than a comparison of line numbers.
  verdict=$(grep -oiE -m1 "$FORBIDDEN_RE|$PERMITTED_RE" "$lic" 2>/dev/null | head -1 || true)

  if [ -z "$verdict" ]; then
    echo "UNKNOWN  $path (license file names no recognized license: $lic)"
    unknown=$((unknown + 1))
  elif echo "$verdict" | grep -qiE "$PERMITTED_RE"; then
    :
  else
    echo "FORBIDDEN  $path -> $lic ($verdict)"
    violations=$((violations + 1))
  fi
done <<< "$mods"

echo "scanned $scanned dependency modules; $violations forbidden, $unknown unclassified"

if [ "$violations" -gt 0 ]; then
  echo "ERROR: forbidden license(s) detected -- GPL/AGPL/SSPL/EUPL dependencies cannot be used." >&2
  exit 1
fi

if [ "$unknown" -gt 0 ]; then
  echo "ERROR: $unknown module(s) could not be classified; an unclassified license is not a permitted one." >&2
  exit 1
fi

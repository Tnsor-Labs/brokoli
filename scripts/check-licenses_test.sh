#!/usr/bin/env bash
# Tests for scripts/check-licenses.sh.
#
# The gate this replaced passed unconditionally because it scanned an
# empty report, so the point of these cases is less "does it accept a
# clean tree" than "can it fail at all, and does it fail for the right
# reason". Each case builds fixture modules with real license header text
# and asserts the exit status and the reported classification.
set -euo pipefail

cd "$(dirname "$0")/.."
SCRIPT=scripts/check-licenses.sh

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail=0

# make_module <name> <license-filename> <header text>
make_module() {
  local name=$1 file=$2 body=$3
  mkdir -p "$WORK/$name"
  printf '%s\n' "$body" > "$WORK/$name/$file"
  echo "example.com/$name|$WORK/$name" >> "$WORK/modules.txt"
}

expect() {
  local label=$1 want_status=$2 want_substr=$3
  local out status
  set +e
  out=$(BROKOLI_LICENSE_MODULES_FILE="$WORK/modules.txt" bash "$SCRIPT" 2>&1)
  status=$?
  set -e
  if [ "$status" -ne "$want_status" ]; then
    echo "FAIL $label: exit $status, want $want_status"
    echo "$out" | sed 's/^/    /'
    fail=1
    return
  fi
  if ! echo "$out" | grep -qF "$want_substr"; then
    echo "FAIL $label: output missing '$want_substr'"
    echo "$out" | sed 's/^/    /'
    fail=1
    return
  fi
  echo "ok   $label"
}

MIT='MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.'

APACHE='                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/'

# MPL-2.0 section 1.12 defines "Secondary License" by naming GPL, LGPL
# and AGPL. This fixture is the regression guard for the false positive
# that a whole-file grep produces: go-sql-driver/mysql, a real
# dependency of this repo, is MPL-2.0 and was reported as GPL.
MPL='Mozilla Public License Version 2.0
==================================

1.12. "Secondary License"
    means either the GNU General Public License, Version 2.0, the GNU
    Lesser General Public License, Version 2.1, the GNU Affero General
    Public License, Version 3.0, or any later versions of those
    licenses.'

GPL='                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007

 Copyright (C) 2007 Free Software Foundation, Inc.'

GPL2='                    GNU GENERAL PUBLIC LICENSE
                       Version 2, June 1991'

AGPL='                    GNU AFFERO GENERAL PUBLIC LICENSE
                       Version 3, 19 November 2007'

# The BSD family names neither "BSD" nor any license title -- a bare
# copyright line, then the grant clause. This is the shape of 28 of this
# repo's 88 dependencies (golang.org/x/*, google/uuid, modernc.org/*).
BSD='Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:'

LGPL='                   GNU LESSER GENERAL PUBLIC LICENSE
                       Version 2.1, February 1999'

SSPL='                   SERVER SIDE PUBLIC LICENSE
                   VERSION 1, OCTOBER 16, 2018'

# --- a clean tree passes, and says how much it actually looked at ---
: > "$WORK/modules.txt"
make_module mit LICENSE "$MIT"
make_module apache LICENSE "$APACHE"
make_module mpl LICENSE "$MPL"
expect "permissive licenses pass" 0 "scanned 3 dependency modules; 0 forbidden, 0 unclassified"

# --- the false positive that motivated header-only matching ---
: > "$WORK/modules.txt"
make_module mpl LICENSE "$MPL"
expect "MPL-2.0 is not read as GPL" 0 "0 forbidden"

# --- each forbidden family is actually caught ---
: > "$WORK/modules.txt"
make_module mit LICENSE "$MIT"
make_module gpl LICENSE "$GPL"
expect "GPL-3.0 is refused" 1 "FORBIDDEN  example.com/gpl"

: > "$WORK/modules.txt"
make_module gpl2 LICENSE "$GPL2"
expect "GPL-2.0 is refused" 1 "FORBIDDEN  example.com/gpl2"

: > "$WORK/modules.txt"
make_module agpl LICENSE "$AGPL"
expect "AGPL-3.0 is refused" 1 "FORBIDDEN  example.com/agpl"

# A BSD-licensed module must still be classified by its grant clause
# alone -- the BSD text never contains the string "BSD".
: > "$WORK/modules.txt"
make_module bsd LICENSE "$BSD"
expect "BSD is recognized without naming itself" 0 "0 forbidden, 0 unclassified"

: > "$WORK/modules.txt"
make_module sspl LICENSE "$SSPL"
expect "SSPL is refused" 1 "FORBIDDEN  example.com/sspl"

# --- LGPL is NOT on the policy list; the GPL pattern must not swallow it ---
: > "$WORK/modules.txt"
make_module lgpl LICENSE "$LGPL"
expect "LGPL is allowed by the declared policy" 0 "0 forbidden"

# --- alternative license filenames are found ---
: > "$WORK/modules.txt"
make_module copying COPYING "$GPL"
expect "COPYING is classified too" 1 "FORBIDDEN  example.com/copying"

: > "$WORK/modules.txt"
make_module licence LICENCE.md "$AGPL"
expect "LICENCE.md is classified too" 1 "FORBIDDEN  example.com/licence"

# --- an unreadable license is a failure, never a pass ---
: > "$WORK/modules.txt"
mkdir -p "$WORK/nolicense"
echo "example.com/nolicense|$WORK/nolicense" >> "$WORK/modules.txt"
expect "a module with no license file fails" 1 "UNKNOWN  example.com/nolicense"

: > "$WORK/modules.txt"
echo "example.com/absent|$WORK/does-not-exist" >> "$WORK/modules.txt"
expect "a module missing from the cache fails" 1 "UNKNOWN  example.com/absent"

# --- the vacuous-scan regression itself ---
: > "$WORK/modules.txt"
expect "an empty scan is a failure, not a clean bill" 1 "inspected nothing"

# --- the wiring, not the classifier ---
#
# Every case above proves the script exits non-zero on a license it must
# refuse. None of them proved the CI step does, and for a while it did
# not: `run: bash scripts/check-licenses.sh | tee /tmp/licenses.txt`
# takes tee's exit code, because GitHub runs a step as `bash -e {0}`
# without pipefail. The gate reported success on a dependency that had
# no license at all.
#
# So the step's own command is extracted from the workflow and run
# against a script that fails. A gate whose wiring has never been
# observed to propagate a failure is indistinguishable from one that
# cannot.
WORKFLOW="$(cd "$(dirname "$0")/.." && pwd)/.github/workflows/security.yml"
step_cmd=$(awk '
  /^      - name: Check for forbidden licenses$/ { found = 1; next }
  found && /^        run: \|$/ { collecting = 1; next }
  collecting && /^          / { sub(/^          /, ""); print; next }
  collecting { exit }
' "$WORKFLOW")

if [ -z "$step_cmd" ]; then
  echo "FAIL license step wiring: could not find the step in $WORKFLOW"
  echo "     (if the step was renamed or reformatted, update this test -- do not delete it)"
  fail=1
else
  STUB_DIR=$(mktemp -d)
  trap 'rm -rf "$STUB_DIR"' EXIT
  mkdir -p "$STUB_DIR/scripts"
  # Stands in for a scan that found something it must refuse.
  printf '#!/bin/sh\necho "FORBIDDEN example.com/gpl"\nexit 1\n' > "$STUB_DIR/scripts/check-licenses.sh"
  chmod +x "$STUB_DIR/scripts/check-licenses.sh"

  # `bash -e -c` and nothing else: a fresh shell with DEFAULT options,
  # which is what GitHub gives a `run:` step (`bash -e {0}`). Running it
  # with eval in this shell would inherit the `set -o pipefail` at the
  # top of this file -- the pipe would then propagate correctly, the
  # test would pass, and it would prove nothing about CI. That mistake
  # was made here first and caught by mutating the workflow.
  set +e
  ( cd "$STUB_DIR" && bash -e -c "$step_cmd" ) >/dev/null 2>&1
  wiring_status=$?
  set -e
  if [ "$wiring_status" -eq 0 ]; then
    echo "FAIL license step wiring: the workflow step returned 0 for a scan that exited 1."
    echo "     The gate cannot fail CI. Restore 'set -o pipefail', or drop the pipe."
    fail=1
  else
    echo "ok   license step wiring propagates a failing scan"
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo "license gate tests FAILED"
  exit 1
fi
echo "license gate tests passed"

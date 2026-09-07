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

if [ "$fail" -ne 0 ]; then
  echo "license gate tests FAILED"
  exit 1
fi
echo "license gate tests passed"

#!/usr/bin/env bash
# Tests for scripts/check-vulns.sh.
#
# The gate this replaces could only ever say "any called vulnerability is
# fatal", so the cases that matter here are the ones that distinguish an
# audited exception from a silent suppression: an unlisted vulnerability
# must fail, a listed one must pass, and a listed one that has since been
# FIXED upstream must fail so the allowlist cannot rot.
#
# A vulnerability gate that has never been observed to fail is
# indistinguishable from one that cannot fail, which is why every case
# asserts an exit status and a specific line of output.
set -euo pipefail

cd "$(dirname "$0")/.."
SCRIPT=scripts/check-vulns.sh

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail=0

# scan <<<json...  builds a govulncheck-shaped stream: a config record
# (without which the script refuses the input as "not a scan result"),
# then whatever records the case needs.
config='{"config":{"scanner_name":"govulncheck","scanner_version":"v1.8.0","db":"https://vuln.go.dev"}}'

# called_finding <osv-id> <module> [fixed_version]
# A trace frame carrying .function is govulncheck's symbol level, i.e.
# "your code calls this".
called_finding() {
  local id=$1 module=$2 fixed=${3:-}
  if [ -n "$fixed" ]; then
    printf '{"finding":{"osv":"%s","fixed_version":"%s","trace":[{"module":"%s","package":"%s","function":"Load"}]}}\n' \
      "$id" "$fixed" "$module" "$module"
  else
    printf '{"finding":{"osv":"%s","trace":[{"module":"%s","package":"%s","function":"Load"}]}}\n' \
      "$id" "$module" "$module"
  fi
}

# imported_finding: module/package level only, no .function frame. This is
# the "you require it but never call it" shape govulncheck reports
# separately and does NOT fail on.
imported_finding() {
  printf '{"finding":{"osv":"%s","trace":[{"module":"%s"}]}}\n' "$1" "$2"
}

osv_record() {
  printf '{"osv":{"id":"%s","summary":"%s"}}\n' "$1" "$2"
}

# allowlist <id>...  writes a complete, audited entry per id.
allowlist() {
  local entries=""
  for id in "$@"; do
    [ -n "$entries" ] && entries="$entries,"
    entries="$entries{\"id\":\"$id\",\"reason\":\"no upstream fix\",\"mitigation\":\"wrapped in recover\",\"reviewed_by\":\"tester\",\"reviewed_on\":\"2026-09-17\"}"
  done
  printf '{"allowed":[%s]}\n' "$entries" > "$WORK/allow.json"
}

expect() {
  local label=$1 want_status=$2 want_substr=$3
  local out status
  set +e
  out=$(BROKOLI_VULN_JSON_FILE="$WORK/scan.json" BROKOLI_VULN_ALLOWLIST="$WORK/allow.json" bash "$SCRIPT" 2>&1)
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

# --- an unlisted called vulnerability fails ---
{ echo "$config"; osv_record GO-2026-9999 "Something bad"; called_finding GO-2026-9999 example.com/mod; } > "$WORK/scan.json"
allowlist
expect "an unlisted called vulnerability fails" 1 "FAIL     GO-2026-9999"

# --- and it says how to fix it when a fix exists ---
{ echo "$config"; osv_record GO-2026-9999 "Something bad"; called_finding GO-2026-9999 example.com/mod v1.2.3; } > "$WORK/scan.json"
allowlist
expect "an unlisted vulnerability names its fix" 1 "upgrade example.com/mod to v1.2.3"

# --- an audited, unfixable one passes, and says so out loud ---
{ echo "$config"; osv_record GO-2026-6452 "Panic via negative shared-string index"; called_finding GO-2026-6452 github.com/xuri/excelize/v2; } > "$WORK/scan.json"
allowlist GO-2026-6452
expect "an allowlisted unfixable vulnerability passes" 0 "WAIVED   GO-2026-6452"
expect "and the waiver is visible, with its reason" 0 "why:        no upstream fix"

# --- THE EXPIRY RULE: allowlisted, but now fixed upstream ---
{ echo "$config"; osv_record GO-2026-6452 "Panic via negative shared-string index"; called_finding GO-2026-6452 github.com/xuri/excelize/v2 v2.12.0; } > "$WORK/scan.json"
allowlist GO-2026-6452
expect "an allowlisted vulnerability that gained a fix FAILS" 1 "a fixed version now EXISTS (v2.12.0)"

# --- module/package level findings are not failed on: that is the same
# set the bare govulncheck command tolerated ---
{ echo "$config"; osv_record GO-2026-5774 "Something in a module we import"; imported_finding GO-2026-5774 github.com/go-chi/chi/v5; } > "$WORK/scan.json"
allowlist
expect "an imported-but-uncalled vulnerability does not fail" 0 "0 unwaived"

# --- a clean scan passes and says what it looked at ---
{ echo "$config"; osv_record GO-2026-1111 "Unrelated"; } > "$WORK/scan.json"
allowlist
expect "a clean scan passes" 0 "0 called by this repo"

# --- a stale allowlist entry is reported, but is not fatal ---
{ echo "$config"; } > "$WORK/scan.json"
allowlist GO-2026-6452
expect "a stale allowlist entry is surfaced" 0 "STALE    GO-2026-6452"

# --- incomplete audit entries cannot waive anything ---
{ echo "$config"; osv_record GO-2026-6452 "x"; called_finding GO-2026-6452 github.com/xuri/excelize/v2; } > "$WORK/scan.json"
printf '{"allowed":[{"id":"GO-2026-6452","reason":"","mitigation":"","reviewed_by":"","reviewed_on":""}]}\n' > "$WORK/allow.json"
expect "an entry with no reason is refused" 1 "incomplete allowlist entries"

# --- the vacuity guards: empty and garbage output are failures ---
: > "$WORK/scan.json"
allowlist
expect "an empty scan is a failure, not a clean bill" 1 "inspected nothing"

echo "this is not json at all" > "$WORK/scan.json"
allowlist
expect "unparseable output is a failure" 1 "could not be parsed as JSON"

# Valid JSON, but not a scan: no config record means the scanner never
# really ran, which must not read as clean.
echo '{"finding":{"osv":"GO-1","trace":[{"module":"m"}]}}' > "$WORK/scan.json"
allowlist
expect "output with no config record is a failure" 1 "not a scan result"

# --- a missing or malformed allowlist is a failure ---
{ echo "$config"; } > "$WORK/scan.json"
rm -f "$WORK/allow.json"
expect "a missing allowlist fails" 1 "not found"

{ echo "$config"; } > "$WORK/scan.json"
echo '{"nope":[]}' > "$WORK/allow.json"
expect "an allowlist with no .allowed array fails" 1 "no .allowed array"

if [ "$fail" -ne 0 ]; then
  echo "vulnerability gate tests FAILED"
  exit 1
fi
echo "vulnerability gate tests passed"

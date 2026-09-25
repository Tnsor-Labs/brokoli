#!/usr/bin/env bash
# Known-vulnerability gate over the code this repo actually calls.
#
# Why this exists instead of a bare `govulncheck ./...`: govulncheck exits
# non-zero for ANY called vulnerability, including one with no upstream fix.
# A single unfixable advisory in a reachable path therefore makes the job
# permanently red, and a gate that cannot go green is a gate somebody
# eventually deletes. GO-2026-6452 (excelize, affected from version 0, no
# fixed version) is exactly that case.
#
# So this keeps the same verdict govulncheck gives -- fail on anything our
# own code calls -- while allowing named, justified, expiring exceptions in
# security/vuln-allowlist.json. Two properties make that safe rather than a
# rubber stamp:
#
#   1. An allowlisted advisory that has SINCE GAINED a fixed version fails
#      the build. The exception is valid only while it is unfixable, so it
#      cannot rot into a blanket suppression.
#   2. Everything suppressed is printed on every run, with its reason, so a
#      reader of the log sees what was waived rather than a silent pass.
#
# govulncheck is deliberately still installed @latest. Catching newly
# published advisories against pinned dependencies is the entire point;
# pinning the scanner would have hidden the two excelize advisories that
# motivated this script.
#
# Scope note: govulncheck emits findings at three levels -- module required,
# package imported, and symbol called. Only the symbol level ("your code is
# affected") is failed on here, which is the same set the bare command
# failed on. Failing on the other two would be a stricter policy adopted by
# accident, not a decision.
set -euo pipefail

cd "$(dirname "$0")/.."

ALLOWLIST=${BROKOLI_VULN_ALLOWLIST:-security/vuln-allowlist.json}

# BROKOLI_VULN_JSON_FILE injects a govulncheck JSON stream instead of
# running the scanner. It exists so the gate's own test
# (scripts/check-vulns_test.sh) can drive the real parser over fixtures --
# a vulnerability gate that has never been observed to fail is
# indistinguishable from one that cannot.
if [ -n "${BROKOLI_VULN_JSON_FILE:-}" ]; then
  raw=$(cat "$BROKOLI_VULN_JSON_FILE")
  scan_status=0
else
  command -v govulncheck >/dev/null 2>&1 || {
    echo "ERROR: govulncheck is not on PATH." >&2
    exit 1
  }
  set +e
  raw=$(govulncheck -format json ./... 2>/dev/null)
  scan_status=$?
  set -e
fi

# Deliberately not ${raw//[[:space:]]/}: bash pattern substitution over a
# real scan (700 KB of JSON) takes minutes and hung this gate in testing.
# Trimming until the first non-space byte is constant work.
if [ -z "$(printf '%s' "$raw" | tr -d '[:space:]' | head -c 1)" ]; then
  echo "ERROR: govulncheck produced no output (exit $scan_status) -- the scan inspected nothing." >&2
  echo "An empty scan must never read as a clean scan; refusing to report success." >&2
  exit 1
fi

# Parse once, up front: unparseable output must fail loudly rather than
# silently yield zero findings, which would look identical to "clean".
if ! printf '%s' "$raw" | jq -s . >/dev/null 2>&1; then
  echo "ERROR: govulncheck output could not be parsed as JSON (exit $scan_status)." >&2
  echo "Refusing to treat unreadable output as a clean scan. First 20 lines:" >&2
  printf '%s\n' "$raw" | head -20 >&2
  exit 1
fi

# A scan that produced no config record did not really run, whatever else
# it printed.
if [ "$(printf '%s' "$raw" | jq -s '[.[] | select(.config)] | length')" -eq 0 ]; then
  echo "ERROR: govulncheck output has no config record (exit $scan_status) -- this is not a scan result." >&2
  exit 1
fi

if [ ! -f "$ALLOWLIST" ]; then
  echo "ERROR: allowlist $ALLOWLIST not found." >&2
  exit 1
fi
if ! jq -e '.allowed | type == "array"' "$ALLOWLIST" >/dev/null 2>&1; then
  echo "ERROR: $ALLOWLIST has no .allowed array." >&2
  exit 1
fi

# An entry missing any of these is not an audit, so it does not get to
# waive anything.
missing=$(jq -r '
  [.allowed[]
   | select((.id // "") == "" or (.reason // "") == "" or (.mitigation // "") == ""
            or (.reviewed_by // "") == "" or (.reviewed_on // "") == "")
   | (.id // "(entry with no id)")] | join(", ")' "$ALLOWLIST")
if [ -n "$missing" ]; then
  echo "ERROR: incomplete allowlist entries (need id, reason, mitigation, reviewed_by, reviewed_on): $missing" >&2
  exit 1
fi

# Called findings only: a trace frame carrying .function is govulncheck's
# symbol level, the "your code is affected" set.
# Fields are pipe-separated with an explicit "-" for an absent fixed
# version. Tab looks tidier but is IFS whitespace, so bash `read` collapses
# two consecutive tabs into one separator and an empty fixed_version
# silently shifts the module into its place -- which reported an unfixable
# advisory as "a fix now exists". Caught by this script's own tests.
called=$(printf '%s' "$raw" | jq -s -r '
  [.[] | select(.finding) | .finding | select(any(.trace[]?; has("function")))]
  | unique_by(.osv) | .[]
  | "\(.osv)|\(.fixed_version // "-")|\(.trace[0].module // "-")"')

scanned=$(printf '%s' "$raw" | jq -s '[.[] | select(.osv)] | length')
echo "govulncheck: scanned against $scanned advisories; $(printf '%s' "$called" | grep -c . || true) called by this repo"

fail=0
waived=0

while IFS="|" read -r id fixed module; do
  [ -n "$id" ] || continue
  [ "$fixed" = "-" ] && fixed=""
  [ "$module" = "-" ] && module=""

  listed=$(jq -r --arg id "$id" '[.allowed[] | select(.id == $id)] | length' "$ALLOWLIST")

  if [ "$listed" -eq 0 ]; then
    summary=$(printf '%s' "$raw" | jq -s -r --arg id "$id" \
      'first(.[] | select(.osv.id == $id) | .osv.summary) // ""')
    echo "FAIL     $id  $module  ${summary}"
    if [ -n "$fixed" ]; then
      echo "         a fix is available: upgrade $module to $fixed"
    else
      echo "         no upstream fix; if that is confirmed, add an audited entry to $ALLOWLIST"
    fi
    fail=$((fail + 1))
    continue
  fi

  # The expiry rule: an exception is only valid while the advisory is
  # genuinely unfixable.
  if [ -n "$fixed" ]; then
    echo "FAIL     $id  $module  allowlisted, but a fixed version now EXISTS ($fixed)"
    echo "         upgrade $module to $fixed and delete its entry from $ALLOWLIST"
    fail=$((fail + 1))
    continue
  fi

  field() { jq -r --arg id "$id" --arg f "$1" 'first(.allowed[] | select(.id == $id) | .[$f]) // ""' "$ALLOWLIST"; }
  reason=$(field reason)
  mitigation=$(field mitigation)
  reviewer="$(field reviewed_by) on $(field reviewed_on)"
  echo "WAIVED   $id  $module  (no upstream fix; reviewed by $reviewer)"
  echo "         why:        $reason"
  echo "         mitigation: $mitigation"
  waived=$((waived + 1))
done <<< "$called"

# An allowlist entry for something no longer reported is stale. Not fatal:
# it can simply mean the call site was removed, which is good news. Say so
# rather than leaving it to accumulate unnoticed.
while read -r id; do
  [ -n "$id" ] || continue
  if ! printf '%s' "$called" | cut -d'|' -f1 | grep -qx "$id"; then
    echo "STALE    $id is allowlisted but no longer reported as called; the entry can be removed"
  fi
done <<< "$(jq -r '.allowed[].id' "$ALLOWLIST")"

echo "result: $fail unwaived, $waived waived"

if [ "$fail" -gt 0 ]; then
  echo "ERROR: $fail called vulnerability(ies) are not covered by an audited allowlist entry." >&2
  exit 1
fi

exit 0

#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
script=scripts/run-preflight-govulncheck.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail=0
expect() {
  local label=$1 want_status=$2 want_text=$3
  shift 3
  local out status
  set +e
  out=$("$@" 2>&1)
  status=$?
  set -e
  if [ "$status" -ne "$want_status" ] || ! grep -qF "$want_text" <<<"$out"; then
    printf 'FAIL %s: exit %s, expected %s and %q\n%s\n' "$label" "$status" "$want_status" "$want_text" "$out"
    fail=1
  else
    echo "ok   $label"
  fi
}

cat >"$work/go-offline" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = env ]; then
  [ "$2" = GOBIN ] && exit 0
  [ "$2" = GOPATH ] && { echo /missing; exit 0; }
fi
echo 'dial tcp: network is unreachable' >&2
exit 1
EOF
chmod +x "$work/go-offline"

expect "offline install is unavailable, not a vulnerability" 2 \
  "SKIP govulncheck (tool unavailable" \
  env BROKOLI_GOVULNCHECK_BIN="$work/missing-govulncheck" \
      BROKOLI_GO_BIN="$work/go-offline" bash "$script"

cat >"$work/govulncheck-wrong-version" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = -version ]; then
  echo 'Scanner: govulncheck@v1.7.01'
  exit 0
fi
echo 'wrong-version scanner must not run' >&2
exit 99
EOF
chmod +x "$work/govulncheck-wrong-version"

expect "a different scanner version is rejected before scanning" 2 \
  "SKIP govulncheck (tool unavailable" \
  env BROKOLI_GOVULNCHECK_BIN="$work/govulncheck-wrong-version" \
      BROKOLI_GO_BIN="$work/go-offline" bash "$script"

cat >"$work/govulncheck" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = -version ]; then
  echo 'Scanner: govulncheck@v1.7.0'
  exit 0
fi
cat <<'JSON'
{"config":{"scanner_name":"govulncheck","scanner_version":"v1.7.0","db":"https://vuln.go.dev"}}
{"osv":{"id":"GO-2026-9999","summary":"test vulnerability"}}
{"finding":{"osv":"GO-2026-9999","fixed_version":"v1.2.3","trace":[{"module":"example.com/vulnerable","package":"example.com/vulnerable","function":"Load"}]}}
JSON
exit 3
EOF
chmod +x "$work/govulncheck"

printf '{"allowed":[]}\n' >"$work/allow.json"
expect "a real vulnerability keeps the failing verdict" 1 \
  "FAIL     GO-2026-9999" \
  env BROKOLI_GOVULNCHECK_BIN="$work/govulncheck" \
      BROKOLI_VULN_ALLOWLIST="$work/allow.json" bash "$script"

# A scan that never ran must remain a failing preflight verdict. Test the
# small verdict helper directly so removing the failure return is caught.
# shellcheck source=scripts/preflight-govulncheck-verdict.sh
source scripts/preflight-govulncheck-verdict.sh
set +e
verdict_out=$(govulncheck_verdict 2 "$work/govulncheck.log" 2>&1)
verdict_status=$?
set -e
if [ "$verdict_status" -eq 0 ] || ! grep -qF "SKIP" <<<"$verdict_out"; then
  printf 'FAIL unavailable scan verdict: exit %s, expected nonzero and SKIP\n%s\n' \
    "$verdict_status" "$verdict_out"
  fail=1
else
  echo "ok   unavailable scan keeps preflight non-green"
fi

[ "$fail" -eq 0 ] || exit 1
echo "preflight govulncheck tests passed"

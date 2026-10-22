#!/usr/bin/env bash
# Run the pinned govulncheck used by preflight without requiring a download
# when the right version is already installed. Exit 2 means the scan could
# not run; exit 1 is reserved for an actual vulnerability verdict.
set -euo pipefail

cd "$(dirname "$0")/.."

version=${GOVULNCHECK_VERSION:-v1.7.0}
go_cmd=${BROKOLI_GO_BIN:-go}
scanner=${BROKOLI_GOVULNCHECK_BIN:-}

has_expected_version() {
  "$1" -version 2>&1 | grep -qE "govulncheck@${version}\$"
}

if [ -z "$scanner" ] && command -v govulncheck >/dev/null 2>&1; then
  scanner=$(command -v govulncheck)
fi

if [ -z "$scanner" ]; then
  gobin=$($go_cmd env GOBIN 2>/dev/null || true)
  if [ -z "$gobin" ]; then
    gopath=$($go_cmd env GOPATH 2>/dev/null || true)
    [ -n "$gopath" ] && gobin="$gopath/bin"
  fi
  [ -n "${gobin:-}" ] && [ -x "$gobin/govulncheck" ] && scanner="$gobin/govulncheck"
fi

if [ -n "$scanner" ] && has_expected_version "$scanner"; then
  echo "govulncheck: using installed $version ($scanner)"
else
  if [ -n "$scanner" ]; then
    echo "govulncheck: installed binary is not $version; installing the pinned version"
  else
    echo "govulncheck: pinned $version is not installed; installing it"
  fi

  install_log=$(mktemp)
  trap 'rm -f "$install_log"' EXIT
  if ! GOTOOLCHAIN="${MOD_TOOLCHAIN:-auto}" "$go_cmd" install "golang.org/x/vuln/cmd/govulncheck@$version" >"$install_log" 2>&1; then
    cat "$install_log" >&2
    echo "SKIP govulncheck (tool unavailable: pinned version could not be installed)" >&2
    exit 2
  fi

  gobin=$($go_cmd env GOBIN)
  [ -n "$gobin" ] || gobin="$($go_cmd env GOPATH)/bin"
  scanner="$gobin/govulncheck"
  if [ ! -x "$scanner" ] || ! has_expected_version "$scanner"; then
    echo "SKIP govulncheck (tool unavailable: installed binary did not report $version)" >&2
    exit 2
  fi
fi

BROKOLI_GOVULNCHECK_BIN="$scanner" exec bash scripts/check-vulns.sh

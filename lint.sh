#!/usr/bin/env bash
# lint.sh — run gofmt + go vet over the OSS repo.
#
# Usage:
#   ./lint.sh              # report issues only (exits non-zero if anything is wrong)
#   ./lint.sh --fix        # auto-apply gofmt formatting in place
#   ./lint.sh --check      # same as no args; explicit for CI
#
# Ignores _archive/ and web/dist/ (vendored UI output).

set -euo pipefail

MODE="check"
case "${1:-}" in
  --fix)    MODE="fix" ;;
  --check|"") MODE="check" ;;
  -h|--help)
    sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "unknown arg: $1 (use --fix or --check)"
    exit 2
    ;;
esac

# Directories to skip when scanning for *.go files. gofmt on `.` walks the tree;
# we filter its output instead of passing explicit paths so new top-level dirs
# are picked up automatically.
IGNORE_PATTERN='^(_archive|web/dist)/'

fail=0

echo "==> gofmt ($MODE)"
if [[ "$MODE" == "fix" ]]; then
  # Apply in place. Then re-check to confirm nothing remains (belt-and-braces).
  gofmt -w .
  remaining=$(gofmt -l . 2>/dev/null | grep -Ev "$IGNORE_PATTERN" || true)
  if [[ -n "$remaining" ]]; then
    echo "  ! gofmt still reports issues after --fix:"
    echo "$remaining" | sed 's/^/    /'
    fail=1
  else
    echo "  clean"
  fi
else
  issues=$(gofmt -l . 2>/dev/null | grep -Ev "$IGNORE_PATTERN" || true)
  if [[ -n "$issues" ]]; then
    echo "  ! these files need formatting (run ./lint.sh --fix):"
    echo "$issues" | sed 's/^/    /'
    fail=1
  else
    echo "  clean"
  fi
fi

echo
echo "==> go vet"
if go vet ./... 2>&1; then
  echo "  clean"
else
  fail=1
fi

echo
if [[ $fail -ne 0 ]]; then
  echo "==> lint failed"
  exit 1
fi
echo "==> lint passed"

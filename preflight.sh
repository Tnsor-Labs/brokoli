#!/usr/bin/env bash
# preflight.sh — run everything CI runs, locally, before pushing.
#
# The whole point: failures get found HERE, not in a GitHub CI round-trip.
# Mirrors .github/workflows/ci.yml and security.yml as of 2026-08-15, with
# two deliberate strictness additions (go vet; svelte-check) that CI lacks.
#
#   ./preflight.sh                 full run (~5-8 min)
#   PREFLIGHT_FAST=1 ./preflight.sh    skip `npm ci` when node_modules exists
#   PREFLIGHT_COVERAGE=1 ./preflight.sh  also run the CI coverage pass (re-runs suite)
#
# Requires: go (toolchain per go.mod), node (20 preferred — warns on drift),
# docker (for CI-identical gosec; skipped loudly if absent), jq.
set -euo pipefail
cd "$(dirname "$0")"

LOGDIR=.preflight
mkdir -p "$LOGDIR"
FAILED=0
START=$(date +%s)

say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
pass() { printf '\033[32mPASS\033[0m %s (%ss)\n' "$1" "$(( $(date +%s) - STAGE_START ))"; }
fail() { printf '\033[31mFAIL\033[0m %s — see %s\n' "$1" "$2"; FAILED=1; }

stage() { STAGE_START=$(date +%s); say "$1"; }

# ---- 1. gofmt — CI parity: UNFILTERED repo root (stricter than lint.sh) ----
stage "gofmt (unfiltered, CI parity)"
UNFORMATTED=$(gofmt -l .)
if [ -n "$UNFORMATTED" ]; then
  echo "$UNFORMATTED"
  echo "run: gofmt -w ."
  exit 1
fi
pass gofmt

# ---- 2. go vet (not in CI's lint job — deliberate extra rigor) ----
stage "go vet"
go vet ./... 2>&1 | tee "$LOGDIR/vet.log"
pass vet

# ---- background security stages (don't need the UI build) ----
say "launching parallel security scans (gosec / govulncheck / licenses)"
(
  if command -v docker >/dev/null 2>&1; then
    docker run --rm -v "$PWD":/src -w /src \
      ghcr.io/securego/gosec@sha256:4342ad119a7c69f3f4e4ce78d81ba183dc774a70a7a4c6eeb15fe9e511f214f0 \
      -no-fail -fmt=json -out=/src/$LOGDIR/gosec.json ./... >/dev/null 2>&1
    findings=$(jq '[.Issues[]] | length' "$LOGDIR/gosec.json")
    baseline=$(jq -r '.issues' security/gosec-baseline.json)
    echo "gosec findings: $findings (baseline: $baseline)" > "$LOGDIR/gosec.verdict"
    if [ "$findings" -gt "$baseline" ]; then exit 1; fi
    if [ "$findings" -lt "$baseline" ]; then echo "note: baseline can be reduced to $findings" >> "$LOGDIR/gosec.verdict"; fi
  else
    echo "SKIP: docker not available — CI WILL run gosec; findings there may differ" > "$LOGDIR/gosec.verdict"
  fi
) > "$LOGDIR/gosec.log" 2>&1 &
GOSEC_PID=$!

# Pin GOTOOLCHAIN to the module's toolchain: `go run pkg@version` is
# module-less, so it only upgrades to x/vuln's own minimum (1.25), which
# then can't analyze packages that use newer language features. CI never
# hits this because setup-go installs the go.mod version outright.
MOD_TOOLCHAIN=$(awk '/^toolchain /{print $2}' go.mod)
( GOTOOLCHAIN="${MOD_TOOLCHAIN:-auto}" go run golang.org/x/vuln/cmd/govulncheck@latest ./... ) > "$LOGDIR/govulncheck.log" 2>&1 &
VULN_PID=$!

(
  go run github.com/google/go-licenses@latest report ./... 2>/dev/null > "$LOGDIR/licenses.txt" || true
  if grep -iE 'GPL-2\.0|GPL-3\.0|AGPL|SSPL|EUPL' "$LOGDIR/licenses.txt"; then
    echo "forbidden license found"; exit 1
  fi
) > "$LOGDIR/licenses.log" 2>&1 &
LIC_PID=$!

# ---- 3. node version guard (CI pins 20) ----
stage "node version"
NODE_MAJOR=$(node -v | sed 's/^v//' | cut -d. -f1)
if [ "$NODE_MAJOR" != "20" ]; then
  if [ -s "$HOME/.nvm/nvm.sh" ]; then
    # Sourcing nvm.sh mutates PATH to nvm's default — if we then fail to
    # land on 20, RESTORE the original PATH: a half-switched node broke
    # npm's native optional deps the first time this script ran.
    PREFLIGHT_OLD_PATH=$PATH
    # shellcheck disable=SC1091
    . "$HOME/.nvm/nvm.sh"
    if nvm use 20 >/dev/null 2>&1 || { nvm install 20 >/dev/null 2>&1 && nvm use 20 >/dev/null 2>&1; }; then
      echo "using node $(node -v) via nvm (CI parity)"
    else
      PATH=$PREFLIGHT_OLD_PATH
      echo "WARN: node $NODE_MAJOR (CI pins 20); nvm couldn't provide 20 — build may differ from CI"
    fi
  else
    echo "WARN: node v$NODE_MAJOR, CI pins v20 — build may differ from CI"
  fi
else
  echo "node $(node -v) matches CI"
fi
pass node-guard

# ---- 4. UI build (go:embed prerequisite) + static checks ----
stage "UI build"
pushd ui >/dev/null
if [ "${PREFLIGHT_FAST:-}" = "1" ] && [ -d node_modules ]; then
  echo "PREFLIGHT_FAST: skipping npm ci"
else
  npm ci --silent
fi
npm run build > "../$LOGDIR/ui-build.log" 2>&1 || { popd >/dev/null; fail ui-build "$LOGDIR/ui-build.log"; exit 1; }
pass ui-build
stage "svelte-check (baseline-gated) + prettier"
# svelte-check carries pre-existing errors (see ui/svelte-check-baseline.json);
# gate on GROWTH, exactly like the gosec baseline.
npm run check > "../$LOGDIR/ui-check.log" 2>&1 || true
CHECK_ERRORS=$(grep -oE '[0-9]+ ERRORS' "../$LOGDIR/ui-check.log" | tail -1 | cut -d' ' -f1)
CHECK_BASELINE=$(jq -r '.errors' svelte-check-baseline.json)
echo "svelte-check errors: ${CHECK_ERRORS:-?} (baseline: $CHECK_BASELINE)"
if [ -n "${CHECK_ERRORS:-}" ] && [ "$CHECK_ERRORS" -gt "$CHECK_BASELINE" ]; then
  popd >/dev/null; fail svelte-check "$LOGDIR/ui-check.log"; exit 1
fi
# Prettier gate scoped to CHANGED files only: the tree has never been
# fully prettier-formatted and CI has no format gate — wholesale
# reformatting belongs in its own dedicated PR, not as preflight fallout.
CHANGED_UI=$( (git -C .. diff --name-only origin/main...HEAD -- ui/src; git -C .. diff --name-only -- ui/src; git -C .. ls-files --others --exclude-standard -- ui/src) | sort -u | grep -E '\.(ts|svelte)$' | sed 's|^ui/||' || true)
if [ -n "$CHANGED_UI" ]; then
  # shellcheck disable=SC2086
  npx prettier --check $CHANGED_UI > "../$LOGDIR/ui-format.log" 2>&1 || { popd >/dev/null; fail prettier "$LOGDIR/ui-format.log"; exit 1; }
  echo "prettier: $(echo "$CHANGED_UI" | wc -l) changed file(s) clean"
else
  echo "prettier: no changed UI files"
fi
popd >/dev/null
pass ui-checks

# ---- 5. tests — CI parity: -v -race ----
stage "go test -race ./... (the long pole)"
go test -race ./... 2>&1 | tail -5 | tee "$LOGDIR/test.tail"
pass tests

# ---- 6. optional coverage pass (CI runs it; adds minutes; opt-in) ----
if [ "${PREFLIGHT_COVERAGE:-}" = "1" ]; then
  stage "coverage (CI parity)"
  go test -coverprofile="$LOGDIR/coverage.out" ./... > "$LOGDIR/coverage.log" 2>&1
  go tool cover -func="$LOGDIR/coverage.out" | tail -1
  pass coverage
fi

# ---- 7. build + smoke — CI parity ----
stage "CGO_ENABLED=0 build + --help smoke"
CGO_ENABLED=0 go build -o "$LOGDIR/brokoli-preflight" .
"$LOGDIR/brokoli-preflight" --help >/dev/null
pass build-smoke

# ---- collect parallel stages ----
say "waiting on parallel security scans"
wait "$GOSEC_PID" || fail gosec "$LOGDIR/gosec.log"
cat "$LOGDIR/gosec.verdict" 2>/dev/null || true
wait "$VULN_PID"  || fail govulncheck "$LOGDIR/govulncheck.log"
wait "$LIC_PID"   || fail licenses "$LOGDIR/licenses.log"
[ "$FAILED" = 0 ] && { [ -f "$LOGDIR/gosec.verdict" ] && grep -q SKIP "$LOGDIR/gosec.verdict" || true; }

TOTAL=$(( $(date +%s) - START ))
if [ "$FAILED" = 1 ]; then
  printf '\n\033[31mPREFLIGHT FAILED\033[0m after %ss — fix locally, do not push.\n' "$TOTAL"
  exit 1
fi
printf '\n\033[32mPREFLIGHT GREEN\033[0m in %ss — safe to push.\n' "$TOTAL"

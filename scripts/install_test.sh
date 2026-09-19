#!/usr/bin/env bash
# Tests for install.sh's checksum verification.
#
# The real installer runs end to end, offline. A stub `curl` (and `wget`)
# on a PATH containing only the tools the installer needs serves a fake
# release out of a fixture directory, so each case decides exactly which
# files exist and which hash tools are available. install.sh itself has no
# test hooks: what runs here is what a user runs.
#
# Every refusal case asserts two things: the installer exits non-zero with
# the message that names the reason, and no binary was installed. A
# refusal that still left a binary behind would be the worst outcome.
#
# Usage: bash scripts/install_test.sh
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INSTALLER="$ROOT/install.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

case "$(uname -s)" in Linux) OS_ID=Linux ;; Darwin) OS_ID=Darwin ;; *) echo "unsupported test host"; exit 2 ;; esac
case "$(uname -m)" in x86_64|amd64) ARCH_ID=x86_64 ;; arm64|aarch64) ARCH_ID=arm64 ;; *) echo "unsupported test host"; exit 2 ;; esac
ASSET="brokoli_${OS_ID}_${ARCH_ID}.tar.gz"
VERSION="v9.9.9-test"

pass=0
fail=0
ok()  { printf '  ok    %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  FAIL  %s\n' "$1"; fail=$((fail + 1)); }
skip() { printf '  skip  %s\n' "$1"; }

sha() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# build_bin DIR [tool...] puts symlinks to the named host tools in DIR,
# plus the download stubs. Anything not listed does not exist for the
# installer, which is how the "no hash tool" case is made.
build_bin() {
    local dir=$1; shift
    mkdir -p "$dir"
    for t in "$@"; do
        local p
        p=$(command -v "$t" 2>/dev/null) || continue
        ln -sf "$p" "$dir/$t"
    done
}

BASE_TOOLS="sh uname tar gzip mktemp mkdir chmod mv rm sed awk tr cat cp"
HASH_TOOLS="sha256sum shasum openssl"

# The stubs map a release URL to a file of the same name in $FIXTURE and
# behave like `curl -f` / `wget` on a 404 when the file is absent.
write_stubs() {
    local dir=$1
    cat > "$dir/curl" <<'EOF'
#!/bin/sh
out="" url=""
while [ $# -gt 0 ]; do
    case "$1" in
        -o) out=$2; shift ;;
        -w|-I|-fsSLI) echo "stub curl: version lookup is not expected here" >&2; exit 2 ;;
        http*) url=$1 ;;
    esac
    shift
done
src="$FIXTURE/${url##*/}"
[ -f "$src" ] || exit 22
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
EOF
    cat > "$dir/wget" <<'EOF'
#!/bin/sh
out="" url=""
while [ $# -gt 0 ]; do
    case "$1" in
        -O) out=$2; shift ;;
        http*) url=$1 ;;
    esac
    shift
done
src="$FIXTURE/${url##*/}"
[ -f "$src" ] || exit 8
if [ -n "$out" ] && [ "$out" != "-" ]; then cp "$src" "$out"; else cat "$src"; fi
EOF
    chmod +x "$dir/curl" "$dir/wget"
}

# A fake release: an archive holding a `brokoli` that prints its version,
# and a checksums.txt in the release's own format.
make_release() {
    local fix=$1
    mkdir -p "$fix" "$WORK/pkg"
    printf '#!/bin/sh\necho "brokoli 9.9.9-test"\n' > "$WORK/pkg/brokoli"
    chmod +x "$WORK/pkg/brokoli"
    tar -czf "$fix/$ASSET" -C "$WORK/pkg" brokoli
    {
        echo "0000000000000000000000000000000000000000000000000000000000000000  brokoli_Windows_x86_64.tar.gz"
        echo "$(sha "$fix/$ASSET")  $ASSET"
    } > "$fix/checksums.txt"
}

# run_install SHELL BIN FIXTURE runs the real installer with nothing but
# BIN on PATH and records its exit status, output, and install dir.
run_install() {
    local shell=$1 bin=$2 fix=$3
    INST="$WORK/inst.$RANDOM"
    set +e
    OUT=$(env -i PATH="$bin" HOME="$WORK/home" FIXTURE="$fix" \
        BROKOLI_VERSION="$VERSION" BROKOLI_INSTALL_DIR="$INST" BROKOLI_NO_SETUP=1 \
        $shell "$INSTALLER" 2>&1)
    STATUS=$?
    set -e
}

installed() { [ -x "$INST/brokoli" ]; }

expect_refusal() {
    local name=$1 want=$2
    if [ "$STATUS" -eq 0 ]; then bad "$name: installer exited 0"; return; fi
    if ! printf '%s' "$OUT" | grep -q -- "$want"; then bad "$name: message does not say \"$want\""; printf '%s\n' "$OUT" | sed 's/^/        /'; return; fi
    if installed; then bad "$name: a binary was installed anyway"; return; fi
    ok "$name"
}

SHELLS=""
for s in dash bash; do command -v "$s" >/dev/null 2>&1 && SHELLS="$SHELLS $(command -v "$s")"; done
command -v busybox >/dev/null 2>&1 && SHELLS="$SHELLS $(command -v busybox)_sh"
[ -n "$SHELLS" ] || { echo "no shell to test with"; exit 2; }

for shell in $SHELLS; do
    run_shell=${shell/_sh/ sh}
    label=$(basename "${shell%_sh}")
    echo "--- $label ---"

    FULL="$WORK/bin-full-$label";      build_bin "$FULL" $BASE_TOOLS $HASH_TOOLS; write_stubs "$FULL"
    NOHASH="$WORK/bin-nohash-$label";  build_bin "$NOHASH" $BASE_TOOLS;           write_stubs "$NOHASH"
    SHASUM="$WORK/bin-shasum-$label";  build_bin "$SHASUM" $BASE_TOOLS shasum;    write_stubs "$SHASUM"
    OPENSSL="$WORK/bin-openssl-$label"; build_bin "$OPENSSL" $BASE_TOOLS openssl; write_stubs "$OPENSSL"
    WGETONLY="$WORK/bin-wget-$label";  build_bin "$WGETONLY" $BASE_TOOLS $HASH_TOOLS; write_stubs "$WGETONLY"; rm "$WGETONLY/curl"

    # 1. A matching archive installs, and says it was verified.
    F="$WORK/fix-good-$label"; make_release "$F"
    run_install "$run_shell" "$FULL" "$F"
    if [ "$STATUS" -eq 0 ] && installed && printf '%s' "$OUT" | grep -q "Checksum OK"; then
        ok "verified archive installs"
    else
        bad "verified archive installs (exit $STATUS)"; printf '%s\n' "$OUT" | sed 's/^/        /'
    fi

    # 2. The same, through wget instead of curl.
    # busybox sh runs its built-in wget and sha256sum applets whatever PATH
    # says, so cases 2 and 7 cannot be isolated under it. Skipped by name
    # rather than counted as passes.
    if [ "$label" = busybox ]; then
        skip "verified archive installs through wget (busybox uses its own wget)"
    else
        run_install "$run_shell" "$WGETONLY" "$F"
        if [ "$STATUS" -eq 0 ] && installed; then ok "verified archive installs through wget"
        else bad "verified archive installs through wget (exit $STATUS)"; printf '%s\n' "$OUT" | sed 's/^/        /'; fi
    fi

    # 2b. macOS has shasum rather than sha256sum, and openssl is the last
    # fallback. Each is exercised with it as the only hash tool present.
    for tool in shasum openssl; do
        if [ "$label" = busybox ]; then
            skip "verifies with only $tool (busybox always has sha256sum)"; continue
        fi
        if ! command -v "$tool" >/dev/null 2>&1; then
            skip "verifies with only $tool ($tool not on this host)"; continue
        fi
        [ "$tool" = shasum ] && bin=$SHASUM || bin=$OPENSSL
        run_install "$run_shell" "$bin" "$F"
        if [ "$STATUS" -eq 0 ] && installed && printf '%s' "$OUT" | grep -q "Checksum OK"; then ok "verifies with only $tool"
        else bad "verifies with only $tool (exit $STATUS)"; printf '%s\n' "$OUT" | sed 's/^/        /'; fi
    done

    # 3. An upper-case hash in checksums.txt still matches.
    F="$WORK/fix-upper-$label"; make_release "$F"
    awk '{ printf "%s  %s\n", toupper($1), $2 }' "$F/checksums.txt" > "$F/c" && mv "$F/c" "$F/checksums.txt"
    run_install "$run_shell" "$FULL" "$F"
    if [ "$STATUS" -eq 0 ] && installed; then ok "upper-case checksum matches"
    else bad "upper-case checksum matches (exit $STATUS)"; fi

    # 4. An archive that differs from the published checksum is refused.
    F="$WORK/fix-tampered-$label"; make_release "$F"
    printf '#!/bin/sh\necho tampered\n' > "$WORK/pkg/brokoli"
    tar -czf "$F/$ASSET" -C "$WORK/pkg" brokoli
    run_install "$run_shell" "$FULL" "$F"
    expect_refusal "altered archive is refused" "checksum mismatch"

    # 5. No entry for this platform's archive.
    F="$WORK/fix-noentry-$label"; make_release "$F"
    grep -v " $ASSET\$" "$F/checksums.txt" > "$F/c" && mv "$F/c" "$F/checksums.txt"
    run_install "$run_shell" "$FULL" "$F"
    expect_refusal "missing checksum entry is refused" "has no entry for $ASSET"

    # 6. No checksums.txt in the release at all.
    F="$WORK/fix-nosums-$label"; make_release "$F"; rm "$F/checksums.txt"
    run_install "$run_shell" "$FULL" "$F"
    expect_refusal "missing checksums.txt is refused" "could not download"

    # 7. No way to compute SHA-256 on this machine.
    if [ "$label" = busybox ]; then
        skip "no hash tool is refused (busybox always has sha256sum)"
    else
        F="$WORK/fix-nohash-$label"; make_release "$F"
        run_install "$run_shell" "$NOHASH" "$F"
        expect_refusal "no hash tool is refused" "no SHA-256 tool found"
    fi
done

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]

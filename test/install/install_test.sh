#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."
real_go=$(command -v "${GO:-go}")
work=$(mktemp -d "${TMPDIR:-/tmp}/nesdit-install-test.XXXXXX")
printf 'Install test artifacts: %s\n' "$work"
# All installation writes are sandboxed; retain logs for failed checks too.
unset MAKEFLAGS MFLAGS MAKEOVERRIDES GOBIN GOPATH GOOS GOARCH
export REAL_GO="$real_go"
base_path=$PATH
count=0

mkdir -p "$work/tool with spaces"
cat > "$work/tool with spaces/go" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [[ $1 == env ]]; then
    printf '%s\n' "$2" >> "$CASE_DIR/queries"
    [[ ${FAIL_QUERY:-} != "$2" ]] || exit 19
    if [[ ${USE_REAL_ENV:-} == 1 ]]; then
        exec "$REAL_GO" "$@"
    fi
    case $2 in
        GOBIN) printf '%s\n' "${SAVED_GOBIN:-}" ;;
        GOPATH) printf '%s\n' "${GOPATH:-${SAVED_GOPATH:-}}" ;;
        GOHOSTOS) printf 'darwin\n' ;;
        GOHOSTARCH) printf 'arm64\n' ;;
        *) exit 20 ;;
    esac
elif [[ $1 == install ]]; then
    [[ ${FAIL_INSTALL:-} != 1 ]] || exit 23
    printf '%s\n' "$GOBIN" > "$CASE_DIR/destination"
    printf '%s/%s/%s\n' "$GOOS" "$GOARCH" "$CGO_ENABLED" > "$CASE_DIR/platform"
    printf '%s\n' "$@" > "$CASE_DIR/args"
    mkdir -p "$GOBIN"
    printf 'sandbox executable\n' > "$GOBIN/nesdit"
else
    exit 24
fi
SHIM
chmod +x "$work/tool with spaces/go"

run_case() (
    name=$1
    expected=$2
    shift 2
    export CASE_DIR="$work/$name" HOME="$work/$name/home with spaces"
    export PATH="$base_path" GO="$work/tool with spaces/go" GOENV=off
    export SAVED_GOBIN= SAVED_GOPATH="$work/not-on-path"
    unset FAIL_QUERY FAIL_INSTALL USE_REAL_ENV
    mkdir -p "$CASE_DIR"
    "$@" > "$CASE_DIR/output.log" 2>&1
    [[ $(<"$CASE_DIR/destination") == "$expected" ]]
    [[ -f "$expected/nesdit" ]]
    [[ $(<"$CASE_DIR/platform") == darwin/arm64/0 ]]
    grep -Fx -- '-mod=readonly' "$CASE_DIR/args" >/dev/null
    grep -Fx -- '-trimpath' "$CASE_DIR/args" >/dev/null
    grep -Fx -- "-ldflags=-s -w -X github.com/sdkks/nesdit/internal/run.Version=$(<VERSION)" "$CASE_DIR/args" >/dev/null
    grep -Fx -- './cmd/nesdit' "$CASE_DIR/args" >/dev/null
)

pass_case() {
    run_case "$@"
    count=$((count + 1))
    printf 'PASS %s\n' "$1"
}

explicit() { GOBIN="$work/explicit/bin with spaces" SAVED_GOBIN="$work/ignored" make install; }
make_override() { GOBIN="$work/ignored" make install "GOBIN=$work/override/bin with spaces"; }
saved() { SAVED_GOBIN="$work/saved/bin" make install; }
primary() { GOPATH="$work/primary/root" PATH="$work/primary/root/bin:$base_path" make install; }
secondary() { make install "GOPATH=$work/secondary/first:$work/secondary/second" "PATH=$work/secondary/second/bin:$base_path"; }
trailing() { GOPATH="$work/trailing/root///" PATH="$work/trailing/root/bin///:$base_path" make install; }
first_match() { GOPATH="$work/first-match/one:$work/first-match/two" PATH="$work/first-match/two/bin:$work/first-match/one/bin:$base_path" make install; }
fallback() { PATH="$work/arbitrary-bin:$base_path" make install; }
native() { GOOS=linux GOARCH=amd64 GOBIN="$work/native/bin" make install; }
literal() { make install "GOBIN=$work/literal/bin; echo not-shell-source"; }

pass_case explicit "$work/explicit/bin with spaces" explicit
[[ ! -f "$work/explicit/queries" ]] || ! grep -Fx GOBIN "$work/explicit/queries" >/dev/null
pass_case override "$work/override/bin with spaces" make_override
pass_case saved "$work/saved/bin" saved
pass_case primary "$work/primary/root/bin" primary
pass_case secondary "$work/secondary/second/bin" secondary
pass_case trailing "$work/trailing/root/bin" trailing
pass_case first-match "$work/first-match/one/bin" first_match
pass_case fallback "$work/fallback/home with spaces/.local/bin" fallback
pass_case native "$work/native/bin" native
pass_case literal "$work/literal/bin; echo not-shell-source" literal

# Use only an isolated settings file to verify Go's actual saved-value semantics.
real_settings() {
    export GOENV="$CASE_DIR/go-settings" USE_REAL_ENV=1
    printf 'GOBIN=%s\nGOOS=linux\nGOARCH=amd64\n' "$work/real-settings/bin with spaces" > "$GOENV"
    [[ $(GOOS= GOARCH= "$REAL_GO" env GOOS GOARCH) == $'linux\namd64' ]]
    make install
}
# The shim records the actual host selected by Go, which may differ by machine.
(
    export CASE_DIR="$work/real-settings" HOME="$work/real-settings/home" PATH="$base_path"
    export GO="$work/tool with spaces/go"
    mkdir -p "$CASE_DIR"
    real_settings > "$CASE_DIR/output.log" 2>&1
    [[ $(<"$CASE_DIR/destination") == "$work/real-settings/bin with spaces" ]]
    host_os=$(GOENV=off "$REAL_GO" env GOHOSTOS)
    host_arch=$(GOENV=off "$REAL_GO" env GOHOSTARCH)
    [[ $(<"$CASE_DIR/platform") == "$host_os/$host_arch/0" ]]
)
count=$((count + 1))
printf 'PASS real-settings\n'

expect_failure() (
    name=$1
    shift
    export CASE_DIR="$work/$name" HOME="$work/$name/home" PATH="$base_path"
    export GO="$work/tool with spaces/go" GOENV=off SAVED_GOBIN= SAVED_GOPATH="$work/not-on-path"
    unset FAIL_QUERY FAIL_INSTALL
    mkdir -p "$CASE_DIR"
    if "$@" > "$CASE_DIR/output.log" 2>&1; then
        printf 'Expected failure: %s\n' "$name" >&2
        exit 1
    fi
    ! grep -q 'Installed nesdit' "$CASE_DIR/output.log"
)
fail_gobin() { FAIL_QUERY=GOBIN make install; }
fail_gopath() { FAIL_QUERY=GOPATH make install; }
fail_host() { FAIL_QUERY=GOHOSTOS GOBIN="$work/fail-host/bin" make install; }
fail_install() { FAIL_INSTALL=1 GOBIN="$work/fail-install/bin" make install; }
not_directory() { printf 'file\n' > "$CASE_DIR/file"; make install "GOBIN=$CASE_DIR/file/bin"; }
for failure in fail_gobin fail_gopath fail_host fail_install not_directory; do
    expect_failure "$failure" "$failure"
    count=$((count + 1))
    printf 'PASS %s\n' "$failure"
done
printf 'Passed %s install checks.\n' "$count"

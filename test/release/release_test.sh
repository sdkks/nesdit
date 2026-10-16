#!/usr/bin/env bash
# Repeatable, network-free release checks. Every mutation is in fresh scratch.
set -euo pipefail
# Nested fixture Make calls must not replay the caller's assignments or flags.
# Each fixture supplies its own overrides and parallelism explicitly.
unset MAKEFLAGS MFLAGS MAKEOVERRIDES
src=$(cd "$(dirname "$0")/../.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/nesdit-release-test.XXXXXX")
echo "Release test fixtures and complete logs retained at $scratch"
export REAL_GIT
REAL_GIT=$(command -v git)
make_bin=$(command -v make)
mkdir "$scratch/tools"
export GIT_AUTHOR_NAME='Release Test' GIT_AUTHOR_EMAIL='release-test@example.invalid'
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"

cat > "$scratch/tools/git" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = ls-remote ] || [ "$1" = push ]; then
    operation=$1
    shift
    [ "$1" = git@github.com:sdkks/nesdit.git ] || exit 90
    shift
    echo "git $operation $*" >> "$CASE_STATE/events"
    if [ "$operation" = ls-remote ] && [ -f "$CASE_STATE/remote-fail" ]; then
        echo 'Remote unavailable' >&2
        exit 1
    fi
    exec "$REAL_GIT" "$operation" "$CASE_STATE/remote.git" "$@"
fi
exec "$REAL_GIT" "$@"
FAKE
cat > "$scratch/tools/gh" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf 'gh' >> "$CASE_STATE/events"
printf ' %q' "$@" >> "$CASE_STATE/events"
printf '\n' >> "$CASE_STATE/events"
case "$1 $2" in
    'auth status') [ ! -f "$CASE_STATE/auth-fail" ] ;;
    'api --hostname')
        if [ -f "$CASE_STATE/api-fail" ]; then echo 'API unavailable' >&2; exit 1; fi
        [ "$3" = github.com ] && [ "$4" = --method ] && [ "$5" = GET ]
        [ "$6" = --paginate ] && [ "$7" = repos/sdkks/nesdit/releases ]
        if [ -f "$CASE_STATE/releases" ]; then cat "$CASE_STATE/releases"; fi
        ;;
    'release create')
        [[ " $* " = *' --verify-tag '* ]] && [[ " $* " = *' --repo github.com/sdkks/nesdit '* ]]
        "$REAL_GIT" --git-dir="$CASE_STATE/remote.git" rev-parse "refs/tags/$3^{commit}" >/dev/null
        printf '%s\n' "$3" >> "$CASE_STATE/releases"
        ;;
    'release upload')
        [[ " $* " != *' --clobber '* ]]
        [ ! -f "$CASE_STATE/upload-fail" ] || exit 1
        tag=$3
        shift 3
        [ "$#" -eq 6 ] && [ "$5" = --repo ] && [ "$6" = github.com/sdkks/nesdit ]
        out=$(dirname "$1")
        printf '%s\n' "$out" > "$CASE_STATE/assets"
        for file in "$1" "$2" "$3" "$4"; do [ -s "$file" ]; done
        if command -v sha256sum >/dev/null; then
            (cd "$out" && sha256sum -c "nesdit_${tag#v}_checksums.txt")
        else
            (cd "$out" && shasum -a 256 -c "nesdit_${tag#v}_checksums.txt")
        fi
        ;;
    *) echo "Unexpected gh call: $*" >&2; exit 91 ;;
esac
FAKE
cat > "$scratch/tools/make" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
[ "$*" = test-all ]
git diff --quiet -- VERSION
printf 'verify %s\n' "$(<VERSION)" >> "$CASE_STATE/events"
[ ! -f "$CASE_STATE/test-fail" ]
FAKE
cat > "$scratch/tools/go" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = build ]
echo "build $GOOS/$GOARCH $CGO_ENABLED $*" >> "$CASE_STATE/events"
[ ! -f "$CASE_STATE/build-fail" ] || exit 1
[ "$CGO_ENABLED" = 0 ]
[[ " $* " = *' -trimpath '* ]] && [[ " $* " = *' -mod=readonly '* ]]
[[ " $* " = *' -ldflags=-s -w -extldflags=-static '* ]]
while [ "$1" != -o ]; do shift; done
shift
if [ -f "$CASE_STATE/oversize" ]; then
    dd if=/dev/zero of="$1" bs=1048576 count=16
elif [ -f "$CASE_STATE/binary-size" ]; then
    # Sparse fixture: exercise the exact byte boundary without huge logs.
    size=$(<"$CASE_STATE/binary-size")
    dd if=/dev/zero of="$1" bs=1 count=1 seek="$((size - 1))"
else
    printf '#!/usr/bin/env bash\necho "nesdit version %s (%s)"\n' "$(<VERSION)" "$(git rev-parse --short=12 HEAD)" > "$1"
fi
chmod +x "$1"
FAKE
chmod +x "$scratch/tools/git" "$scratch/tools/gh" "$scratch/tools/make" "$scratch/tools/go"
export PATH="$scratch/tools:$PATH"
export GO=go
case_count=0
new_case() {
    case_count=$((case_count + 1))
    export CASE_STATE="$scratch/case-$case_count"
    mkdir -p "$CASE_STATE/repo/scripts" "$CASE_STATE/repo/docs"
    repo="$CASE_STATE/repo"
    cp "$src"/scripts/{bump-version,release-common,release,package-release}.sh "$repo/scripts/"
    cp "$src/Makefile" "$src/README.md" "$src/THIRD-PARTY-NOTICES.md" "$repo/"
    cp "$src/docs/nesdit.1" "$repo/docs/"
    cd "$repo"
    # Fixtures have their own stable base, independent of the version being
    # released by the caller (these checks also run inside make test-all).
    printf '0.3.2\n' > VERSION
    "$REAL_GIT" init -q
    "$REAL_GIT" init --bare -q "$CASE_STATE/remote.git"
    "$REAL_GIT" add .
    "$REAL_GIT" commit -qm 'chore: initial version'
    "$REAL_GIT" tag v0.3.2
    : > "$CASE_STATE/events"
    unset BUMP
}
commit_message() {
    printf '%s\n' "$1" > "$CASE_STATE/message"
    "$REAL_GIT" commit --allow-empty -q -F "$CASE_STATE/message"
}
assert_version() { [ "$(<VERSION)" = "$1" ]; }
expect_failure() {
    if "$@" > "$CASE_STATE/failure.log" 2>&1; then
        echo "Expected failure: $*" >&2
        exit 1
    fi
    cat "$CASE_STATE/failure.log"
}
no_publication() {
    ! grep -Eq '^git push|^gh release (create|upload)' "$CASE_STATE/events"
}

# Subject/body detection, explicit levels, numeric validation and idempotence.
for entry in 'fix: repair|0.3.3' $'fix: repair\n\nfeat!: documentation example only|0.3.3' 'feat(cli): add option|0.4.0' 'chore!: change contract|1.0.0' $'fix: repair\n\nBREAKING CHANGE: incompatible behavior|1.0.0' $'fix: repair\n\nBREAKING-CHANGE: incompatible behavior|1.0.0'; do
    new_case
    commit_message "${entry%|*}"
    bash scripts/bump-version.sh
    assert_version "${entry##*|}"
    bash scripts/bump-version.sh
    assert_version "${entry##*|}"
done
for entry in major:1.0.0 minor:0.4.0 patch:0.3.3 2.5.8:2.5.8; do
    new_case
    bash scripts/bump-version.sh "${entry%:*}"
    assert_version "${entry#*:}"
    bash scripts/bump-version.sh "${entry%:*}"
    assert_version "${entry#*:}"
done
new_case
for invalid in 01.2.3 1.2 1.2.3-rc.1 0.3.2 0.2.9 1000000000.0.0 'patch; touch injected'; do
    expect_failure bash scripts/bump-version.sh "$invalid"
    assert_version 0.3.2
    [ ! -e injected ]
done
# Non-semver and unreachable tags do not become the bump base.
new_case
"$REAL_GIT" tag v99-invalid
"$REAL_GIT" checkout -qb disconnected
commit_message 'feat: elsewhere'
"$REAL_GIT" tag v9.9.9
"$REAL_GIT" checkout -q -
commit_message 'fix: on release branch'
bash scripts/bump-version.sh
assert_version 0.3.3

# All guards must stop before a tag push or GitHub write.
for guard in auth api remote dirty index version-index untracked test build oversize local-tag remote-tag; do
    new_case
    commit_message 'fix: repair'
    case "$guard" in
        auth) touch "$CASE_STATE/auth-fail" ;;
        api) touch "$CASE_STATE/api-fail" ;;
        remote) touch "$CASE_STATE/remote-fail" ;;
        dirty) echo dirty >> README.md ;;
        index) echo dirty >> README.md; "$REAL_GIT" add README.md ;;
        version-index) echo 0.3.3 > VERSION; "$REAL_GIT" add VERSION ;;
        untracked) touch unrelated ;;
        test) touch "$CASE_STATE/test-fail" ;;
        build) touch "$CASE_STATE/build-fail" ;;
        oversize) touch "$CASE_STATE/oversize" ;;
        local-tag|remote-tag)
            echo 0.3.3 > VERSION
            "$REAL_GIT" tag v0.3.3 v0.3.2
            if [ "$guard" = remote-tag ]; then
                "$REAL_GIT" push "$CASE_STATE/remote.git" refs/tags/v0.3.3
                "$REAL_GIT" tag -d v0.3.3
            fi
            ;;
    esac
    expect_failure bash scripts/release.sh
    no_publication
    if [ "$guard" != local-tag ]; then [ -z "$("$REAL_GIT" tag --list 'v0.3.3')" ]; fi
    case "$guard" in
        remote) grep -q 'Cannot read remote tag' "$CASE_STATE/failure.log" ;;
        test) grep -q '^verify ' "$CASE_STATE/events" ;;
        build) grep -q '^build ' "$CASE_STATE/events" ;;
        oversize) grep -q 'binary is.*16777216' "$CASE_STATE/failure.log" ;;
    esac
    if [ "$guard" = index ]; then [ "$("$REAL_GIT" diff --cached --name-only)" = README.md ]; fi
    if [ "$guard" = local-tag ]; then [ "$("$REAL_GIT" rev-parse v0.3.3)" = "$("$REAL_GIT" rev-parse v0.3.2)" ]; fi
done
new_case
# Missing gh is represented by a PATH with every required tool except gh.
mkdir "$CASE_STATE/no-gh"
for tool in bash dirname git; do ln -s "$(command -v "$tool")" "$CASE_STATE/no-gh/$tool"; done
expect_failure /usr/bin/env PATH="$CASE_STATE/no-gh" /bin/bash scripts/release.sh
no_publication

# Packaging accepts exactly one byte below the ceiling and rejects the ceiling
# and one byte above it, without ever invoking publication.
for size in 16777215 16777216 16777217; do
    new_case
    printf '%s\n' "$size" > "$CASE_STATE/binary-size"
    if [ "$size" -lt 16777216 ]; then
        bash scripts/package-release.sh "$CASE_STATE/packages"
    else
        expect_failure bash scripts/package-release.sh "$CASE_STATE/packages"
        grep -q "binary is.*$size.*limit: < 16777216" "$CASE_STATE/failure.log"
    fi
    no_publication
done

# Parallel make still runs one ordered release; prepared versions are not bumped twice.
for mode in release release-cli; do
    new_case
    commit_message 'feat: release feature'
    if [ "$mode" = release-cli ]; then bash scripts/bump-version.sh; fi
    "$make_bin" -j8 "$mode"
    assert_version 0.4.0
    [ "$("$REAL_GIT" rev-parse 'v0.4.0^{commit}')" = "$("$REAL_GIT" rev-parse HEAD)" ]
    [ "$("$REAL_GIT" diff-tree --no-commit-id --name-only -r HEAD)" = VERSION ]
    [ -z "$("$REAL_GIT" status --porcelain)" ]
    awk '/^verify / {verified=1} /^build / {if (!verified) exit 1; builds++} /^git push/ {if (builds != 3) exit 1; pushed=1} /^gh release create/ {if (!pushed) exit 1; created=1} /^gh release upload/ {if (!created) exit 1; uploaded=1} END {if (!uploaded) exit 1}' "$CASE_STATE/events"
    out=$(<"$CASE_STATE/assets")
    for target in linux_amd64 linux_arm64 darwin_arm64; do
        tar -tzf "$out/nesdit_0.4.0_${target}.tar.gz" > "$CASE_STATE/manifest"
        grep -Fxq nesdit "$CASE_STATE/manifest"
        grep -Fxq README.md "$CASE_STATE/manifest"
        grep -Fxq THIRD-PARTY-NOTICES.md "$CASE_STATE/manifest"
        tar -xOf "$out/nesdit_0.4.0_${target}.tar.gz" THIRD-PARTY-NOTICES.md > "$CASE_STATE/notice"
        cmp "$src/THIRD-PARTY-NOTICES.md" "$CASE_STATE/notice"
        grep -Fxq share/man/man1/nesdit.1 "$CASE_STATE/manifest"
    done
    mkdir "$CASE_STATE/extracted"
    tar -xzf "$out/nesdit_0.4.0_darwin_arm64.tar.gz" -C "$CASE_STATE/extracted"
    [ "$("$CASE_STATE/extracted/nesdit" --version)" = "nesdit version 0.4.0 ($("$REAL_GIT" rev-parse --short=12 HEAD))" ]
    # An existing release is never re-created or overwritten.
    before=$(wc -l < "$CASE_STATE/events")
    expect_failure bash scripts/release.sh
    [ "$(grep -c '^gh release create' "$CASE_STATE/events")" -eq 1 ]
    [ "$(grep -c '^gh release upload' "$CASE_STATE/events")" -eq 1 ]
    [ "$(wc -l < "$CASE_STATE/events")" -gt "$before" ]
done

# Fixture-local Make assignments survive isolation of the caller's overrides.
for mode in release release-cli; do
    for entry in patch:0.3.3 minor:0.4.0 2.5.8:2.5.8; do
        new_case
        commit_message 'feat!: automatic detection would select major'
        "$make_bin" -j8 "$mode" "BUMP=${entry%:*}" GO=go
        assert_version "${entry#*:}"
        [ "$("$REAL_GIT" rev-parse "v${entry#*:}^{commit}")" = "$("$REAL_GIT" rev-parse HEAD)" ]
        grep -Fxq "verify ${entry#*:}" "$CASE_STATE/events"
        [ "$(grep -c '^build ' "$CASE_STATE/events")" -eq 3 ]
        [ "$(grep -c '^gh release upload' "$CASE_STATE/events")" -eq 1 ]
    done
done

# Retry a verified existing local/remote tag when GitHub creation never happened.
new_case
commit_message 'fix: prepare retry'
bash scripts/bump-version.sh
"$REAL_GIT" add VERSION
"$REAL_GIT" commit -qm 'chore: bump version to 0.3.3'
"$REAL_GIT" tag -a v0.3.3 -m 'nesdit 0.3.3'
"$REAL_GIT" push "$CASE_STATE/remote.git" refs/tags/v0.3.3
bash scripts/release.sh
! grep -q '^git push' "$CASE_STATE/events"
assert_version 0.3.3

# Upload failure retains artifacts and refuses an automatic overwrite on retry.
new_case
commit_message 'fix: partial upload'
touch "$CASE_STATE/upload-fail"
expect_failure bash scripts/release.sh
grep -Fxq v0.3.3 "$CASE_STATE/releases"
expect_failure bash scripts/release.sh
[ "$(grep -c '^gh release upload' "$CASE_STATE/events")" -eq 1 ]

# Make overrides are data, not executable recipe fragments.
new_case
for value in 'BUMP=patch; touch injected' 'BUMP=`touch injected`'; do
    expect_failure "$make_bin" bump-version "$value"
    [ ! -e injected ]
    assert_version 0.3.2
done

# A packager never reuses an output directory, even an empty one.
new_case
mkdir "$CASE_STATE/prior-assets"
expect_failure bash scripts/package-release.sh "$CASE_STATE/prior-assets"
[ -z "$(ls -A "$CASE_STATE/prior-assets")" ]
# Uncommitted input cannot be packaged independently of the publishing guards.
echo 0.3.3 > VERSION
expect_failure bash scripts/package-release.sh "$CASE_STATE/new-assets"
[ ! -e "$CASE_STATE/new-assets" ]
# Missing reachable history and malformed version sources fail clearly.
new_case
"$REAL_GIT" tag -d v0.3.2
expect_failure bash scripts/bump-version.sh
assert_version 0.3.2
echo broken > VERSION
expect_failure bash scripts/bump-version.sh
[ "$(<VERSION)" = broken ]
echo "PASS: $case_count isolated fixtures; no real tags, pushes or GitHub calls."

#!/usr/bin/env bash
# Shared local-release checks. Callers enable errexit, nounset and pipefail.
fail() { echo "REFUSING: $*" >&2; exit 1; }

valid_version() {
    # Bound components to nine digits for portable shell arithmetic.
    [[ "$1" =~ ^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$ ]]
}

read_version() {
    [ -f VERSION ] && [ ! -L VERSION ] || fail 'VERSION must be a regular file.'
    VERSION=$(<VERSION)
    valid_version "$VERSION" || fail 'VERSION must contain a numeric major.minor.patch version.'
}

validate_bump() {
    case "$1" in
        ''|major|minor|patch) ;;
        *) valid_version "$1" || fail 'BUMP must be major, minor, patch or numeric major.minor.patch.' ;;
    esac
}

version_gt() {
    local a b i
    IFS=. read -r -a a <<< "$1"
    IFS=. read -r -a b <<< "$2"
    for i in 0 1 2; do
        if (( a[i] > b[i] )); then return 0; fi
        if (( a[i] < b[i] )); then return 1; fi
    done
    return 1
}

last_version_tag() {
    local tags tag matches=()
    tags=$(git tag --merged HEAD --list 'v*') || fail 'Cannot read local tags.'
    while IFS= read -r tag; do
        if valid_version "${tag#v}"; then matches+=(--match "$tag"); fi
    done <<< "$tags"
    if [ "${#matches[@]}" -gt 0 ]; then
        git describe --tags --abbrev=0 "${matches[@]}" HEAD
    fi
}

guard_clean() {
    local untracked
    # Even VERSION may not be pre-staged: the script owns its entire commit.
    git diff --cached --quiet || fail 'Index is not empty; commit or unstage changes first.'
    git diff --quiet -- . ':!VERSION' || fail 'Tracked files other than VERSION are dirty.'
    untracked=$(git ls-files --others --exclude-standard) || fail 'Cannot inspect untracked input.'
    [ -z "$untracked" ] || fail 'Untracked files exist; commit or move them first.'
    git ls-files --error-unmatch VERSION >/dev/null
}

guard_committed() {
    guard_clean
    git diff --quiet -- VERSION || fail 'VERSION must be committed before building.'
}

local_tag_commit() {
    local ref="refs/tags/$1"
    if git show-ref --verify --quiet "$ref"; then
        git rev-parse "$ref^{commit}" || fail 'Local tag does not resolve to a commit.'
    else
        [ "$?" -eq 1 ] || fail 'Cannot read local tag.'
    fi
}

remote_tag_commit() {
    local refs
    refs=$(git ls-remote "$REMOTE" "refs/tags/$1" "refs/tags/$1^{}") || fail 'Cannot read remote tag.'
    # Prefer the peeled commit for annotated tags, otherwise the lightweight tag.
    awk 'NR == 1 {commit=$1} /\^\{\}$/ {peeled=$1} END {print peeled ? peeled : commit}' <<< "$refs"
}

check_tag() {
    local commit="$1" expected="$2"
    [ -z "$commit" ] || [ "$commit" = "$expected" ] || fail "Tag $TAG points to $commit, not release commit $expected. Tags are never moved."
}

#!/usr/bin/env bash
# Local-only, idempotent version preparation based on the last reachable tag.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/release-common.sh
[ "$#" -le 1 ] || fail 'Usage: bump-version.sh [major|minor|patch|major.minor.patch]'
bump=${1:-${BUMP:-}}
validate_bump "$bump"
read_version
last=$(last_version_tag)
[ -n "$last" ] || fail 'No reachable numeric v* tag; fetch the release history first.'
base=${last#v}

if [ -z "$bump" ]; then
    subjects=$(git log "$last..HEAD" --format=%s)
    bodies=$(git log "$last..HEAD" --format=%B)
    if grep -Eq '^[a-z]+(\([^)]*\))?!:' <<< "$subjects" ||
        grep -Eq '^BREAKING[ -]CHANGE:' <<< "$bodies"; then
        bump=major
    elif grep -Eq '^feat(\([^)]*\))?:' <<< "$subjects"; then
        bump=minor
    else
        bump=patch
    fi
    echo "Detected $bump from $last..HEAD"
fi
IFS=. read -r major minor patch <<< "$base"
case "$bump" in
    major) next="$((major + 1)).0.0" ;;
    minor) next="$major.$((minor + 1)).0" ;;
    patch) next="$major.$minor.$((patch + 1))" ;;
    *) next=$bump ;;
esac
valid_version "$next" || fail 'Next version exceeds supported numeric range.'
version_gt "$next" "$base" || fail "Next version must be greater than $last."
if [ "$VERSION" = "$next" ]; then
    echo "Already prepared $VERSION; nothing to bump."
    exit 0
fi
# Never lower a prepared version accidentally, including with an override.
version_gt "$next" "$VERSION" || fail "Next version $next would lower prepared version $VERSION."
printf '%s\n' "$next" > VERSION
echo "Prepared $VERSION -> $next (VERSION only; no commit or tag)."

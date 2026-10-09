#!/usr/bin/env bash
# Local-only: prepare, commit VERSION, verify, package, tag, then publish.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/release-common.sh
REPO=github.com/sdkks/nesdit
REMOTE=git@github.com:sdkks/nesdit.git
validate_bump "${BUMP:-}"
guard_clean
read_version
for tool in gh git "${GO:-go}" make tar; do
    command -v "$tool" >/dev/null || fail "$tool is required."
done
gh auth status --hostname github.com
# Listing all pages distinguishes absence from auth/network errors, without
# treating a failed release view as permission to create a release.
releases=$(gh api --hostname github.com --method GET --paginate repos/sdkks/nesdit/releases --jq '.[].tag_name')
last=$(last_version_tag)
[ -n "$last" ] || fail 'No reachable numeric v* tag; fetch the release history first.'
TAG="v$VERSION"
local_commit=$(local_tag_commit "$TAG")
if [ -n "${BUMP:-}" ]; then
    bash scripts/bump-version.sh
elif [ "$local_commit" = "$(git rev-parse HEAD)" ]; then
    git diff --quiet -- VERSION || fail 'Existing tag requires committed VERSION.'
elif [ -n "$local_commit" ] && ! git diff --quiet -- VERSION; then
    fail 'Prepared VERSION conflicts with an existing tag; tags are never moved.'
elif ! version_gt "$VERSION" "${last#v}"; then
    # A committed base version with new source commits is a normal new release,
    # not an attempt to republish the old tag at a different commit.
    bash scripts/bump-version.sh
fi
read_version
TAG="v$VERSION"
head=$(git rev-parse HEAD)
local_commit=$(local_tag_commit "$TAG")
check_tag "$local_commit" "$head"
remote_commit=$(remote_tag_commit "$TAG")
check_tag "$remote_commit" "$head"
if grep -Fxq "$TAG" <<< "$releases"; then
    fail "Release $TAG already exists. No tags or assets changed; see docs/releasing.md for partial-upload recovery."
fi
# VERSION is the sole release-owned file. No unrelated staged input may enter
# the commit, and normal commit hooks must run successfully.
if ! git diff --quiet -- VERSION; then
    [ -z "$remote_commit" ] && [ -z "$local_commit" ] || fail 'Cannot commit VERSION for an existing tag.'
    guard_clean
    git add -- VERSION
    git commit -m "chore: bump version to $VERSION"
fi
guard_committed
head=$(git rev-parse HEAD)
local_commit=$(local_tag_commit "$TAG")
check_tag "$local_commit" "$head"
remote_commit=$(remote_tag_commit "$TAG")
check_tag "$remote_commit" "$head"
make test-all
scratch=$(mktemp -d "${TMPDIR:-/tmp}/nesdit-release-${VERSION}.XXXXXX")
echo "Release artifacts retained at $scratch/assets (also on failure)."
bash scripts/package-release.sh "$scratch/assets"
guard_committed
[ "$(git rev-parse HEAD)" = "$head" ] || fail 'HEAD changed during verification.'
# Recheck immediately before publication, and never force a tag push.
local_commit=$(local_tag_commit "$TAG")
check_tag "$local_commit" "$head"
remote_commit=$(remote_tag_commit "$TAG")
check_tag "$remote_commit" "$head"
if [ -z "$local_commit" ]; then git tag -a "$TAG" -m "nesdit $VERSION" "$head"; fi
if [ -z "$remote_commit" ]; then git push "$REMOTE" "refs/tags/$TAG:refs/tags/$TAG"; fi
remote_commit=$(remote_tag_commit "$TAG")
[ "$remote_commit" = "$head" ] || fail 'Remote tag did not resolve to the verified release commit.'
gh release create "$TAG" --repo "$REPO" --verify-tag --title "nesdit $VERSION" --generate-notes
assets=(
    "$scratch/assets/nesdit_${VERSION}_linux_amd64.tar.gz"
    "$scratch/assets/nesdit_${VERSION}_linux_arm64.tar.gz"
    "$scratch/assets/nesdit_${VERSION}_darwin_arm64.tar.gz"
    "$scratch/assets/nesdit_${VERSION}_checksums.txt"
)
# Intentionally no --clobber: collisions are errors, never deletion permission.
gh release upload "$TAG" "${assets[@]}" --repo "$REPO"
echo "Released $TAG from $head; artifacts retained at $scratch/assets"

#!/usr/bin/env bash
# Build committed input into a NEW directory; never overwrite prior artifacts.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/release-common.sh
[ "$#" -eq 1 ] || fail 'Usage: package-release.sh NEW_OUTPUT_DIRECTORY'
guard_committed
read_version
commit=$(git rev-parse HEAD)
short=$(git rev-parse --short=12 HEAD)
out=$1
mkdir -- "$out" # Must not exist, including a pre-existing empty directory.
out=$(cd "$out" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/nesdit-package.XXXXXX")
echo "Packaging $VERSION ($commit); staging retained at $work"
if command -v sha256sum >/dev/null; then
    checksum=(sha256sum)
else
    command -v shasum >/dev/null || fail 'sha256sum or shasum is required.'
    checksum=(shasum -a 256)
fi
assets=()
for target in linux/amd64 linux/arm64 darwin/arm64; do
    os=${target%/*}
    arch=${target#*/}
    stage="$work/${os}_${arch}"
    mkdir -p "$stage/share/man/man1"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "${GO:-go}" build -mod=readonly -trimpath \
        -ldflags="-s -w -extldflags=-static -X github.com/sdkks/nesdit/internal/run.Version=$VERSION -X github.com/sdkks/nesdit/internal/run.Commit=$short" \
        -o "$stage/nesdit" ./cmd/nesdit
    bytes=$(wc -c < "$stage/nesdit")
    [ "$bytes" -lt 16777216 ] || fail "$target binary is $bytes bytes (limit: < 16777216)."
    cp README.md "$stage/README.md"
    cp THIRD-PARTY-NOTICES.md "$stage/THIRD-PARTY-NOTICES.md"
    cp docs/nesdit.1 "$stage/share/man/man1/nesdit.1"
    asset="nesdit_${VERSION}_${os}_${arch}.tar.gz"
    tar -czf "$out/$asset" -C "$stage" nesdit README.md THIRD-PARTY-NOTICES.md share/man/man1/nesdit.1
    assets+=("$asset")
    echo "$target: $bytes bytes -> $out/$asset"
done
(cd "$out" && "${checksum[@]}" "${assets[@]}" > "nesdit_${VERSION}_checksums.txt")
guard_committed
[ "$(git rev-parse HEAD)" = "$commit" ] || fail 'HEAD changed during packaging.'
echo "Packages and SHA-256 checksums: $out"

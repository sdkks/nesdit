#!/usr/bin/env bash
set -euo pipefail

# Query only the Go settings needed for command installation.
go=${GO:-go}
destination=${GOBIN:-}
if [[ -z $destination ]]; then
    destination=$("$go" env GOBIN)
fi
if [[ -z $destination ]]; then
    gopath=$("$go" env GOPATH)
    IFS=: read -r -a roots <<< "$gopath"
    IFS=: read -r -a path_entries <<< "${PATH:-}"
    for root in "${roots[@]}"; do
        [[ -n $root ]] || continue
        while [[ $root != / && $root == */ ]]; do root=${root%/}; done
        candidate=${root%/}/bin
        for entry in "${path_entries[@]}"; do
            while [[ $entry != / && $entry == */ ]]; do entry=${entry%/}; done
            if [[ $entry == "$candidate" ]]; then
                destination=$candidate
                break 2
            fi
        done
    done
fi
if [[ -z $destination ]]; then
    destination=${HOME:?HOME must be set for the local install fallback}/.local/bin
fi

version=$(<VERSION)
if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo 'VERSION must contain a major.minor.patch version.' >&2
    exit 1
fi

# Explicit host values override both shell and saved cross-compilation settings.
host_os=$("$go" env GOHOSTOS)
host_arch=$("$go" env GOHOSTARCH)
CGO_ENABLED=0 GOOS="$host_os" GOARCH="$host_arch" GOBIN="$destination" "$go" install -mod=readonly -trimpath \
    -ldflags="-s -w -X github.com/sdkks/nesdit/internal/run.Version=$version" ./cmd/nesdit
printf 'Installed nesdit in %s\n' "$destination"

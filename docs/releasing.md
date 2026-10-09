# Local releases

CI tests changes but does not publish releases or Homebrew formulas. Local
maintainers use thin Make targets backed by `scripts/bump-version.sh` and
`scripts/release.sh`. `VERSION` is the single numeric release-version source;
ordinary development builds still report `dev`.

## Prerequisites

- Bash, Git, Make, tar, and either `sha256sum` or `shasum`.
- Go 1.27.2 or later and golangci-lint v2.14.0, as for `make test-all`.
- GitHub CLI (`gh`) authenticated to github.com with release-write access to
  `sdkks/nesdit`, and Git SSH push access to `git@github.com:sdkks/nesdit.git`.
- A complete local tag history. Fetch tags normally (`git fetch --tags origin`)
  before preparing a release. No forced fetch or tag replacement is performed.
- A clean index and working tree, including untracked files. Only an **unstaged**
  modification to `VERSION` is allowed when publishing a prepared version.
  Ignored build outputs are not release input. Commit all source changes first.

## Prepare and publish

```sh
make bump-version                 # automatic detection, local VERSION edit only
make bump-version BUMP=minor       # major, minor, or patch
make bump-version BUMP=0.4.1       # explicit numeric version
make release-cli                  # publish the prepared version, no double bump
# Alternatively, from clean committed source:
make release                      # prepare if necessary, then verify and publish
```

The two publication targets use the same sequential script and are safe with
`make -j release` or `make -j release-cli`. Do not invoke separate version and
publication targets concurrently or run multiple release processes at once.
`BUMP` can also be supplied to a publication target to choose the version.

Automatic detection considers commits since the last **reachable** numeric
`vMAJOR.MINOR.PATCH` tag. A conventional subject containing `!:` or a
`BREAKING CHANGE:` / `BREAKING-CHANGE:` body/footer selects major; a `feat:`
subject selects minor; otherwise it selects patch. Bumps are based on that tag,
not on the prepared file, so repeating the same preparation is a no-op. Explicit
versions must exceed the base tag and cannot lower a prepared version. Only
numeric stable versions are supported (no leading zeros or prerelease suffixes;
components are bounded to nine digits). The scripts do not fetch automatically.

Publication checks tools, authentication, clean input, existing releases and
local/remote tag identity. It commits **only** `VERSION`, with normal hooks, if
needed; failed hooks leave the version staged for maintainer inspection (unstage
it before retrying). It then runs `make test-all` and builds all packages from
committed source. Failed verification stops before tagging or publishing, but
may leave the version commit in local history. No branch is automatically pushed.

After verification it creates an annotated tag if absent, pushes that exact tag
without force, rechecks the remote commit, and uses `gh release create` with an
explicit repository and `--verify-tag`, followed by `gh release upload` without
`--clobber`. Existing tags must resolve to the verified release commit. Source
and HEAD are checked again after verification. Do not edit the checkout during
publication. Push the release commit to the desired branch separately afterward.

## Artifacts

Unique scratch directories are created with `mktemp`; they are never reused,
recursively cleaned, or written over pre-existing build directories. Paths are
printed and retained on success or failure for inspection and recovery.

For version `0.4.1`, GitHub receives:

- `nesdit_0.4.1_linux_amd64.tar.gz`
- `nesdit_0.4.1_linux_arm64.tar.gz`
- `nesdit_0.4.1_darwin_arm64.tar.gz`
- `nesdit_0.4.1_checksums.txt` (SHA-256 of the three archives)

Each archive contains `nesdit`, `README.md`, and
`share/man/man1/nesdit.1`. Binaries use `CGO_ENABLED=0`, `-trimpath`, stripped
symbols, and injected version/12-character commit metadata. Every binary must
be smaller than 8 MiB. No additional platform support is implied.

To package committed source without publication:

```sh
scratch=$(mktemp -d)
bash scripts/package-release.sh "$scratch/assets" # output must not already exist
```

## Retrying safely

- Failure before GitHub release creation: resolve the error and rerun
  `make release-cli`. A matching existing local or remote tag is reused, not
  moved; a prepared or tagged version is not bumped again.
- An existing GitHub release always makes the script **stop**, even when complete.
  It never silently replaces assets, modifies release notes, or creates a second
  release. API/authentication errors are errors, not evidence of absence.
- If creation succeeded but upload failed, inspect the release and the **original
  retained artifacts**. Confirm its tag still resolves to the recorded release
  commit. Upload only missing assets, explicitly, for example:

  ```sh
  gh release view v0.4.1 --repo github.com/sdkks/nesdit
  gh release upload v0.4.1 /printed/scratch/assets/nesdit_0.4.1_linux_amd64.tar.gz \
    --repo github.com/sdkks/nesdit
  ```

  Repeat only for missing assets, including the original checksum file if
  missing. Never use `--clobber`: archives rebuilt later need not have identical
  gzip/tar metadata. Immutable releases may disallow further uploads; a failure
  then requires a maintainer decision, not automatic deletion or replacement.

`make test-release` exercises version detection, safety guards, packaging and
publication calls in isolated temporary Git repositories with fake `gh` and
build commands. It performs no real network writes; fixtures and diagnostics
are retained at the printed path.

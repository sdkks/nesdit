# Installation

## go install (recommended)

Use Go 1.27.2 or later. Older Go installations with automatic toolchain selection enabled may download the required compiler; offline builds or `GOTOOLCHAIN=local` require a sufficiently new installed Go.

```sh
go install github.com/sdkks/nesdit/cmd/nesdit@latest
```

The binary is placed in `$GOPATH/bin` (usually `~/go/bin`). Make sure that directory is on your `$PATH`.

## Prebuilt binaries

Download a prebuilt binary from the [GitHub Releases page](https://github.com/sdkks/nesdit/releases).

Binaries built with the Go 1.27 baseline require macOS 13 Ventura or later, or Linux kernel 3.2 or later on amd64/arm64. Prebuilt binaries do not require a Go installation.

Archives are versioned and contain the executable, README, and manpage. Select
an available version from the releases page (the examples use `0.3.2`).

Linux (amd64; use `linux_arm64` for ARM64):

```sh
version=0.3.2
platform=linux_amd64
```

macOS (Apple Silicon):

```sh
version=0.3.2
platform=darwin_arm64
```

Then download and verify the chosen archive in a fresh directory:

```sh
scratch=$(mktemp -d)
cd "$scratch"
asset="nesdit_${version}_${platform}.tar.gz"
base="https://github.com/sdkks/nesdit/releases/download/v${version}"
curl -fLO "$base/$asset"
curl -fLO "$base/nesdit_${version}_checksums.txt"
grep "  ${asset}$" "nesdit_${version}_checksums.txt" > checksum.txt
# macOS (Linux can use: sha256sum -c checksum.txt)
shasum -a 256 -c checksum.txt
# Continue only if checksum verification succeeds:
tar -xzf "$asset"
install -m 755 nesdit /usr/local/bin/nesdit
# Optional manpage installation (may require elevated permissions):
# install -m 644 share/man/man1/nesdit.1 /usr/local/share/man/man1/nesdit.1
```

Installation into `/usr/local/bin` may require elevated permissions; alternatively,
install into a user-owned directory on your `PATH`. Homebrew publication is not
part of the local release workflow.

## Verify the installation

```sh
nesdit --version
```

Expected output (version number will vary):

```
nesdit version 0.4.1 (0123456789ab)
```

Quick smoke-test:

```sh
echo '{"ok":true}' | nesdit --format json --query '.ok'
```

Expected output:

```
true
```

## Next steps

- [Quickstart](quickstart.md) — make your first edit in under 5 minutes.
- [Modes](modes/in-place.md) — learn all the run modes available.
- [Examples](examples/formats/index.md) — exhaustive examples for every format and use case.

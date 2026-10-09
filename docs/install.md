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

Linux (amd64):

```sh
curl -Lo nesdit "https://github.com/sdkks/nesdit/releases/latest/download/nesdit-linux-amd64"
chmod +x nesdit
mv nesdit /usr/local/bin/
```

macOS (arm64):

```sh
curl -Lo nesdit "https://github.com/sdkks/nesdit/releases/latest/download/nesdit-darwin-arm64"
chmod +x nesdit
mv nesdit /usr/local/bin/
```

## Homebrew

!!! note
    Homebrew support is planned for a future release. For now, use `go install` or the prebuilt binary.

## Verify the installation

```sh
nesdit --version
```

Expected output (version number will vary):

```
nesdit version v0.1.0
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

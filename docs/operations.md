# Operations

`entire-sem` is a local Entire CLI plugin. The operational surface is intentionally
small: build the provider, install the local executable into Entire's plugin
directory, and generate checksum-backed release archives.

## Local Install

```sh
scripts/install-local.sh
```

The script builds `./entire-sem`, installs it with `entire plugin install
./entire-sem --force`, and prints `entire sem version`. It fails before writing
anything if the parent `entire` CLI is not on `PATH`.

## Release Archives

```sh
scripts/release.sh
```

The release script writes `dist/release-<version>/` with one `.tar.gz` archive per
target and a `SHA256SUMS` manifest. `VERSION=<value>` overrides the version;
otherwise the script uses `git describe --tags --always --dirty`.

By default the script builds the current host target. Set `ENTIRE_RELEASE_TARGETS`
to a space-separated list of `GOOS/GOARCH` targets to request more builds:

```sh
ENTIRE_RELEASE_TARGETS="darwin/arm64 linux/amd64" scripts/release.sh
```

`entire-sem` includes native tree-sitter parser bindings, so cross-platform
artifacts require the matching cgo-capable compiler/toolchain for each requested
target. The script records checksums for artifacts it successfully builds; it
does not sign artifacts or publish them.

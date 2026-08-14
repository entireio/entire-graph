# Operations

`entire-graph` is a local Entire CLI plugin. The operational surface is intentionally
small: build the provider, install the local executable into Entire's plugin
directory, and generate checksum-backed release archives.

## Requirements

- The Entire CLI and Git must be on `PATH` for local installation.
- Development uses Go 1.26. Tree-sitter bindings require CGO and a working C
  compiler for the target platform.
- Release archives require `tar` and `shasum`. Signing additionally requires
  either `cosign` or `gpg` and a configured local key.

## Local Install

```sh
scripts/install-local.sh
```

The script builds `./entire-graph`, installs it with `entire plugin install
./entire-graph --force`, and prints `entire graph version`. It fails before writing
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

`entire-graph` includes native tree-sitter parser bindings, so cross-platform
artifacts require the matching cgo-capable compiler/toolchain for each requested
target. The script records checksums for artifacts it successfully builds; it
also signs archives when a local signing key is explicitly configured:

- `COSIGN_KEY=<key-ref>` with `cosign` on `PATH` writes `<archive>.sig`.
- `GPG_SIGNING_KEY=<key-id>` with `gpg` on `PATH` writes `<archive>.asc`.

If both signing variables are set and both tools are available, cosign takes
precedence and the script writes only the `.sig` file.

The script does not publish artifacts.

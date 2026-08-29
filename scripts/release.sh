#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || printf 'dev')}
asset_version=${version#v}
out_dir=${OUT_DIR:-dist/release-$version}
targets=${ENTIRE_RELEASE_TARGETS:-$(go env GOOS)/$(go env GOARCH)}

mkdir -p "$out_dir"
out_dir=$(CDPATH= cd -- "$out_dir" && pwd)
rm -f "$out_dir"/checksums.txt "$out_dir"/SHA256SUMS

checksum_artifact() {
	artifact_name=${1##*/}
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$out_dir" && sha256sum "$artifact_name")
		return
	fi
	if command -v shasum >/dev/null 2>&1; then
		(cd "$out_dir" && shasum -a 256 "$artifact_name")
		return
	fi
	printf 'neither sha256sum nor shasum is available\n' >&2
	return 1
}

archive_windows() {
	work=$1
	archive=$2
	if command -v zip >/dev/null 2>&1; then
		(cd "$work" && zip -q -r "$archive" .)
		return
	fi
	if command -v 7z >/dev/null 2>&1; then
		(cd "$work" && 7z a -bd -y -tzip "$archive" . >/dev/null)
		return
	fi
	printf 'neither zip nor 7z is available for a Windows archive\n' >&2
	return 1
}

build_target() {
	goos=${1%/*}
	goarch=${1#*/}
	if [ "$goos" = "$goarch" ] || [ -z "$goos" ] || [ -z "$goarch" ]; then
		printf 'invalid target %s; expected GOOS/GOARCH\n' "$1" >&2
		return 1
	fi

	bin="entire-graph"
	case "$goos" in
		windows) bin="$bin.exe" ;;
	esac

	asset="entire-graph_${asset_version}_${goos}_${goarch}"
	work="$out_dir/$asset"
	archive="$out_dir/$asset.tar.gz"
	if [ "$goos" = "windows" ]; then
		archive="$out_dir/$asset.zip"
	fi
	rm -f "$archive" "$archive.sig" "$archive.asc"
	rm -rf "$work"
	mkdir -p "$work"

	printf 'building %s/%s\n' "$goos" "$goarch" >&2
	cgo_ldflags=$(go env CGO_LDFLAGS)
	if [ "$goos" = "windows" ]; then
		# Release archives cannot assume a MinGW runtime is installed beside the binary.
		case " $cgo_ldflags " in
			*" -static "*) ;;
			*) cgo_ldflags="${cgo_ldflags:+$cgo_ldflags }-static" ;;
		esac
	fi
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="${CGO_ENABLED:-1}" CGO_LDFLAGS="$cgo_ldflags" \
		go build -trimpath -ldflags "-s -w -X main.version=$version" \
		-o "$work/$bin" ./cmd/entire-graph

	cp README.md LICENSE entire-plugin.yml "$work/"
	if [ "$goos" = "windows" ]; then
		archive_windows "$work" "$archive"
	else
		tar -C "$work" -czf "$archive" .
	fi
	rm -rf "$work"
	checksum_artifact "$archive" >> "$out_dir/checksums.txt"
	sign_artifact "$archive"
}

sign_artifact() {
	artifact=$1
	if [ -n "${COSIGN_KEY:-}" ] && command -v cosign >/dev/null 2>&1; then
		cosign sign-blob --yes --key "$COSIGN_KEY" --output-signature "$artifact.sig" "$artifact"
		return
	fi
	if [ -n "${GPG_SIGNING_KEY:-}" ] && command -v gpg >/dev/null 2>&1; then
		gpg --batch --yes --armor --detach-sign --local-user "$GPG_SIGNING_KEY" --output "$artifact.asc" "$artifact"
	fi
}

for target in $targets; do
	build_target "$target"
done

printf 'wrote %s\n' "$out_dir" >&2

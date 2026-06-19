#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || printf 'dev')}
out_dir=${OUT_DIR:-dist/release-$version}
targets=${ENTIRE_RELEASE_TARGETS:-$(go env GOOS)/$(go env GOARCH)}

mkdir -p "$out_dir"
rm -f "$out_dir"/SHA256SUMS

build_target() {
	goos=${1%/*}
	goarch=${1#*/}
	if [ "$goos" = "$goarch" ] || [ -z "$goos" ] || [ -z "$goarch" ]; then
		printf 'invalid target %s; expected GOOS/GOARCH\n' "$1" >&2
		return 1
	fi

	bin="entire-sem"
	case "$goos" in
		windows) bin="$bin.exe" ;;
	esac

	work="$out_dir/entire-sem-$version-$goos-$goarch"
	archive="$out_dir/entire-sem-$version-$goos-$goarch.tar.gz"
	rm -rf "$work"
	mkdir -p "$work"

	printf 'building %s/%s\n' "$goos" "$goarch" >&2
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="${CGO_ENABLED:-1}" \
		go build -trimpath -ldflags "-s -w -X main.version=$version" \
		-o "$work/$bin" ./cmd/entire-sem

	cp README.md LICENSE entire-plugin.yml "$work/"
	tar -C "$out_dir" -czf "$archive" "entire-sem-$version-$goos-$goarch"
	rm -rf "$work"
	shasum -a 256 "$archive" >> "$out_dir/SHA256SUMS"
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

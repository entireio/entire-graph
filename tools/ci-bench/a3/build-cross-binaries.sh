#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: build-cross-binaries.sh --repo PATH --output PATH [--repetitions N]

Cross-compiles every package test binary for windows/amd64 with CGO enabled.
Each repetition starts from an identical empty build-cache seed; the module
cache is shared and must already contain the pinned module graph.
EOF
}

repo=""
output=""
repetitions=1
while (($#)); do
	case "$1" in
	--repo) repo=${2:?}; shift 2 ;;
	--output) output=${2:?}; shift 2 ;;
	--repetitions) repetitions=${2:?}; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

[[ -n "$repo" && -n "$output" ]] || { usage >&2; exit 2; }
[[ "$repetitions" =~ ^[1-9][0-9]*$ ]] || { printf 'invalid repetition count\n' >&2; exit 2; }

repo=$(cd "$repo" && pwd -P)
mkdir -p "$output"
output=$(cd "$output" && pwd -P)
helper_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)

export GOOS=windows
export GOARCH=amd64
export CGO_ENABLED=1
export CC=${CC:-x86_64-w64-mingw32-gcc}
export CXX=${CXX:-x86_64-w64-mingw32-g++}
# Several Windows tests copy their own test executable into an isolated helper
# directory and replace PATH before re-execing it. Statically link the MinGW
# support runtime so those exact child PEs cannot silently load a host DLL (or
# fail before TestMain) after they leave the artifact directory.
export CGO_LDFLAGS=${CGO_LDFLAGS:--static -static-libgcc -static-libstdc++}

[[ "$(git -C "$repo" rev-parse HEAD)" == "ee6468a6a49d9b2a1a828bd276792f415f392185" ]] || {
	printf 'product checkout is not pinned to ee6468a6\n' >&2
	exit 1
}
[[ "$(go env GOVERSION)" == "go1.26.7" ]] || {
	printf 'Go must be go1.26.7, got %s\n' "$(go env GOVERSION)" >&2
	exit 1
}
command -v "$CC" >/dev/null
command -v "$CXX" >/dev/null
command -v x86_64-w64-mingw32-objdump >/dev/null
command -v python3 >/dev/null
command -v zip >/dev/null

gomodcache=${A3_GOMODCACHE:-$output/gomodcache}
seed_cache=${A3_SEED_GOCACHE:-$output/seed-gocache}
mkdir -p "$gomodcache" "$seed_cache"
export GOMODCACHE=$gomodcache

(
	cd "$repo"
	GOCACHE="$output/module-download-cache" go mod download
	GOCACHE="$output/inventory-cache" go list -json ./... >"$output/target-go-list.raw.json"
)
python3 "$helper_dir/compare-evidence.py" normalize-target \
	--input "$output/target-go-list.raw.json" \
	--output "$output/target-files.cross.json"

(
	cd "$repo"
	GOCACHE="$output/inventory-cache" go list \
		-f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{"\t"}}{{.Dir}}{{end}}' ./...
) | awk 'NF' | sort >"$output/test-packages.tsv"

seed_listing="$output/seed-cache-files.txt"
(
	cd "$seed_cache"
	find . -type f -print0 | sort -z | xargs -0 shasum -a 256
) >"$seed_listing"
seed_sha=$(shasum -a 256 "$seed_listing" | awk '{print $1}')

go version >"$output/go-version.txt"
go env -json >"$output/go-env.json"
"$CC" --version >"$output/cross-gcc-version.txt"
"$CXX" --version >"$output/cross-gxx-version.txt"
uname -a >"$output/uname.txt"

for repetition in $(seq 1 "$repetitions"); do
	run=$(printf 'cross-%02d' "$repetition")
	run_dir="$output/$run"
	run_cache="$output/cache-$run"
	rm -rf "$run_cache"
	cp -a "$seed_cache" "$run_cache"
	mkdir -p "$run_dir/binaries" "$run_dir/pe" "$run_dir/build-metadata"
	cp "$output/target-files.cross.json" "$run_dir/target-files.cross.json"
	cp "$output/test-packages.tsv" "$run_dir/test-packages.tsv"

	monitor_pids=()
	if command -v mpstat >/dev/null; then
		mpstat -P ALL 1 >"$run_dir/mpstat.txt" & monitor_pids+=("$!")
	fi
	if command -v iostat >/dev/null; then
		iostat -dx 1 >"$run_dir/iostat.txt" & monitor_pids+=("$!")
	fi

	started_ns=$(date +%s%N)
	index=0
	: >"$run_dir/compile-metrics.tsv"
	: >"$run_dir/package-manifest.tsv"
	while IFS=$'\t' read -r package package_dir; do
		index=$((index + 1))
		binary=$(printf 'p%03d.test.exe' "$index")
		exe="$run_dir/binaries/$binary"
		package_relative=${package_dir#"$repo"/}
		compile_start=$(date +%s%N)
		set +e
		(
			cd "$repo"
			GOCACHE="$run_cache" /usr/bin/time -v -o "$run_dir/build-metadata/$binary.time.txt" \
				go test -vet=off -c "$package" -o "$exe"
		) >"$run_dir/build-metadata/$binary.stdout.txt" \
			2>"$run_dir/build-metadata/$binary.stderr.txt"
		compile_exit=$?
		set -e
		compile_end=$(date +%s%N)
		compile_ms=$(((compile_end - compile_start) / 1000000))
		printf '%s\t%s\t%s\t%s\t%s\n' \
			"$package" "$binary" "$compile_ms" "$compile_exit" \
			"$(if [[ -f "$exe" ]]; then stat -c %s "$exe"; else printf 0; fi)" \
			>>"$run_dir/compile-metrics.tsv"
		[[ "$compile_exit" == 0 && -f "$exe" ]] || exit "$compile_exit"
		go version -m "$exe" >"$run_dir/pe/$binary.go-version-m.txt"
		x86_64-w64-mingw32-objdump -p "$exe" >"$run_dir/pe/$binary.objdump.txt"
		sha=$(shasum -a 256 "$exe" | awk '{print $1}')
		printf '%s\t%s\t%s\t%s\n' "$package" "$package_relative" "$binary" "$sha" \
			>>"$run_dir/package-manifest.tsv"
	done <"$output/test-packages.tsv"
	: >"$run_dir/runtime-dlls.tsv"
	if grep -h 'DLL Name:' "$run_dir"/pe/*.objdump.txt | grep -Eiq 'DLL Name: (libgcc|libstdc\+\+|libwinpthread).*\.dll'; then
		printf 'static MinGW runtime link still imports a MinGW support DLL\n' >&2
		exit 1
	fi
	ended_ns=$(date +%s%N)
	for pid in "${monitor_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
	wait "${monitor_pids[@]}" 2>/dev/null || true

	total_ms=$(((ended_ns - started_ns) / 1000000))
	python3 - "$run_dir" "$run" "$total_ms" "$seed_sha" <<'PY'
import csv, json, pathlib, subprocess, sys
root, run, total_ms, seed_sha = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3]), sys.argv[4]
metrics = {}
with (root / "compile-metrics.tsv").open(encoding="utf-8") as stream:
    for package, binary, duration_ms, exit_code, size in csv.reader(stream, delimiter="\t"):
        metrics[package] = {"durationMilliseconds": int(duration_ms), "exitCode": int(exit_code), "binaryBytes": int(size)}
packages = []
with (root / "package-manifest.tsv").open(encoding="utf-8") as stream:
    for package, relative_dir, binary, sha256 in csv.reader(stream, delimiter="\t"):
        argv = [str((root / "binaries" / binary).resolve()), "-test.v=test2json", "-test.timeout=30m"]
        argv_chars = len(subprocess.list2cmdline(argv)) + 1
        if argv_chars > 30000:
            raise SystemExit(f"Windows argv preflight failed for {package}: {argv_chars}")
        packages.append({"importPath": package, "packageDirectory": relative_dir, "binary": f"binaries/{binary}", "sha256": sha256, "argvCharacters": argv_chars, "compile": metrics[package]})
runtime_dlls = []
with (root / "runtime-dlls.tsv").open(encoding="utf-8") as stream:
    for name, sha256, size in csv.reader(stream, delimiter="\t"):
        runtime_dlls.append({"name": name, "path": f"binaries/{name}", "sha256": sha256, "bytes": int(size)})
payload = {
    "schema": "entire-graph.windows-ci.a3.cross-artifact.v1",
    "run": run,
    "repositorySha": "ee6468a6a49d9b2a1a828bd276792f415f392185",
    "goVersion": "go1.26.7",
    "target": {"goos": "windows", "goarch": "amd64", "cgoEnabled": "1"},
    "runtimeLinkage": "static-mingw-support",
    "cgoLdflags": "-static -static-libgcc -static-libstdc++",
    "seedBuildCacheSha256": seed_sha,
    "compileDurationMilliseconds": total_ms,
    "packageCount": len(packages),
    "packages": packages,
    "runtimeDlls": runtime_dlls,
}
(root / "manifest.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
	(
		cd "$run_dir"
		zip -q -r "$output/$run.zip" .
	)
	shasum -a 256 "$output/$run.zip" >"$output/$run.zip.sha256"
done

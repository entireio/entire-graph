#!/usr/bin/env python3
import base64
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile


HERE = Path(__file__).resolve().parent
TEMPLATE = (HERE / "remote-launcher-template.sh").read_text()
LIVE_TESTS = [
    "TestLiveCompilerDirectAndCandidate",
    "TestLiveCompilerMissingDependenciesAndCancellation",
    "TestLiveCompilerWorkspaceAliases",
    "TestLiveCompilerTagsReplacementAndClosure",
    "TestLiveCompilerSignatureAndWorkspaceInvalidation",
    "TestLiveCompilerProcessBoundary",
    "TestLiveCompilerSemanticMapping",
    "TestLiveCompilerConversionsAreNotCalls",
    "TestLiveAdvantageCombinationsAndNextQueryFreshness",
    "TestLiveReviewCompilerOrdinaryQueries",
]


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_executable(path: Path, body: str) -> None:
    path.write_text(body)
    path.chmod(0o755)


def run_case(failure: str, test_output: str = "") -> tuple[int, bool, bool]:
    with tempfile.TemporaryDirectory(prefix="effa-compiler-gate-") as tmp:
        temp = Path(tmp)
        source = temp / "source"
        source.mkdir()
        tracked = source / "fixture.go"
        tracked.write_text("package fixture\n")
        archive = temp / "source.tar.gz"
        archive.write_bytes(b"retained-source-archive")
        manifest = temp / "tracked-manifest.tsv"
        manifest.write_text(f"100644\t{sha256(tracked)}\tfixture.go\n")
        root = temp / "result"
        tools = temp / "tools"
        tools.mkdir()
        correctness_marker = temp / "correctness-reached"
        build_marker = temp / "build-reached"
        output = temp / "go-test-output.txt"
        output.write_text(test_output)

        go_version = "go version go1.26.1 linux/amd64"
        if failure == "go_version":
            go_version = "go version go0.0.0 linux/amd64"
        write_executable(
            tools / "go",
            "#!/bin/bash\n"
            "if [ \"$1\" = version ]; then\n"
            "  if [ \"${2-}\" = -m ]; then\n"
            "    [ \"$FAKE_BUILD_MODE\" = info_fail ] && exit 1\n"
            "    echo 'fake build info'\n"
            f"  else echo '{go_version}'; fi\n"
            "  exit 0\n"
            "fi\n"
            "is_build=false\n"
            "for arg in \"$@\"; do [ \"$arg\" = -c ] && is_build=true; done\n"
            "if [ \"$is_build\" = true ]; then\n"
            "  : > \"$BUILD_MARKER\"\n"
            "  if [ \"$FAKE_BUILD_MODE\" != binary_missing ]; then\n"
            "    while [ \"$#\" -gt 0 ]; do\n"
            "      if [ \"$1\" = -o ]; then shift; printf fake-binary > \"$1\"; break; fi\n"
            "      shift\n"
            "    done\n"
            "  fi\n"
            "else\n"
            "  : > \"$CORRECTNESS_MARKER\"\n"
            "  if [ \"$FAKE_OUTPUT_MODE\" != missing ]; then\n"
            "    printf '{}\\n' > \"$ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT\"\n"
            "    printf '{}\\n' > \"$ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT\"\n"
            "  fi\n"
            "  cat \"$FAKE_GO_OUTPUT\"\n"
            "fi\n",
        )
        write_executable(
            tools / "gopls",
            "#!/bin/bash\necho 'golang.org/x/tools/gopls v0.20.0'\n",
        )
        write_executable(tools / "bwrap", "#!/bin/bash\necho 'bubblewrap test'\n")
        write_executable(
            tools / "readlink",
            "#!/bin/bash\n[ \"${1-}\" = -f ] && shift\nexec /bin/realpath \"$1\"\n",
        )

        valid_url = (
            "${GRAPH_ADVANTAGE_BLOB_URL}"
            "diagnostics-linux-effa358f-compiler-01/results.tar.gz?dummy=write"
        )
        rendered = TEMPLATE.replace(
            "__RESULT_URL_B64__", base64.b64encode(valid_url.encode()).decode()
        )
        replacements = {
            "/opt/graph-validation/check-effa358f-linux-full-01/src": str(source),
            "/opt/graph-validation/check-effa358f-linux-full-01/input/source.tar.gz": str(archive),
            "/opt/graph-validation/check-effa358f-linux-full-01/input/tracked-manifest.tsv": str(manifest),
            "/opt/graph-validation/diagnostics-linux-effa358f-compiler-01": str(root),
            "/usr/local/go/bin/go": str(tools / "go"),
            "/usr/local/go/bin": str(tools),
            "/opt/graph-tools/gopls": str(tools / "gopls"),
            "/usr/bin/bwrap": str(tools / "bwrap"),
            "test \"$manifest_count\" -eq 2512": "test \"$manifest_count\" -eq 1",
            "base64 -d": "base64 -D",
            "sha256sum": "shasum -a 256",
            "/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file \"$ROOT/results.tar.gz\" \"$RESULT_URL\"": "true",
        }
        for old, new in replacements.items():
            rendered = rendered.replace(old, new)
        source_sha = sha256(archive)
        go_sha = sha256(tools / "go")
        if failure == "source_hash":
            source_sha = "0" * 64
        if failure == "go_hash":
            go_sha = "0" * 64
        rendered = rendered.replace(
            "81c0ba6ba6f58c61270a6f70517b6ed23db806f1e64852ab5c69c9649a45fed3",
            source_sha,
        ).replace(
            "ac4cf87f80b0140d1e9b0ebb21c5a8350a90d7e2e3e22d74ad073b79a33ef267",
            sha256(manifest),
        ).replace(
            "548e61b2d08ae52043be2f1924ed3c1d2b2c41967e360f3e317667f6fa912fc2",
            go_sha,
        ).replace(
            "2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1",
            sha256(tools / "gopls"),
        )
        script = temp / "runner.sh"
        write_executable(script, rendered)
        env = os.environ.copy()
        env.update(
            CORRECTNESS_MARKER=str(correctness_marker),
            BUILD_MARKER=str(build_marker),
            FAKE_GO_OUTPUT=str(output),
            FAKE_BUILD_MODE=(failure if failure in {"binary_missing", "info_fail"} else "ok"),
            FAKE_OUTPUT_MODE=("missing" if failure == "live_output_missing" else "ok"),
        )
        completed = subprocess.run([str(script)], env=env, capture_output=True, text=True)
        return completed.returncode, correctness_marker.exists(), build_marker.exists()


def main() -> None:
    cases = {
        "source_hash": 74,
        "go_hash": 75,
        "go_version": 75,
    }
    for failure, expected in cases.items():
        code, correctness, build = run_case(failure)
        assert code == expected, (failure, code)
        assert not correctness and not build, (failure, correctness, build)

    lines = [f"--- PASS: {name} (0.00s)" for name in LIVE_TESTS[:-1]]
    lines.extend(f"--- PASS: TestPinnedCompilerFiller{i:02d} (0.00s)" for i in range(19))
    lines.append(f"--- SKIP: {LIVE_TESTS[-1]} (0.00s)")
    code, correctness, build = run_case("live_skip", "\n".join(lines) + "\n")
    assert code == 78, code
    assert correctness and not build, (correctness, build)

    passing = [f"--- PASS: {name} (0.00s)" for name in LIVE_TESTS]
    passing.extend(f"--- PASS: TestPinnedCompilerFiller{i:02d} (0.00s)" for i in range(18))
    passing_output = "\n".join(passing) + "\n"
    code, correctness, build = run_case("live_output_missing", passing_output)
    assert code == 78, code
    assert correctness and not build, (correctness, build)

    for failure in ("binary_missing", "info_fail"):
        code, correctness, build = run_case(failure, passing_output)
        assert code == 79, (failure, code)
        assert correctness and build, (failure, correctness, build)
    code, correctness, build = run_case("ok", passing_output)
    assert code == 0, code
    assert correctness and build, (correctness, build)
    print("7 negative gate cases and 1 positive control passed")


if __name__ == "__main__":
    main()

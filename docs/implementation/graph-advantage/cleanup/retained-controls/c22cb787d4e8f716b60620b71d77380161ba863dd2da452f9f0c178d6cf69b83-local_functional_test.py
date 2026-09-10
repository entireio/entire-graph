#!/usr/bin/env python3
import base64
import hashlib
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path

template = Path(__file__).with_name("remote-template.sh").read_text(encoding="utf-8")

def bind(text: str, result_url: str | None, source_url: str | None) -> str:
    if result_url is None or source_url is None:
        return text
    result_encoded = base64.b64encode(result_url.encode()).decode()
    source_encoded = base64.b64encode(source_url.encode()).decode()
    return text.replace("__RESULT_URL_B64__", result_encoded).replace("__SOURCE_URL_B64__", source_encoded)

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    fakebin = root / "bin"
    source = root / "source"
    fakebin.mkdir()
    (source / "internal/sem").mkdir(parents=True)
    fixture = source / "internal/sem/fixture.go"
    fixture.write_text("package sem\n", encoding="utf-8")
    archive = root / "uploaded-source.tar.gz"
    with tarfile.open(archive, "w:gz") as handle:
        handle.add(source / "internal", arcname="internal")
    archive_sha = hashlib.sha256(archive.read_bytes()).hexdigest()
    calls = root / "calls.log"
    go = fakebin / "go"
    go.write_text("#!/bin/sh\nprintf 'go %s\\n' \"$*\" >> \"$FAKE_CALL_LOG\"\nif [ \"$1\" = version ]; then echo \"${FAKE_GO_VERSION:-go version go1.26.1 linux/amd64}\"; exit 0; fi\nprintf '%s\\n' '--- PASS: TestPreindexProviderSnapshotReusesSameTreeAcrossCommits (0.01s)' 'PASS'\n", encoding="utf-8")
    gopls = fakebin / "gopls"
    gopls.write_text("#!/bin/sh\nprintf 'gopls %s\\n' \"$*\" >> \"$FAKE_CALL_LOG\"\necho 'golang.org/x/tools/gopls v0.20.0'\n", encoding="utf-8")
    curl = fakebin / "curl"
    curl.write_text("""#!/bin/sh
output=''
is_upload=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    -X) [ "$2" = PUT ] && is_upload=true; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$is_upload" = true ]; then printf 'curl upload\\n' >> "$FAKE_CALL_LOG"; exit 0; fi
printf 'curl download\\n' >> "$FAKE_CALL_LOG"
cp "$FAKE_SOURCE_ARCHIVE" "$output"
""", encoding="utf-8")
    for executable in (go, gopls, curl): executable.chmod(0o755)

    transformed = template.replace("SOURCE_ROOT=/opt/graph-validation/diagnostics-linux-6f23da0a/src", f"SOURCE_ROOT={source}")
    durable_archive = root / "durable-source.tar.gz"
    transformed = transformed.replace("SOURCE_ARCHIVE=/opt/graph-validation/check-6f23da0a-linux-cache-fixtures-r2-source.tar.gz", f"SOURCE_ARCHIVE={durable_archive}")
    transformed = transformed.replace("EXPECTED_SOURCE_SHA=e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b", f"EXPECTED_SOURCE_SHA={archive_sha}")
    transformed = transformed.replace("ROOT=/opt/graph-validation/check-6f23da0a-linux-cache-fixtures-r2", f"ROOT={root / 'result'}")
    transformed = transformed.replace("export PATH=/usr/local/go/bin:/usr/bin:/bin", f"export PATH={fakebin}:/usr/bin:/bin")
    transformed = transformed.replace("/usr/bin/curl", str(curl))
    transformed = transformed.replace("/opt/graph-tools/gopls", str(gopls)).replace("/usr/local/go/bin/go", str(go))
    cases = []
    inputs = (
        ("valid", None, False, False, 0, 1, 1),
        ("wrong_source_hash", None, False, True, 1, 0, 1),
        ("wrong_go", "go version go9.99.0 linux/amd64", False, False, 76, 0, 1),
        ("changed_source", None, True, False, 75, 0, 1),
    )
    result_url = "${GRAPH_ADVANTAGE_BLOB_URL}"
    source_url = "${GRAPH_ADVANTAGE_BLOB_URL}"
    for name, fake_version, mutate_source, wrong_hash, expected_exit, expected_tests, expected_uploads in inputs:
        calls.write_text("", encoding="utf-8")
        fixture.write_text("package sem\n", encoding="utf-8")
        if mutate_source:
            fixture.write_text("package sem\n// changed\n", encoding="utf-8")
        environment = dict(os.environ, FAKE_CALL_LOG=str(calls), FAKE_SOURCE_ARCHIVE=str(archive))
        if fake_version is not None:
            environment["FAKE_GO_VERSION"] = fake_version
        case_script = transformed
        if wrong_hash:
            case_script = case_script.replace(f"EXPECTED_SOURCE_SHA={archive_sha}", "EXPECTED_SOURCE_SHA=" + "0" * 64)
        script = bind(case_script, result_url, source_url)
        completed = subprocess.run(["bash"], input=script, text=True, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        call_lines = calls.read_text(encoding="utf-8").splitlines()
        cases.append((name, completed.returncode, sum(line.startswith("go ") for line in call_lines), sum(line.startswith("go test ") for line in call_lines), sum(line == "curl upload" for line in call_lines)))
        if not (completed.returncode == expected_exit and cases[-1][3] == expected_tests and cases[-1][4] == expected_uploads): raise SystemExit(f"{name} binding/source/toolchain result differed: {cases[-1]} stderr={completed.stderr!r}")
        if name in ("wrong_source_hash", "changed_source") and cases[-1][2] != 0: raise SystemExit(f"{name} unexpectedly invoked fake Go")
        result = root / "result"
        if result.exists():
            import shutil
            shutil.rmtree(result)
        durable_archive.unlink(missing_ok=True)
    for case in cases:
        print("case=%s exit=%d fake_go_calls=%d fake_go_test_calls=%d fake_upload_calls=%d" % case)

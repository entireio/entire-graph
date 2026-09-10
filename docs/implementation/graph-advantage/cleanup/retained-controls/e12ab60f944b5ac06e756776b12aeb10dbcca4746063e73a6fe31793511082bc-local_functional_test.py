#!/usr/bin/env python3
import base64
import hashlib
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path

template = Path(__file__).with_name("remote-template.sh").read_text(encoding="utf-8")

def bind(text: str, url: str | None) -> str:
    if url is None:
        return text
    encoded = base64.b64encode(url.encode()).decode()
    return text.replace("__RESULT_URL_B64__", encoded)

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    fakebin = root / "bin"
    source = root / "source"
    fakebin.mkdir()
    (source / "internal/sem").mkdir(parents=True)
    fixture = source / "internal/sem/fixture.go"
    fixture.write_text("package sem\n", encoding="utf-8")
    archive = root / "source.tar.gz"
    with tarfile.open(archive, "w:gz") as handle:
        handle.add(source / "internal", arcname="internal")
    archive_sha = hashlib.sha256(archive.read_bytes()).hexdigest()
    calls = root / "calls.log"
    go = fakebin / "go"
    go.write_text("#!/bin/sh\nprintf 'go %s\\n' \"$*\" >> \"$FAKE_CALL_LOG\"\nif [ \"$1\" = version ]; then echo \"${FAKE_GO_VERSION:-go version go1.26.1 linux/amd64}\"; exit 0; fi\nprintf '%s\\n' '--- PASS: TestPreindexProviderSnapshotReusesSameTreeAcrossCommits (0.01s)' 'PASS'\n", encoding="utf-8")
    gopls = fakebin / "gopls"
    gopls.write_text("#!/bin/sh\nprintf 'gopls %s\\n' \"$*\" >> \"$FAKE_CALL_LOG\"\necho 'golang.org/x/tools/gopls v0.20.0'\n", encoding="utf-8")
    curl = fakebin / "curl"
    curl.write_text("#!/bin/sh\nprintf 'curl upload\\n' >> \"$FAKE_CALL_LOG\"\nexit 0\n", encoding="utf-8")
    for executable in (go, gopls, curl): executable.chmod(0o755)

    transformed = template.replace("SOURCE_ROOT=/opt/graph-validation/diagnostics-linux-6f23da0a/src", f"SOURCE_ROOT={source}")
    transformed = transformed.replace("SOURCE_ARCHIVE=/tmp/diagnostics-linux-6f23da0a-source.tar.gz", f"SOURCE_ARCHIVE={archive}")
    transformed = transformed.replace("EXPECTED_SOURCE_SHA=e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b", f"EXPECTED_SOURCE_SHA={archive_sha}")
    transformed = transformed.replace("ROOT=/opt/graph-validation/check-6f23da0a-linux-cache-fixtures-r1", f"ROOT={root / 'result'}")
    transformed = transformed.replace("export PATH=/usr/local/go/bin:/usr/bin:/bin", f"export PATH={fakebin}:/usr/bin:/bin")
    transformed = transformed.replace("/opt/graph-tools/gopls", str(gopls)).replace("/usr/local/go/bin/go", str(go))
    cases = []
    inputs = (
        ("valid", "${GRAPH_ADVANTAGE_BLOB_URL}", None, False, 0, 1, 1),
        ("missing", None, None, False, 72, 0, 0),
        ("wrong_host", "https://example.invalid/results.tar.gz?dummy=write", None, False, 72, 0, 0),
        ("wrong_go", "${GRAPH_ADVANTAGE_BLOB_URL}", "go version go9.99.0 linux/amd64", False, 76, 0, 1),
        ("changed_source", "${GRAPH_ADVANTAGE_BLOB_URL}", None, True, 75, 0, 1),
    )
    for name, url, fake_version, mutate_source, expected_exit, expected_tests, expected_uploads in inputs:
        calls.write_text("", encoding="utf-8")
        fixture.write_text("package sem\n", encoding="utf-8")
        if mutate_source:
            fixture.write_text("package sem\n// changed\n", encoding="utf-8")
        environment = dict(os.environ, FAKE_CALL_LOG=str(calls))
        if fake_version is not None:
            environment["FAKE_GO_VERSION"] = fake_version
        script = bind(transformed, url)
        completed = subprocess.run(["bash"], input=script, text=True, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        call_lines = calls.read_text(encoding="utf-8").splitlines()
        cases.append((name, completed.returncode, sum(line.startswith("go ") for line in call_lines), sum(line.startswith("go test ") for line in call_lines), sum(line.startswith("curl ") for line in call_lines)))
        if not (completed.returncode == expected_exit and cases[-1][3] == expected_tests and cases[-1][4] == expected_uploads): raise SystemExit(f"{name} binding/source/toolchain result differed: {cases[-1]}")
        if name in ("missing", "wrong_host", "changed_source") and cases[-1][2] != 0: raise SystemExit(f"{name} unexpectedly invoked fake Go")
        result = root / "result"
        if result.exists():
            import shutil
            shutil.rmtree(result)
    for case in cases:
        print("case=%s exit=%d fake_go_calls=%d fake_go_test_calls=%d fake_upload_calls=%d" % case)

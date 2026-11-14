import json
import os
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

from ..contracts import Observation
from ..gitutil import write_fixture


def execute(files, cases, run_id, side, sha, backend="local", timeout=30):
    if not cases:
        return []
    rows, error, error_status = [], None, "execution_error"
    with tempfile.TemporaryDirectory(prefix="pact-execution-") as td:
        root = Path(td)
        write_fixture(root, files)
        worker = Path(__file__).resolve().parents[1] / "fixture_worker.py"
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            process = subprocess.Popen([sys.executable, "-I", str(worker), str(root)],
                                       stdin=subprocess.PIPE, stdout=stdout, stderr=stderr,
                                       cwd=root, env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
                                       start_new_session=True)
            try:
                process.communicate(json.dumps([{"scenario_id": s.scenario_id, "request": s.request()} for s in cases]).encode(), timeout=timeout)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.communicate()
                error, error_status = "Fixture exceeded execution budget", "timeout"
            stdout.seek(0)
            stderr.seek(0)
            raw = stdout.read(1048577)
            if error is None:
                try:
                    if process.returncode:
                        raise ValueError(stderr.read(1000).decode(errors="replace") or "Fixture process failed")
                    if len(raw) > 1048576:
                        raise ValueError("Fixture output exceeds limit")
                    rows = json.loads(raw)
                    if not isinstance(rows, list) or {r["scenario_id"] for r in rows} != {s.scenario_id for s in cases} or len(rows) != len(cases):
                        raise ValueError("Fixture returned missing or duplicate scenarios")
                    return [Observation(run_id=run_id, side=side, commit_sha=sha, execution_backend=backend, **row) for row in rows]
                except (ValueError, TypeError, KeyError) as exc:
                    error, error_status = str(exc)[:1000], "invalid_output"
    return [Observation(run_id=run_id, side=side, commit_sha=sha, scenario_id=s.scenario_id,
                        status=error_status, execution_backend=backend, error_kind=error_status,
                        error_message=error) for s in cases]

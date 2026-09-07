"""Credential-free subprocess entrypoint shared by local and remote runners."""
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(sys.argv[1]) / "pact/demo"))
from workspace_app.app import dispatch

results = []
for case in json.load(sys.stdin):
    start = time.perf_counter()
    try:
        output = dispatch(case["request"])
        if not isinstance(output, dict) or type(output.get("allowed")) is not bool:
            raise ValueError("Fixture output must contain a boolean allowed field")
        row = {"scenario_id": case["scenario_id"], "allowed": output["allowed"], "status": "ok"}
    except Exception as error:
        row = {"scenario_id": case["scenario_id"], "allowed": None, "status": "execution_error",
               "error_kind": type(error).__name__, "error_message": str(error)[:500]}
    row["duration_ms"] = (time.perf_counter() - start) * 1000
    results.append(row)
print(json.dumps(results))

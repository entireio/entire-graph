from pathlib import Path

import pytest

from btw_pact.contracts import ReviewRequest, now
from btw_pact.evidence import replay, seal
from btw_pact.gitutil import fixture_files, resolve
from btw_pact.review import review
from btw_pact.runners.local import execute
from btw_pact.scenarios import matrix, proposed_requirements

REPO = Path(__file__).resolve().parents[2]


def requirements():
    # Explicitly synthetic approval used only for automated tests.
    return [r.model_copy(update={"status": "confirmed_active", "confirmed_by": "synthetic test actor", "confirmed_at": now()})
            for r in proposed_requirements()]


@pytest.mark.parametrize("head, failures", [("pact-H1", 2), ("pact-H2", 0)])
def test_real_graph_selects_unchanged_export_and_executes_versions(tmp_path, head, failures):
    report = review(ReviewRequest(repo_path=str(REPO), base_sha="pact-B0", head_sha=head,
                                 requirements=requirements()), tmp_path)
    assert not report["errors"]
    assert report["counts"]["base"]["pass"] == 8
    assert report["counts"]["head"]["fail"] == failures
    assert report["counts"]["head"]["pass"] == 10 - failures
    assert any(p["requirement_ref"] == "R1@1" and p["symbols"][-1]["file_path"].endswith("export.py")
               for p in report["selection"]["paths"])
    assert all(f["classification"] == "confirmed_regression" for f in report["findings"])
    # Missing real source evidence must remain visible even when execution works.
    assert report["completion_state"] == "partial" and report["source_gaps"]


def test_changed_file_baseline_misses_old_export_rule(tmp_path):
    report = review(ReviewRequest(repo_path=str(REPO), base_sha="pact-B0", head_sha="pact-H1",
                                 requirements=requirements(), strategy="changed_file"), tmp_path)
    assert report["selection"]["selected_requirement_ids"] == ["R4@1"]
    assert report["counts"]["base"]["observations"] == 0
    assert report["counts"]["head"]["pass"] == 2


def test_pinned_reproducer_and_tamper_detection():
    commits = {"base": resolve(REPO, "pact-B0"), "head": resolve(REPO, "pact-H1")}
    payload = {"commits": commits, "fixtures": {s: fixture_files(REPO, sha) for s, sha in commits.items()},
               "requirements": [r.model_dump() for r in requirements()], "scenarios": [s.model_dump() for s in matrix()]}
    assert replay(seal(payload))["exit_code"] == 1
    broken = seal(payload)
    broken["sha256"] = "bad"
    with pytest.raises(ValueError, match="integrity"):
        replay(broken)


def test_timeout_is_not_a_decision():
    files = fixture_files(REPO, resolve(REPO, "pact-B0"))
    files["pact/demo/workspace_app/permissions.py"] = "def can_access(request):\n    while True: pass\n"
    output = execute(files, matrix()[:1], "test", "head", "test-sha", timeout=0.15)
    assert output[0].status == "timeout" and output[0].allowed is None

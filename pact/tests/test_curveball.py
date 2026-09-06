"""Team-authored noon fixtures; the official constraint was supplied as screenshots."""
import copy
import json
from pathlib import Path

import pytest

from btw_pact.contracts import ReviewRequest
from btw_pact.evidence import replay
from btw_pact.gitutil import SCOPE, fixture_files, git, resolve, write_fixture
from btw_pact.graph_adapter import analyse
from btw_pact.review import review
from btw_pact.selector import select
from test_incomplete_evidence import policies

REPO = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="module")
def dynamic_repo(tmp_path_factory):
    """Only the helper changes. The unchanged export uses a runtime lookup."""
    repo = tmp_path_factory.mktemp("team-dynamic-fixture")
    git(repo, "init", "-q")
    git(repo, "config", "user.name", "PACT synthetic fixture test")
    git(repo, "config", "user.email", "fixture@example.invalid")
    git(repo, "config", "core.hooksPath", "/dev/null")
    (repo / "pact").mkdir()
    (repo / "pact/graph-fixture.ignore").write_text((REPO / "pact/graph-fixture.ignore").read_text())
    for label, source in (("base", "pact-B0"), ("head", "pact-H1")):
        files = fixture_files(REPO, resolve(REPO, source))
        files[SCOPE + "/export.py"] = '''from workspace_app import permissions


def export_document(request):
    check = getattr(permissions, "can_" + "access")
    return {"allowed": check(request)}
'''
        write_fixture(repo, files)
        git(repo, "add", ".")
        git(repo, "commit", "-qm", "Synthetic dynamic fixture " + label)
        git(repo, "tag", label)
    return repo


def test_dynamic_caller_executes_fallback_without_inventing_graph_path(dynamic_repo, tmp_path):
    # Without fallback the real Graph omits guest export and misses both failures.
    report = review(ReviewRequest(repo_path=str(dynamic_repo), base_sha="base", head_sha="head",
                                 requirements=policies()), tmp_path)
    assert report["counts"]["head"]["fail"] == 2
    selection = report["selection"]
    assert selection["partial_analysis"]
    assert selection["fallback"]["mode"] == "all_registered"
    assert selection["fallback"]["added_requirement_ids"] == ["R1@1", "R3@1"]
    assert not any(p["requirement_ref"] == "R1@1" for p in selection["paths"])
    assert any(d["side"] == "head" and d["file_path"].endswith("export.py") for d in selection["diagnostics"])
    assert report["completion_state"] == "partial"
    saved = json.loads((tmp_path / report["run_id"] / "reproducer.json").read_text())
    reproduced = replay(saved)
    assert reproduced["exit_code"] == 1
    assert reproduced["evidence_context"]["selection"] == selection
    assert reproduced["review_completion_state"] == "partial"


def test_graph_unavailable_runs_all_registered_checks_without_paths():
    result = select(policies(), {"diff":{"files":[]}, "versions":{}, "partial":True, "errors":["unavailable"]})
    assert result["selected_requirement_ids"] == ["R1@1", "R2@1", "R3@1", "R4@1"]
    assert result["paths"] == []
    assert result["fallback"]["mode"] == "all_registered"


def test_resolved_paths_stay_structural_and_heuristics_are_never_upgraded():
    original = analyse(REPO, resolve(REPO, "pact-B0"), resolve(REPO, "pact-H1"))
    resolved = select(policies(), original)
    assert not resolved["partial_analysis"]
    assert resolved["fallback"]["mode"] == "none"
    assert all(p["evidence_quality"] == "confirmed_structural" for p in resolved["paths"])
    heuristic = copy.deepcopy(original)
    for graph in heuristic["versions"].values():
        for edge in graph["calls"]:
            if "export.py" in edge["from_id"]:
                edge.update(resolution="heuristic", confidence=0.5)
    result = select(policies(), heuristic)
    export_paths = [p for p in result["paths"] if p["requirement_ref"] == "R1@1"]
    assert export_paths and all(p["evidence_quality"] == "heuristic" for p in export_paths)
    assert result["partial_analysis"] and result["fallback"]["mode"] == "all_registered"


def test_silent_graph_omissions_still_trigger_source_verification(dynamic_repo):
    # A provider may emit no unresolved edge at all. Parser success is insufficient.
    evidence = analyse(dynamic_repo, resolve(dynamic_repo, "base"), resolve(dynamic_repo, "head"))
    for graph in evidence["versions"].values():
        graph["calls"] = [e for e in graph["calls"] if e["to_id"] in graph["symbols"]]
        graph["partial"] = False
    evidence["partial"] = False
    result = select(policies(), evidence)
    assert result["fallback"]["mode"] == "all_registered"
    assert "R1@1" in result["selected_requirement_ids"]
    assert any(d["origin"] == "source_precaution" for d in result["diagnostics"])


def test_module_configuration_change_cannot_look_like_no_impact(tmp_path):
    from btw_pact.graph_adapter import source_diagnostics
    diagnostics = source_diagnostics({SCOPE + "/permissions.py":
        'ALLOW_GUESTS = True\n\ndef can_access(request):\n    return ALLOW_GUESTS\n'})
    assert any(d["code"] == "module_configuration" and d["line"] == 1 for d in diagnostics)

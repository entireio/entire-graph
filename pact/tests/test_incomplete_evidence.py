from pathlib import Path

import pytest

from btw_pact.contracts import Observation, ReviewRequest
from btw_pact.review import review
from btw_pact.scenarios import proposed_requirements
from btw_pact.selector import select
from btw_pact.review import recover
from btw_pact.evidence import write_json


def policies():
    return [r.model_copy(update={"status":"confirmed_active","confirmed_by":"synthetic test actor",
            "confirmed_at":"2026-09-06T05:00:00+00:00"}) for r in proposed_requirements()]


def test_graph_failure_is_partial_and_does_not_invent_paths():
    selection = select(policies(),{"diff":{"files":[]},"versions":{},"partial":True,"errors":["truncated snapshot"]})
    assert selection["partial_analysis"]
    assert len(selection["unresolved_ids"]) == 4
    assert selection["paths"] == []


@pytest.mark.parametrize("fault",["wrong_sha","missing_row"])
def test_remote_identity_and_cardinality_are_enforced(tmp_path,fault):
    def remote(bundle, chosen, run_id):
        rows = {side:[Observation(run_id=run_id,side=side,commit_sha="0"*40 if fault=="wrong_sha" else bundle["commits"][side],
                scenario_id=c.scenario_id,allowed=False,status="ok",execution_backend="databricks") for c in cases]
                for side,cases in chosen.items()}
        if fault=="missing_row": rows["head"].pop()
        return rows,{}
    request = ReviewRequest(repo_path=str(Path(__file__).resolve().parents[2]),base_sha="pact-B0",head_sha="pact-H1",
                            requirements=policies(),runner="databricks")
    with pytest.raises(ValueError,match="identity or cardinality"):
        review(request,tmp_path,remote=remote)
    assert not list(tmp_path.glob("*/report.json"))


def test_completed_recovery_is_immutable_and_needs_no_remote(tmp_path):
    run_id = "a"*32
    saved = {"run_id":run_id,"counts":{"head":{"fail":2}},"completion_state":"partial"}
    write_json(tmp_path/run_id/"report.json",saved)
    assert recover(run_id,tmp_path) == saved
    with pytest.raises(ValueError,match="receipt/request"):
        recover("b"*32,tmp_path)
    with pytest.raises(ValueError,match="identity"):
        recover("../../elsewhere",tmp_path)

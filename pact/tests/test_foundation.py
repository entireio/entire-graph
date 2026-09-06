import pytest
from pydantic import ValidationError

from btw_pact.contracts import Observation
from btw_pact.scenarios import matrix, matches, proposed_requirements
from btw_pact.storage import Store


def test_independent_matrix_and_oracle():
    cases = matrix()
    assert len(cases) == len({s.scenario_id for s in cases}) == 24
    requirements = proposed_requirements()
    assert [sum(matches(r, s) for s in cases) for r in requirements] == [4, 2, 2, 2]
    assert sum(matches(r, s) for r in requirements for s in cases if "base" in r.applies_to) == 8


def test_confirmation_is_immutable_and_survives_restart(tmp_path):
    store = Store(tmp_path / "state.db")
    proposal = proposed_requirements()[0]
    store.add(proposal)
    with pytest.raises(ValueError, match="explicit"):
        store.confirm(proposal.key, " ")
    confirmed = store.confirm(proposal.key, "synthetic test actor")
    assert confirmed.revision == 2 and confirmed.status == "confirmed_active"
    assert Store(store.path).requirements()[0] == confirmed
    assert store.requirements(history=True)[0].status == "proposed"
    with pytest.raises(ValueError, match="stale"):
        store.confirm(proposal.key, "test actor")


def test_runner_error_cannot_masquerade_as_pass():
    with pytest.raises(ValidationError):
        Observation(run_id="test", side="head", commit_sha="a", scenario_id="x", allowed=True,
                    status="timeout", execution_backend="local")

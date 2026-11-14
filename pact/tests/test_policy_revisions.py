from pathlib import Path

from btw_pact.contracts import ReviewRequest
from btw_pact.review import review
from btw_pact.scenarios import proposed_requirements
from btw_pact.storage import Store

REPO = Path(__file__).resolve().parents[2]


def confirmed(tmp_path):
    store = Store(tmp_path / "registry.sqlite")
    for requirement in proposed_requirements():
        store.add(requirement)
        store.confirm(requirement.key, "synthetic test actor")
    return store


def test_harmless_refactor_and_preexisting_violation(tmp_path):
    store = confirmed(tmp_path)
    request = ReviewRequest(repo_path=str(REPO),base_sha="pact-H2",head_sha="pact-H3",requirements=store.review_requirements())
    result = review(request,tmp_path)
    assert result["counts"]["head"]["fail"] == 0
    assert result["counts"]["head"]["pass"] == 10
    result = review(request.model_copy(update={"base_sha":"pact-H1","head_sha":"pact-H4"}),tmp_path)
    assert len(result["findings"]) == 2
    assert {f["classification"] for f in result["findings"]} == {"pre_existing_violation"}


def test_intentional_policy_revision_keeps_baseline_and_history(tmp_path):
    store = confirmed(tmp_path)
    original = store.requirements()[0]
    revised = store.confirm(original.key,"synthetic test actor",amendments={
        "text":"Alternative test policy: deny guest exports only for private content.",
        "scenario_filter":{"role":"guest","operation":"export","visibility":"private"},
        "applies_to":["head"]})
    assert store.requirements(history=True)[1] == original
    requirements = store.review_requirements()
    assert next(r for r in requirements if r.key==original.key).applies_to == ["base"]
    result = review(ReviewRequest(repo_path=str(REPO),base_sha="pact-B0",head_sha="pact-H4",requirements=requirements),tmp_path)
    assert result["counts"]["head"]["fail"] == 0
    assert any(f["classification"]=="intentional_change" and f["requirement_ref"]==revised.key for f in result["findings"])
    assert all(a["status"]=="not_applicable" for a in result["assertions"] if a["side"]=="head" and a["requirement_id"]=="R1" and a["requirement_revision"]==original.revision)
    reconfirmed = store.confirm(revised.key,"synthetic test actor")
    assert reconfirmed.policy_changed
    assert next(r for r in store.review_requirements() if r.applies_to==["base"]).key == original.key

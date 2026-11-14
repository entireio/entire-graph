import time
from pathlib import Path

from fastapi.testclient import TestClient

from btw_pact.web import create_app


def test_confirmation_review_and_immutable_history(tmp_path):
    app = create_app(Path(__file__).resolve().parents[2], tmp_path)
    with TestClient(app) as client:
        assert client.get("/").status_code == 200
        assert client.post("/api/reviews", json={}).status_code == 409
        assert client.post("/api/requirements/R1@1/confirm", json={"actor":"test actor"},
                           headers={"Origin":"https://unrelated.example"}).status_code == 403
        for requirement in client.get("/api/state").json()["requirements"]:
            assert client.post(f'/api/requirements/{requirement["key"]}/confirm',json={"actor":"test actor"}).status_code == 200
        assert client.post("/api/requirements/R1@1/confirm",json={"actor":"test actor"}).status_code == 409
        job = client.post("/api/reviews", json={}).json()["job_id"]
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            current = next(j for j in client.get("/api/state").json()["jobs"] if j["job_id"]==job)
            if current["status"] in ("complete","failed"):
                break
            time.sleep(.1)
        assert current["status"] == "complete", current
        run_id = current["run_ids"][0]
        report = client.get(f"/api/runs/{run_id}").json()
        assert report["counts"]["head"]["fail"] == 2
        assert report["completion_state"] == "partial"  # No invented Checkpoint source.
        assert client.get(f"/api/runs/{run_id}/reproducer").json()["format"] == "pact-reproducer-1"
    restarted = create_app(Path(__file__).resolve().parents[2], tmp_path)
    assert restarted.state.store.run(run_id)["counts"]["head"]["fail"] == 2

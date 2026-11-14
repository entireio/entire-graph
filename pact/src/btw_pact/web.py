"""Local review workbench. Background execution keeps source inspection responsive."""
import json
import threading
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse, HTMLResponse
from fastapi.staticfiles import StaticFiles
from starlette.middleware.trustedhost import TrustedHostMiddleware
from pydantic import BaseModel, ConfigDict, Field

from .checkpoints import read_sources
from .contracts import ReviewRequest, Source, digest
from .review import benchmark, review
from .gitutil import resolve
from .scenarios import proposed_requirements
from .storage import Store


class Confirm(BaseModel):
    model_config = ConfigDict(extra="forbid")
    actor: str = Field(min_length=1, max_length=100)
    amendments: dict | None = None


class Start(BaseModel):
    model_config = ConfigDict(extra="forbid")
    base_sha: str = "pact-B0"
    head_sha: str = "pact-H1"
    runner: str = "local"
    benchmark: bool = False


class LinkSource(BaseModel):
    commit: str
    excerpt_hash: str
    requirement_id: str


def create_app(repo: Path, data: Path | None = None):
    repo = repo.resolve()
    data = data or repo / "pact/runs"
    store = Store(data / "pact.sqlite")
    versions = {}
    for tag in ("pact-D0", "pact-D1", "pact-D2"):
        try:
            versions[tag] = resolve(repo, tag)
        except RuntimeError:
            pass  # Older checkouts can still show and replay their saved evidence.
    if not store.requirements():
        for r in proposed_requirements():
            store.add(r)
    app = FastAPI(title="PACT for Entire")
    app.add_middleware(TrustedHostMiddleware, allowed_hosts=["127.0.0.1", "localhost", "testserver"])
    assets = Path(__file__).parent
    app.mount("/static", StaticFiles(directory=assets / "static"), name="static")
    executor, jobs, lock = ThreadPoolExecutor(max_workers=1), {}, threading.Lock()
    app.state.store = store

    @app.middleware("http")
    async def local_mutation(request: Request, call_next):
        if request.method == "POST":
            origin = request.headers.get("origin")
            if origin and origin != str(request.base_url).rstrip("/"):
                return HTMLResponse("Cross-origin mutation rejected", status_code=403)
            if request.headers.get("content-type", "").split(";")[0] != "application/json":
                return HTMLResponse("JSON body required", status_code=415)
        return await call_next(request)

    @app.get("/", response_class=HTMLResponse)
    def index():
        return (assets / "templates/index.html").read_text()

    @app.get("/api/state")
    def state():
        with lock:
            progress = list(jobs.values())
        return {"requirements": [r.model_dump() | {"key": r.key} for r in store.requirements()],
                "history": [{"run_id": r["run_id"], "created_at": r["created_at"], "backend": r["backend"],
                             "completion_state": r["completion_state"], "counts": r["counts"],
                             "commits": r["commits"]} for r in store.runs()], "jobs": progress,
                "versions": versions, "event": {"track": "E2 · Graph Intelligence", "deadline": "15:00 IST · 6 September 2026"}}

    @app.get("/api/requirements/history")
    def history():
        return [r.model_dump() for r in store.requirements(history=True)]

    @app.post("/api/requirements/{key}/confirm")
    def confirm(key: str, body: Confirm):
        try:
            return store.confirm(key, body.actor, amendments=body.amendments).model_dump()
        except ValueError as error:
            raise HTTPException(409, str(error)) from error

    @app.get("/api/sources")
    def sources(commit: str = "pact-H1"):
        try:
            return read_sources(repo, commit)
        except Exception as error:
            raise HTTPException(422, str(error)[:1000]) from error

    @app.post("/api/requirements/link-source")
    def link_source(body: LinkSource):
        # Re-read the real exporter; do not accept client-authored evidence text.
        exported = read_sources(repo, body.commit)
        match = next((s for s in exported["sources"] if s["excerpt_hash"] == body.excerpt_hash), None)
        requirement = next((r for r in store.requirements() if r.requirement_id == body.requirement_id), None)
        if not match or requirement is None:
            raise HTTPException(404, "Original excerpt or requirement not found")
        new = requirement.model_copy(update={"revision": requirement.revision + 1, "status": "proposed",
                    "confirmed_by": None, "confirmed_at": None, "supersedes": requirement.key,
                    "source_refs": [Source.model_validate(match)]})
        store.add(new)
        return new.model_dump()

    @app.post("/api/reviews", status_code=202)
    def start(body: Start):
        try:
            request = ReviewRequest(repo_path=str(repo), base_sha=body.base_sha, head_sha=body.head_sha,
                                    runner=body.runner, requirements=store.review_requirements())
        except ValueError as error:
            raise HTTPException(422, str(error)) from error
        if not any(r.status == "confirmed_active" for r in request.requirements):
            raise HTTPException(409, "Confirm a requirement before running its checks")
        with lock:
            if any(j["status"] in ("queued", "running") for j in jobs.values()):
                raise HTTPException(409, "A review is already running")
            key = uuid.uuid4().hex
            jobs[key] = {"job_id": key, "status": "queued", "runner": body.runner}

        def work():
            with lock:
                jobs[key]["status"] = "running"
            try:
                result = (benchmark if body.benchmark else review)(request, data)
                reports = result["strategies"] + [result["reference"]] if body.benchmark else [result]
                for report in reports:
                    store.save_run(report)
                with lock:
                    jobs[key].update(status="complete", run_ids=[r["run_id"] for r in reports],
                                     comparison_id=result.get("comparison_id"))
            except Exception as error:
                with lock:
                    jobs[key].update(status="failed", error=str(error)[:2000])
        executor.submit(work)
        return {"job_id": key}

    @app.get("/api/runs/{run_id}")
    def run(run_id: str):
        try:
            return store.run(run_id)
        except KeyError as error:
            raise HTTPException(404, "Unknown run") from error

    @app.get("/api/runs/{run_id}/reproducer")
    def reproducer(run_id: str):
        run(run_id)  # Membership check prevents path traversal.
        return FileResponse(data / run_id / "reproducer.json", filename=f"pact-{run_id[:8]}-reproducer.json")

    @app.get("/api/remote-history")
    def cloud_history():
        from .runners.databricks import remote_history
        try:
            return remote_history()
        except Exception as error:
            raise HTTPException(503, str(error)[:1000]) from error

    return app

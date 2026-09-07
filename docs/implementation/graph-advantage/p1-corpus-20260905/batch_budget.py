"""Fail-closed accounting for one bounded P1 execution batch.

The launcher validates the immutable selected-cell manifest and assigns each
cell to exactly one worker.  A worker then persists its own claim ledger.  The
launcher-owned allocations are the cross-worker global bound; the local ledger
prevents a worker restart from reusing a reservation.
"""
from __future__ import annotations

import hashlib
import json
import os
import pathlib
import tempfile
import re
from typing import Any


CAP = 100
REQUIRED_IDENTITY = (
    "binary_sha256",
    "input_manifest_sha256",
    "runner_sha256",
    "scenario_sha256",
    "gate_sha256",
    "budget_sha256",
    "build_manifest_sha256",
)
ALLOWED_SCOPES = {"bounded-diagnostic", "stability-sampling", "full-campaign"}
_BATCH_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")


class BudgetError(ValueError):
    """The selected batch cannot be admitted or its ledger is inconsistent."""


def _canonical(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def cell_id(cell: dict[str, Any]) -> str:
    """Return the stable ID for a cell, excluding its supplied ID."""
    identity = {key: value for key, value in cell.items() if key != "cell_id"}
    return hashlib.sha256(_canonical(identity)).hexdigest()


def _bool_arm(value: Any) -> bool:
    if not isinstance(value, bool):
        raise BudgetError("cell arms must contain booleans")
    return value


def normalize_cell(raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise BudgetError("each batch cell must be an object")
    required = ("cell_id", "worker", "repository", "profile", "verb", "scenario", "trial", "arms")
    missing = [key for key in required if key not in raw]
    if missing:
        raise BudgetError("cell missing " + ", ".join(missing))
    if not isinstance(raw["cell_id"], str) or raw["cell_id"] != cell_id(raw):
        raise BudgetError("cell_id does not match the canonical cell identity")
    worker = raw["worker"]
    if isinstance(worker, bool) or not isinstance(worker, int) or worker < 1:
        raise BudgetError("cell worker must be a positive integer")
    if raw["verb"] not in ("snapshot", "search"):
        raise BudgetError("cell verb must be snapshot or search")
    if isinstance(raw["trial"], bool) or not isinstance(raw["trial"], int) or raw["trial"] < 0:
        raise BudgetError("cell trial must be a non-negative integer")
    arms = raw["arms"]
    if not isinstance(arms, list) or not 1 <= len(arms) <= 2 or len(set(arms)) != len(arms):
        raise BudgetError("cell arms must contain one or two distinct booleans")
    arms = [_bool_arm(value) for value in arms]
    prep = raw.get("preparatory_invocations", 0)
    if isinstance(prep, bool) or not isinstance(prep, int) or prep < 0:
        raise BudgetError("preparatory_invocations must be a non-negative integer")
    # The declared cost is informational only.  Never trust it for admission.
    result = dict(raw)
    result["arms"] = arms
    result["preparatory_invocations"] = prep
    result["derived_invocations"] = prep + len(arms)
    return result


def validate(document: Any, expected_identity: dict[str, str] | None = None) -> dict[str, Any]:
    if not isinstance(document, dict) or document.get("version") != 1:
        raise BudgetError("batch manifest version 1 is required")
    cap = document.get("cap")
    if isinstance(cap, bool) or not isinstance(cap, int) or cap != CAP:
        raise BudgetError(f"batch cap must be exactly {CAP}")
    batch_id = document.get("batch_id")
    if not isinstance(batch_id, str) or not _BATCH_ID_RE.fullmatch(batch_id):
        raise BudgetError("batch_id must be a path-safe lowercase identifier")
    if document.get("scope") not in ALLOWED_SCOPES:
        raise BudgetError("batch scope is unknown")
    identity = document.get("identity")
    if not isinstance(identity, dict):
        raise BudgetError("batch identity is required")
    for key in REQUIRED_IDENTITY:
        value = identity.get(key)
        if not isinstance(value, str) or len(value) != 64:
            raise BudgetError(f"batch identity {key} is required")
        if expected_identity is not None and value.lower() != str(expected_identity[key]).lower():
            raise BudgetError(f"batch identity mismatch: {key}")
    cells_raw = document.get("cells")
    if not isinstance(cells_raw, list) or not cells_raw:
        raise BudgetError("selected batch must contain cells")
    cells = [normalize_cell(raw) for raw in cells_raw]
    ids = [item["cell_id"] for item in cells]
    if len(set(ids)) != len(ids):
        raise BudgetError("duplicate cell_id in batch manifest")
    total = sum(item["derived_invocations"] for item in cells)
    if total > CAP:
        raise BudgetError(f"selected batch requires {total} invocations; cap is {CAP}")
    workers = document.get("workers")
    if not isinstance(workers, dict) or not workers:
        raise BudgetError("per-worker allocations are required")
    allocation_total = 0
    calculated = {}
    for item in cells:
        calculated[str(item["worker"])] = calculated.get(str(item["worker"]), 0) + item["derived_invocations"]
    for worker, allocation in workers.items():
        if not isinstance(worker, str) or not worker.isdigit() or int(worker) < 1:
            raise BudgetError(f"invalid worker allocation key {worker!r}")
        if not isinstance(allocation, int) or allocation < 0:
            raise BudgetError(f"invalid allocation for worker {worker}")
        if allocation != calculated.get(worker, 0):
            raise BudgetError(f"worker {worker} allocation does not match derived cost")
        allocation_total += allocation
    if set(workers) != set(calculated) or allocation_total != total:
        raise BudgetError("worker allocations do not equal derived invocation total")
    semantic = {}
    for item in cells:
        key = tuple((name, tuple(item[name]) if name == "arms" else item[name]) for name in
                    ("repository", "profile", "verb", "scenario", "trial"))
        if key in semantic:
            raise BudgetError("duplicate semantic pair across workers")
        semantic[key] = item["cell_id"]
    result = dict(document)
    result["cells"] = cells
    result["derived_invocations"] = total
    result["workers"] = workers
    return result


def load(path: pathlib.Path, expected_identity: dict[str, str] | None = None) -> dict[str, Any]:
    try:
        document = json.loads(pathlib.Path(path).read_text())
    except (OSError, json.JSONDecodeError) as exc:
        raise BudgetError(f"cannot read batch manifest: {exc}") from exc
    result = validate(document, expected_identity)
    result["manifest_sha256"] = hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
    return result


def create_claim(path: pathlib.Path, document: dict[str, Any]) -> None:
    """Atomically claim a batch dispatch path before any remote mutation."""
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError as exc:
        raise BudgetError(f"batch dispatch claim already exists: {path}") from exc
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(document, stream, sort_keys=True, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
    except BaseException:
        path.unlink(missing_ok=True)
        raise


class WorkerLedger:
    """Durable reservation/start accounting for one immutable worker slice."""

    def __init__(self, path: pathlib.Path, batch: dict[str, Any], worker: int):
        self.path = pathlib.Path(path)
        self.cells = {cell["cell_id"]: cell for cell in batch["cells"] if cell["worker"] == worker}
        self.state = {
            "version": 1,
            "batch_id": batch["batch_id"],
            "worker": worker,
            "cap": CAP,
            "reserved": {},
            "started": [],
            "completed": [],
            "failed": [],
        }
        for item in self.cells.values():
            phases = [f"prep:{index}" for index in range(item["preparatory_invocations"])]
            phases.extend(f"arm:{str(arm).lower()}" for arm in item["arms"])
            self.state["reserved"][item["cell_id"]] = phases
        self.path.parent.mkdir(parents=True, exist_ok=True)
        try:
            fd = os.open(self.path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        except FileExistsError as exc:
            raise BudgetError(f"existing budget ledger refuses restart: {self.path}") from exc
        try:
            with os.fdopen(fd, "w") as stream:
                json.dump(self.state, stream, sort_keys=True, indent=2)
                stream.write("\n")
                stream.flush()
                os.fsync(stream.fileno())
        except BaseException:
            self.path.unlink(missing_ok=True)
            raise

    def _write(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        fd, name = tempfile.mkstemp(prefix=self.path.name + ".", dir=self.path.parent)
        try:
            with os.fdopen(fd, "w") as stream:
                json.dump(self.state, stream, sort_keys=True, indent=2)
                stream.write("\n")
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(name, self.path)
        finally:
            pathlib.Path(name).unlink(missing_ok=True)

    def start(self, cell_id_value: str, phase: str) -> None:
        phases = self.state["reserved"].get(cell_id_value)
        token = f"{cell_id_value}:{phase}"
        if phases is None or phase not in phases:
            raise BudgetError(f"unreserved evaluator invocation: {token}")
        if token in self.state["started"]:
            raise BudgetError(f"duplicate evaluator invocation: {token}")
        # Persist before spawning. A failed Popen is still an attempted spawn.
        self.state["started"].append(token)
        self._write()

    def finish(self, cell_id_value: str, phase: str, status: str) -> None:
        token = f"{cell_id_value}:{phase}"
        if token not in self.state["started"]:
            raise BudgetError(f"finished invocation was never started: {token}")
        if token in self.state["completed"] or token in self.state["failed"]:
            raise BudgetError(f"invocation finished twice: {token}")
        (self.state["completed"] if status == "ok" else self.state["failed"]).append(token)
        self._write()

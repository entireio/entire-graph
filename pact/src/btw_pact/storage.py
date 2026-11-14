"""Append-only requirement revisions and immutable completed run records."""
import json
import sqlite3
from pathlib import Path

from .contracts import Requirement, canonical, now


class Store:
    def __init__(self, path: Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self.connect() as db:
            db.executescript("""
                PRAGMA journal_mode=WAL;
                CREATE TABLE IF NOT EXISTS requirements (
                    id TEXT NOT NULL, revision INTEGER NOT NULL, payload TEXT NOT NULL,
                    PRIMARY KEY (id, revision));
                CREATE TABLE IF NOT EXISTS runs (
                    id TEXT PRIMARY KEY, created_at TEXT NOT NULL, payload TEXT NOT NULL);
            """)

    def connect(self):
        return sqlite3.connect(self.path, timeout=10)

    def add(self, requirement: Requirement):
        with self.connect() as db:
            db.execute("INSERT INTO requirements VALUES (?, ?, ?)",
                       (requirement.requirement_id, requirement.revision, canonical(requirement)))

    def requirements(self, history=False) -> list[Requirement]:
        query = "SELECT payload FROM requirements"
        if not history:
            query += " r WHERE revision=(SELECT MAX(revision) FROM requirements WHERE id=r.id)"
        with self.connect() as db:
            return [Requirement.model_validate_json(row[0]) for row in db.execute(query + " ORDER BY id, revision")]

    def confirm(self, key: str, actor: str, *, amendments: dict | None = None) -> Requirement:
        if not actor.strip():
            raise ValueError("An explicit confirming actor is required")
        with self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            rid, revision = key.rsplit("@", 1)
            row = db.execute("SELECT payload FROM requirements WHERE id=? AND revision=?", (rid, int(revision))).fetchone()
            if row is None:
                raise ValueError("Unknown requirement revision")
            old = Requirement.model_validate_json(row[0])
            latest = db.execute("SELECT MAX(revision) FROM requirements WHERE id=?", (rid,)).fetchone()[0]
            if old.revision != latest:
                raise ValueError("This proposal is stale; inspect the latest revision")
            editable = {"text", "scenario_filter", "expected_allowed", "entrypoints", "applies_to"}
            if set(amendments or {}) - editable:
                raise ValueError("Unsupported amendment")
            data = old.model_dump()
            data.update(amendments or {})
            data.update(revision=latest + 1, status="confirmed_active", confirmed_by=actor.strip(),
                        confirmed_at=now(), supersedes=old.key, policy_changed=old.policy_changed or bool(amendments))
            new = Requirement.model_validate(data)
            db.execute("INSERT INTO requirements VALUES (?, ?, ?)", (rid, new.revision, canonical(new)))
            return new

    def review_requirements(self) -> list[Requirement]:
        """Bind an explicitly head-only revision to its prior baseline policy."""
        latest, history = self.requirements(), self.requirements(history=True)
        result = list(latest)
        for current in latest:
            if current.status != "confirmed_active" or not current.policy_changed or current.applies_to != ["head"]:
                continue
            previous = [r for r in history if r.requirement_id == current.requirement_id
                        and r.revision < current.revision and r.status == "confirmed_active" and "base" in r.applies_to]
            if previous:
                # Applicability is a review binding; stored historical revision stays unchanged.
                result.append(previous[-1].model_copy(update={"applies_to": ["base"]}))
        return result

    def save_run(self, report: dict):
        with self.connect() as db:
            db.execute("INSERT INTO runs VALUES (?, ?, ?)", (report["run_id"], report["created_at"], canonical(report)))

    def run(self, run_id: str) -> dict:
        with self.connect() as db:
            row = db.execute("SELECT payload FROM runs WHERE id=?", (run_id,)).fetchone()
        if not row:
            raise KeyError(run_id)
        return json.loads(row[0])

    def runs(self) -> list[dict]:
        with self.connect() as db:
            return [json.loads(row[0]) for row in db.execute("SELECT payload FROM runs ORDER BY created_at DESC LIMIT 100")]

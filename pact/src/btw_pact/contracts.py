"""Validated, versioned interfaces shared by the UI and execution backends."""
from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

Side = Literal["base", "head"]


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def canonical(value) -> str:
    if isinstance(value, BaseModel):
        value = value.model_dump(mode="json")
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def digest(value) -> str:
    return hashlib.sha256(canonical(value).encode()).hexdigest()


class Record(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"


class Source(Record):
    checkpoint_id: str
    session_id: str
    associated_commit: str
    association_status: Literal["verified", "manual_attachment", "unresolved", "synthetic"]
    message_role: str
    excerpt: str
    excerpt_locator: str
    excerpt_hash: str
    source_uri: str


class Requirement(Record):
    requirement_id: str = Field(pattern=r"^[A-Za-z0-9_-]+$")
    revision: int = Field(default=1, ge=1)
    text: str = Field(min_length=1)
    status: Literal["proposed", "confirmed_active", "superseded", "unresolved"] = "proposed"
    source_refs: list[Source] = Field(default_factory=list)
    entrypoints: list[str]
    assertion_template: Literal["permission_allowed_equals"] = "permission_allowed_equals"
    expected_allowed: bool
    scenario_filter: dict[str, str | bool]
    applies_to: list[Side] = Field(default_factory=lambda: ["base", "head"])
    confirmed_by: str | None = None
    confirmed_at: str | None = None
    supersedes: str | None = None
    proposal_mode: Literal["agent_prepared", "model", "manual", "synthetic_test"] = "agent_prepared"

    @property
    def key(self) -> str:
        return f"{self.requirement_id}@{self.revision}"

    @model_validator(mode="after")
    def validate_policy(self):
        options = {"role": {"guest", "member", "admin"}, "operation": {"preview", "export"},
                   "visibility": {"public", "private"}, "same_workspace": {True, False}}
        for key, value in self.scenario_filter.items():
            if key not in options or value not in options[key]:
                raise ValueError(f"Unsupported scenario predicate: {key}={value}")
            if key == "same_workspace" and type(value) is not bool:
                raise ValueError("same_workspace must be a boolean")
        if not self.entrypoints or not self.applies_to or len(set(self.applies_to)) != len(self.applies_to):
            raise ValueError("Unique applicability sides and registered entrypoints are required")
        if self.status == "confirmed_active" and not (self.confirmed_by and self.confirmed_at):
            raise ValueError("Confirmation requires an identified actor and timestamp")
        return self


class Scenario(Record):
    scenario_id: str
    role: Literal["guest", "member", "admin"]
    operation: Literal["preview", "export"]
    same_workspace: bool = Field(strict=True)
    visibility: Literal["public", "private"]

    def request(self) -> dict:
        return self.model_dump(exclude={"schema_version", "scenario_id"})


class ReviewRequest(Record):
    repo_path: str
    base_sha: str
    head_sha: str
    requirements: list[Requirement]
    scope_prefix: Literal["pact/demo/workspace_app"] = "pact/demo/workspace_app"
    runner: Literal["local", "databricks"] = "local"
    strategy: Literal["changed_file", "graph", "all"] = "graph"
    execution_scope: Literal["selected_applicable_scenarios", "full_scenario_matrix"] = "selected_applicable_scenarios"
    comparison_id: str | None = None

    @model_validator(mode="after")
    def unique_requirements(self):
        keys = [r.key for r in self.requirements]
        if len(keys) != len(set(keys)):
            raise ValueError("Duplicate requirement revisions")
        for side in ("base", "head"):
            active = [r.requirement_id for r in self.requirements if side in r.applies_to and r.status == "confirmed_active"]
            if len(active) != len(set(active)):
                raise ValueError("Multiple active revisions of one requirement on the same side")
        return self


class Observation(Record):
    run_id: str
    side: Side
    commit_sha: str
    scenario_id: str
    allowed: bool | None = Field(default=None, strict=True)
    status: Literal["ok", "execution_error", "timeout", "invalid_output", "cancelled"]
    duration_ms: float = 0
    error_kind: str | None = None
    error_message: str | None = None
    execution_backend: Literal["local", "databricks"]

    @model_validator(mode="after")
    def valid_decision(self):
        if (self.status == "ok") != (self.allowed is not None):
            raise ValueError("Only a valid observation can carry a decision")
        return self


class AssertionResult(Record):
    run_id: str
    requirement_id: str
    requirement_revision: int
    scenario_id: str
    side: Side
    expected_allowed: bool
    actual_allowed: bool | None
    status: Literal["pass", "fail", "not_applicable", "unresolved", "not_run"]
    applicability_reason: str
    observation_ref: str | None = None

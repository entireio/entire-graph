"""Load normalized evidence through the canonical compatibility reader."""

from __future__ import annotations

import importlib.util
from pathlib import Path
from typing import Any


def _compat_module():
    path = Path(__file__).resolve().parents[1] / "evidence" / "canonical-v1" / "tools" / "compat.py"
    spec = importlib.util.spec_from_file_location("graph_advantage_canonical_compat", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load canonical compatibility reader: {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_run_and_rows(path: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    compat = _compat_module()
    resolved = path.resolve()
    return compat.load_run(resolved), compat.load_legacy_rows(resolved)


def source_artifact_name(run: dict[str, Any]) -> str:
    for artifact in run.get("artifacts", []):
        if artifact.get("role") == "case-source":
            return Path(artifact["path"]).name
    raise ValueError("canonical run does not name its case-source artifact")

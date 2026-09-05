from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("finalize_baseline.py")
SPEC = importlib.util.spec_from_file_location("p1_finalize_baseline", MODULE_PATH)
assert SPEC and SPEC.loader
finalize_baseline = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = finalize_baseline
SPEC.loader.exec_module(finalize_baseline)


def baseline_row(
    repository: str,
    profile: str,
    verb: str,
    trial: int,
    *,
    parse_ns: int | None = 60,
    total_ns: int | None = 100,
    status: str = "ok",
    partial_failures_count: int = 0,
    partial_failures: list[dict[str, object]] | None = None,
) -> dict[str, object]:
    row: dict[str, object] = {
        "repository": repository,
        "profile": profile,
        "verb": verb,
        "operation": verb,
        "scenario": "baseline",
        "trial": trial,
        "reuse": False,
        "status": status,
        "partial_failures_count": partial_failures_count,
    }
    if partial_failures is not None:
        row["partial_failures"] = partial_failures
    if parse_ns is not None or total_ns is not None:
        row["phase_ns"] = {}
        if parse_ns is not None:
            row["phase_ns"]["phaseParse"] = parse_ns  # type: ignore[index]
        if total_ns is not None:
            row["phase_ns"]["total"] = total_ns  # type: ignore[index]
    return row


def write_worker(
    root: Path,
    assignment: list[dict[str, str]],
    rows: list[dict[str, object]],
    *,
    binary: str = "binary-sha",
    inputs: str = "input-sha",
    blocked: list[dict[str, object]] | None = None,
) -> Path:
    root.mkdir()
    (root / "baseline.ndjson").write_text("\n".join(json.dumps(row) for row in rows) + "\n", encoding="utf-8")
    manifest = {
        "stage": "baseline",
        "binary_sha256": binary,
        "input_manifest_sha256": inputs,
        "compiler": "off",
        "ranking": "current",
        "assignment": assignment,
        "blocked_strata": blocked or [],
    }
    path = root / "baseline-manifest.json"
    path.write_text(json.dumps(manifest), encoding="utf-8")
    return path


class FinalizeBaselineTest(unittest.TestCase):
    def test_freezes_snapshot_membership_and_search_unavailable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workers: list[Path] = []
            rows_a = [baseline_row("repo-a", "fast", "snapshot", trial, parse_ns=60) for trial in range(3)]
            rows_a += [baseline_row("repo-a", "fast", "search", trial, parse_ns=None, total_ns=None) for trial in range(3)]
            workers.append(write_worker(root / "worker-a", [{"repository": "repo-a", "profile": "fast"}], rows_a))

            rows_b = [baseline_row("repo-b", "full", "snapshot", trial, parse_ns=40) for trial in range(3)]
            rows_b += [baseline_row("repo-b", "full", "search", trial, parse_ns=None, total_ns=None) for trial in range(3)]
            workers.append(write_worker(root / "worker-b", [{"repository": "repo-b", "profile": "full"}], rows_b))

            rows_c = [baseline_row("repo-c", "syntax-only", "snapshot", trial, parse_ns=70) for trial in range(2)]
            rows_c += [baseline_row("repo-c", "syntax-only", "search", trial, parse_ns=None, total_ns=None) for trial in range(3)]
            workers.append(write_worker(root / "worker-c", [{"repository": "repo-c", "profile": "syntax-only"}], rows_c))

            result = finalize_baseline.finalize(workers)

        self.assertEqual(result["status"], "evidence_incomplete")
        self.assertEqual(len(result["source_workers"]), 3)
        self.assertEqual(len(result["parse_dominated_groups"]), 1)
        self.assertEqual(result["parse_dominated_groups"][0]["repository"], "repo-a")
        self.assertAlmostEqual(result["baseline_phase_profiles"][0]["extraction_share"], 0.60)
        search = {entry["repository"]: entry for entry in result["search_phase_profiles"]}
        self.assertEqual(search["repo-a"]["parse_classification"], "unavailable")
        repo_c = next(entry for entry in result["baseline_phase_profiles"] if entry["repository"] == "repo-c")
        self.assertFalse(repo_c["eligible"])
        self.assertIn("expected 3 rows, got 2", repo_c["baseline_observation_issues"])

    def test_partial_and_blocked_baselines_are_ineligible(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workers: list[Path] = []
            workers.append(write_worker(root / "worker-a", [{"repository": "repo-a", "profile": "fast"}], [baseline_row("repo-a", "fast", "snapshot", trial) for trial in range(3)]))
            workers.append(write_worker(root / "worker-b", [{"repository": "repo-b", "profile": "fast"}], [baseline_row("repo-b", "fast", "snapshot", trial, status="partial", partial_failures_count=1, partial_failures=[{"code": "E_PARSE_ERROR", "severity": "warning", "effect_on_semantic_completeness": "partial"}]) for trial in range(3)]))
            blocked = [{"repository": "repo-c", "profile": "full", "verb": "snapshot", "reason": "three hard failures"}]
            workers.append(write_worker(root / "worker-c", [{"repository": "repo-c", "profile": "full"}], [], blocked=blocked))

            result = finalize_baseline.finalize(workers)

        profiles = {(entry["repository"], entry["verb"]): entry for entry in result["baseline_phase_profiles"]}
        self.assertFalse(profiles[("repo-b", "snapshot")]["eligible"])
        self.assertIn("status=partial", profiles[("repo-b", "snapshot")]["baseline_observation_issues"])
        self.assertEqual(profiles[("repo-b", "snapshot")]["partial_reasons"]["reasons"][0]["code"], "E_PARSE_ERROR")
        self.assertFalse(profiles[("repo-c", "snapshot")]["eligible"])
        self.assertIn("blocked stratum", profiles[("repo-c", "snapshot")]["baseline_observation_issues"])
        self.assertTrue(result["blocked_strata"])

    def test_worker_identity_mismatch_is_retained_as_incomplete(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workers = [
                write_worker(root / "worker-a", [{"repository": "repo-a", "profile": "fast"}], [baseline_row("repo-a", "fast", "snapshot", trial) for trial in range(3)], binary="bin-a"),
                write_worker(root / "worker-b", [{"repository": "repo-b", "profile": "fast"}], [baseline_row("repo-b", "fast", "snapshot", trial) for trial in range(3)], binary="bin-a", inputs="input-b"),
                write_worker(root / "worker-c", [{"repository": "repo-c", "profile": "fast"}], [baseline_row("repo-c", "fast", "snapshot", trial) for trial in range(3)], binary="bin-c"),
            ]

            result = finalize_baseline.finalize(workers)

        self.assertEqual(result["status"], "evidence_incomplete")
        self.assertTrue(any("shared binary" in issue for issue in result["issues"]))
        self.assertTrue(any("shared input" in issue for issue in result["issues"]))

    def test_requires_three_workers(self) -> None:
        with self.assertRaises(finalize_baseline.InputError):
            finalize_baseline.finalize([])


if __name__ == "__main__":
    unittest.main()

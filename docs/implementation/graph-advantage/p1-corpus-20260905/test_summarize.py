from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("summarize.py")
SPEC = importlib.util.spec_from_file_location("p1_summarize", MODULE_PATH)
assert SPEC and SPEC.loader
summarize = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = summarize
SPEC.loader.exec_module(summarize)


def row(
    *,
    repo: str = "repo-a",
    profile: str = "fast",
    scenario: str = "one-edit",
    verb: str = "snapshot",
    trial: int,
    reuse: bool,
    elapsed_ns: int = 100_000_000,
    status: str = "ok",
    semantic_digest: str = "same-output",
    source_digest: str = "same-input",
    peak_rss_bytes: int = 100,
    unchanged_reparses: int = 0,
    stale_source: bool = False,
) -> dict[str, object]:
    return {
        "repository": repo,
        "profile": profile,
        "scenario": scenario,
        "verb": verb,
        "trial": trial,
        "reuse": reuse,
        "status": status,
        "error": "" if status == "ok" else f"{status} in test",
        "elapsed_ns": elapsed_ns,
        "wall_ns": elapsed_ns + 1,
        "peak_rss_bytes": peak_rss_bytes,
        "cache_bytes": 10,
        "semantic_digest": semantic_digest,
        "source_digest": source_digest,
        "extraction": {
            "unchanged_reparses": unchanged_reparses,
            "stale_source": stale_source,
        },
        "phase_ns": {"parse": 60_000_000, "total": 100_000_000},
        "partial_failures_count": 0,
    }


def paired_rows(**kwargs: object) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    for trial in range(30):
        baseline = row(trial=trial, reuse=False, **kwargs)
        treatment = row(trial=trial, reuse=True, elapsed_ns=70_000_000, **kwargs)
        if trial % 2:
            rows.extend([treatment, baseline])
        else:
            rows.extend([baseline, treatment])
    return rows


def metadata(*, groups: list[dict[str, object]] | None = None) -> dict[str, object]:
    groups = groups or [{"repository": "repo-a", "profile": "fast", "scenario": "one-edit", "verb": "snapshot", "phaseParse_ns": 60_000_000, "total_ns": 100_000_000}]
    return {
        "compiler": "off",
        "ranking": "current",
        "binary_sha256": "binary",
        "input_manifest_sha256": "inputs",
        "baseline_phase_profiles": groups,
    }


class P1ScorerTest(unittest.TestCase):
    def test_paired_benefit_and_bootstrap_are_deterministic(self) -> None:
        observations = paired_rows()
        first = summarize.summarize(observations, metadata(), bootstrap_resamples=200)
        second = summarize.summarize(observations, metadata(), bootstrap_resamples=200)

        self.assertEqual(first["overall"], "pass")
        gate = first["gates"]["one_edit_median_benefit"]
        self.assertEqual(gate["status"], "pass")
        self.assertAlmostEqual(gate["benefit"], 0.30)
        self.assertEqual(first["groups"], second["groups"])

    def test_semantic_mismatch_is_a_measured_failure(self) -> None:
        observations = paired_rows()
        observations[1]["semantic_digest"] = "different"

        result = summarize.summarize(observations, metadata(), bootstrap_resamples=100)

        self.assertEqual(result["overall"], "fail")
        self.assertEqual(result["gates"]["semantic_equivalence"]["status"], "fail")

    def test_timeout_is_retained_and_missing_metric_is_incomplete(self) -> None:
        observations = paired_rows()
        observations[0]["status"] = "timeout"
        observations[0]["error"] = "deadline exceeded"
        observations[0]["elapsed_ns"] = None
        observations[0]["semantic_digest"] = None
        observations[0]["peak_rss_bytes"] = None

        result = summarize.summarize(observations, metadata(), bootstrap_resamples=100)

        self.assertEqual(result["overall"], "evidence_incomplete")
        self.assertEqual(result["observations"]["failures"], 1)
        self.assertEqual(result["observations"]["errors"][0]["kind"], "request_failure")
        self.assertEqual(result["gates"]["one_edit_median_benefit"]["status"], "evidence_incomplete")

    def test_zero_denominator_is_explicitly_na(self) -> None:
        observations = paired_rows()
        for item in observations:
            item["status"] = "timeout"
            item["elapsed_ns"] = None
            item["peak_rss_bytes"] = None
        result = summarize.summarize(observations, metadata(), bootstrap_resamples=100)

        metrics = result["groups"][0]["metrics"]
        self.assertEqual(metrics["baseline"]["total_ms"]["status"], "N/A")
        self.assertEqual(metrics["paired"]["status"], "N/A")

    def test_nonzero_unchanged_reparse_fails_gate(self) -> None:
        observations = paired_rows(scenario="unchanged")
        observations[1]["extraction"]["unchanged_reparses"] = 1  # treatment, trial 0

        result = summarize.summarize(observations, metadata(groups=[{"repository": "repo-a", "profile": "fast", "scenario": "unchanged", "verb": "snapshot", "phaseParse_ns": 60_000_000, "total_ns": 100_000_000}]), bootstrap_resamples=100)

        self.assertEqual(result["gates"]["zero_unchanged_reparses"]["status"], "fail")
        self.assertEqual(result["overall"], "fail")

    def test_cold_rss_regression_is_reported(self) -> None:
        observations = paired_rows(scenario="cold")
        for item in observations:
            if item["reuse"]:
                item["peak_rss_bytes"] = 120
        result = summarize.summarize(observations, metadata(groups=[{"repository": "repo-a", "profile": "fast", "scenario": "cold", "verb": "snapshot", "phaseParse_ns": 60_000_000, "total_ns": 100_000_000}]), bootstrap_resamples=100)

        self.assertEqual(result["gates"]["cold_rss_regression"]["status"], "fail")

    def test_search_phase_classification_is_unavailable(self) -> None:
        observations = paired_rows(verb="search")
        result = summarize.summarize(observations, metadata(groups=[]), bootstrap_resamples=100)

        self.assertEqual(result["groups"][0]["parse_profile"]["parse_classification"], "unavailable")
        self.assertEqual(result["gates"]["one_edit_median_benefit"]["status"], "evidence_incomplete")

    def test_json_ndjson_loader_preserves_top_level_manifest(self) -> None:
        observations = paired_rows()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "raw.ndjson"
            path.write_text("\n".join(json.dumps(item) for item in observations) + "\n", encoding="utf-8")
            loaded, loaded_metadata = summarize._read_json_or_ndjson(path)
        self.assertEqual(len(loaded), 60)
        self.assertEqual(loaded_metadata, {})


if __name__ == "__main__":
    unittest.main()

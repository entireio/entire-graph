from __future__ import annotations

import hashlib
import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from bench.memory.ci import summarize_run


RUN_ID = "full_entire"


class SummarizeRunTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.harness_dir = Path(self.tempdir.name)
        self.results_dir = self.harness_dir / "results" / "locomo"
        self.results_dir.mkdir(parents=True)
        self.code_md5: dict[str, str] = {}
        for name in summarize_run.REQUIRED_CODE_HASHES:
            path = self.harness_dir / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"test content for {name}\n")
            self.code_md5[name] = hashlib.md5(
                path.read_bytes(), usedforsecurity=False
            ).hexdigest()
        self.lock_path = self.harness_dir / summarize_run.LOCK_NAME
        self.lock_md5 = self.code_md5[summarize_run.LOCK_NAME]

    def write_aggregate(
        self,
        *,
        metadata_updates: dict[str, object] | None = None,
        env_updates: dict[str, object] | None = None,
        **overall_updates: object,
    ) -> None:
        overall: dict[str, object] = {"total": 3, "correct": 3, "accuracy": 100.0}
        overall.update(overall_updates)
        code_md5 = dict(self.code_md5)
        env_capture: dict[str, object] = {
            "fair_mode": True,
            "asymmetric_settings_active": {},
            "env": {
                "ENTIRE_CORPUS_ROOT": "/tmp/benchmark/full_entire/entire",
                "FAIR_MODE": "1",
                "LLM_TIMEOUT": "600",
                "MEM0_HOST": "http://localhost:18888",
            },
            "code_md5": code_md5,
            "argv": [
                "/harness/benchmarks/locomo/run.py",
                "--project-name",
                RUN_ID,
                "--backend",
                "entire",
                "--provider",
                "azure_ai",
            ],
        }
        if env_updates:
            env_capture.update(env_updates)
        metadata: dict[str, object] = {
            "run_id": RUN_ID,
            "project_name": RUN_ID,
            "benchmark": "locomo",
            "timestamp": "2026-08-21T00:00:00Z",
            "answerer_model": "gpt-5.6-sol",
            "judge_model": "gpt-5.6-sol",
            "provider": "azure_ai",
            "top_k": 200,
            "top_k_cutoffs": ["top_200"],
            "total_questions": 3,
            "categories": [1, 2, 3, 4],
            "env_capture": env_capture,
        }
        if metadata_updates:
            metadata.update(metadata_updates)
        doc = {
            "metadata": metadata,
            "metrics_by_cutoff": {"top_200": {"overall": overall}},
        }
        (self.results_dir / "locomo_results_test.json").write_text(json.dumps(doc))

    def write_records(self, totals: list[int], scores: list[int] | None = None) -> Path:
        records_dir = self.results_dir / f"predicted_{RUN_ID}"
        records_dir.mkdir(exist_ok=True)
        scores = scores or [1] * len(totals)
        for index, total_results in enumerate(totals):
            score = scores[index]
            record = {
                "question_id": f"conv0_q{index}",
                "category": 1,
                "category_name": "single-hop",
                "question": f"question {index}",
                "retrieval": {
                    "search_dropped": False,
                    "total_results": total_results,
                    "search_results": [
                        {"memory": f"context {item}"}
                        for item in range(total_results)
                    ],
                },
                "cutoff_results": {
                    "top_200": {
                        "judgment": "CORRECT" if score == 1 else "WRONG",
                        "score": float(score),
                        "generated_answer": f"answer {index}",
                        "memories_evaluated": total_results,
                        "reason": "test judgment",
                    }
                },
            }
            (records_dir / f"conv0_q{index}.json").write_text(json.dumps(record))
        return records_dir

    def read_record(self, index: int) -> dict:
        path = self.results_dir / f"predicted_{RUN_ID}" / f"conv0_q{index}.json"
        return json.loads(path.read_text())

    def write_record(self, index: int, record: dict) -> None:
        path = self.results_dir / f"predicted_{RUN_ID}" / f"conv0_q{index}.json"
        path.write_text(json.dumps(record))

    def run_summary(self) -> tuple[int, str]:
        argv = [
            "summarize_run.py",
            "--results-dir",
            str(self.results_dir),
            "--run-id",
            RUN_ID,
        ]
        output = io.StringIO()
        with (
            mock.patch.object(sys, "argv", argv),
            mock.patch.object(summarize_run, "EXPECTED_QUESTIONS", 3),
            redirect_stdout(output),
        ):
            return summarize_run.main(), output.getvalue()

    def test_complete_non_empty_run_is_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 0)
        self.assertIn("### Scoreable", output)

    def test_valid_wrong_judgment_reconciles_with_aggregate(self) -> None:
        self.write_aggregate(correct=2, accuracy=2 / 3 * 100)
        self.write_records([5, 6, 7], scores=[1, 0, 1])

        status, output = self.run_summary()

        self.assertEqual(status, 0)
        self.assertIn("valid top_200 judgments: **3/3**", output)

    def test_missing_top_200_judgment_is_not_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["cutoff_results"].pop("top_200")
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("valid top_200 judgments: **2/3**", output)
        self.assertIn("`cutoff_results.top_200` is missing", output)

    def test_top_200_score_and_judgment_must_agree(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["cutoff_results"]["top_200"]["score"] = 0.0
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("score and judgment disagree", output)

    def test_boolean_top_200_score_is_not_accepted_as_one(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["cutoff_results"]["top_200"]["score"] = True
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("top_200.score` must be the number 0 or 1", output)

    def test_empty_generated_answer_is_not_scoreable(self) -> None:
        self.write_aggregate(correct=2, accuracy=2 / 3 * 100)
        self.write_records([5, 6, 7], scores=[1, 0, 1])
        record = self.read_record(1)
        record["cutoff_results"]["top_200"]["generated_answer"] = "  "
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("top_200.generated_answer` must be a non-empty string", output)

    def test_memories_evaluated_must_match_retrieved_context(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["cutoff_results"]["top_200"]["memories_evaluated"] = 5
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("memories_evaluated` does not match", output)

    def test_aggregate_total_must_match_question_records(self) -> None:
        self.write_aggregate(total=2)
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("aggregate `total` (2) does not match 3 per-question records", output)

    def test_metadata_total_must_match_question_records(self) -> None:
        self.write_aggregate(metadata_updates={"total_questions": 2})
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn(
            "metadata `total_questions` (2) does not match 3 per-question records",
            output,
        )

    def test_aggregate_correct_must_match_question_judgments(self) -> None:
        self.write_aggregate(correct=2)
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn(
            "aggregate `correct` (2) does not match 3 correct per-question judgments",
            output,
        )

    def test_aggregate_accuracy_must_match_question_judgments(self) -> None:
        self.write_aggregate(accuracy=99.0)
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("aggregate `accuracy` (99.0) does not match", output)

    def test_intended_benchmark_metadata_is_required(self) -> None:
        self.write_records([5, 6, 7])
        cases = {
            "benchmark": "longmemeval",
            "project_name": "some_other_run",
            "answerer_model": "other-answerer",
            "judge_model": "other-judge",
            "provider": "openai",
            "top_k": 20,
            "top_k_cutoffs": ["top_20", "top_200"],
        }
        for field, value in cases.items():
            with self.subTest(field=field):
                self.write_aggregate(metadata_updates={field: value})
                status, output = self.run_summary()
                self.assertEqual(status, 1)
                self.assertIn(f"metadata `{field}`", output)
                self.assertIn("expected", output)

    def test_code_md5_provenance_is_required(self) -> None:
        self.write_aggregate(env_updates={"code_md5": None})
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`env_capture.code_md5` is missing or malformed", output)

    def test_llm_runtime_controls_must_match_the_nightly(self) -> None:
        self.write_aggregate(
            env_updates={
                "env": {
                    "ENTIRE_CORPUS_ROOT": "/tmp/benchmark/full_entire/entire",
                    "FAIR_MODE": "1",
                    "LLM_TIMEOUT": "600",
                    "LLM_REASONING_EFFORT": "low",
                    "MEM0_HOST": "http://localhost:18888",
                }
            }
        )
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("captured LLM controls", output)
        self.assertIn("LLM_REASONING_EFFORT", output)

    def test_backend_environment_override_is_not_scoreable(self) -> None:
        self.write_aggregate(
            env_updates={
                "env": {
                    "ENTIRE_CORPUS_ROOT": "/tmp/benchmark/full_entire/entire",
                    "FAIR_MODE": "1",
                    "LLM_TIMEOUT": "600",
                    "MEM0_BACKEND": "oss",
                    "MEM0_HOST": "http://localhost:18888",
                }
            }
        )
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("non-baseline retrieval controls", output)
        self.assertIn("MEM0_BACKEND", output)

    def test_effective_cli_backend_must_be_entire(self) -> None:
        self.write_aggregate(
            env_updates={
                "argv": [
                    "/harness/benchmarks/locomo/run.py",
                    "--backend",
                    "entire",
                    "--backend=oss",
                ]
            }
        )
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("selected backend 'oss'; expected 'entire'", output)

    def test_retrieval_shaping_environment_is_not_scoreable(self) -> None:
        cases = {
            "EG_DEEP": "1",
            "EG_FUTURE_KNOB": "1",
            "ENTIRE_MAX_CONTEXT_BYTES": "4096",
            "ENTIRE_GRAPH_BIN": "/tmp/fake",
            "HARNESS_SEARCH_RETRIES": "20",
        }
        self.write_records([5, 6, 7])
        for name, value in cases.items():
            with self.subTest(name=name):
                self.write_aggregate(
                    env_updates={
                        "env": {
                            "ENTIRE_CORPUS_ROOT": "/tmp/benchmark/full_entire/entire",
                            "FAIR_MODE": "1",
                            "LLM_TIMEOUT": "600",
                            "MEM0_HOST": "http://localhost:18888",
                            name: value,
                        }
                    }
                )
                status, output = self.run_summary()
                self.assertEqual(status, 1)
                self.assertIn("non-baseline retrieval controls", output)
                self.assertIn(name, output)

    def test_explicit_corpus_root_is_required(self) -> None:
        self.write_aggregate(
            env_updates={
                "env": {
                    "FAIR_MODE": "1",
                    "LLM_TIMEOUT": "600",
                    "MEM0_HOST": "http://localhost:18888",
                }
            }
        )
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`ENTIRE_CORPUS_ROOT` is missing or empty", output)

    def test_captured_argv_is_required(self) -> None:
        self.write_aggregate(env_updates={"argv": None})
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`env_capture.argv` is missing or malformed", output)

    def test_asymmetry_metadata_must_be_present_and_structured(self) -> None:
        self.write_aggregate(env_updates={"asymmetric_settings_active": None})
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`env_capture.asymmetric_settings_active`", output)

    def test_code_md5_requires_core_harness_entries(self) -> None:
        self.write_aggregate(
            env_updates={
                "code_md5": {summarize_run.LOCK_NAME: self.lock_md5},
            }
        )
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`code_md5` is missing required entries", output)

    def test_recorded_lock_hash_must_match_reconstructed_lock(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        self.lock_path.write_text("dependency drift after metadata capture\n")

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("the reconstructed lock", output)
        self.assertIn("hashes to", output)

    def test_recorded_source_hash_must_match_reconstructed_harness(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        source = self.harness_dir / "benchmarks" / "common" / "llm_client.py"
        source.write_text("source drift after metadata capture\n")

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("benchmarks/common/llm_client.py", output)
        self.assertIn("reconstructed harness file", output)

    def test_reconstructed_lock_must_exist(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        self.lock_path.unlink()

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("could not find `requirements-lock-py312.txt`", output)

    def test_even_one_empty_context_is_not_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([5, 0, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("zero-context questions: **1**", output)
        self.assertIn("1 question(s) had zero retrieved context", output)

    def test_missing_accuracy_is_not_scoreable(self) -> None:
        self.write_aggregate(accuracy=None)
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("aggregate `accuracy` is missing or is not a finite number", output)

    def test_non_finite_accuracy_is_not_scoreable(self) -> None:
        self.write_aggregate(accuracy=float("nan"))
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("aggregate `accuracy` is missing or is not a finite number", output)

    def test_booleans_are_not_accepted_as_aggregate_numbers(self) -> None:
        self.write_aggregate(total=True, correct=True, accuracy=True)
        self.write_records([5, 6, 7])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("aggregate `total` is missing", output)
        self.assertIn("aggregate `correct` is missing", output)
        self.assertIn("aggregate `accuracy` is missing", output)

    def test_missing_per_question_record_is_not_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("incomplete per-question records: found 2, expected 3", output)

    def test_question_id_must_match_its_record_filename(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["question_id"] = "conv9_q999"
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`question_id` does not match", output)

    def test_malformed_per_question_record_is_not_scoreable(self) -> None:
        self.write_aggregate()
        records_dir = self.write_records([5, 6, 7])
        (records_dir / "conv0_q1.json").write_text("{not json")

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("1 per-question record(s) were malformed", output)
        self.assertIn("conv0_q1.json: unreadable JSON", output)

    def test_missing_retrieval_integrity_fields_are_not_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["retrieval"] = {}
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`retrieval.search_dropped` is missing or not boolean", output)
        self.assertIn("`retrieval.total_results` is missing", output)
        self.assertIn("`retrieval.search_results` is missing or not a list", output)

    def test_empty_search_results_cannot_hide_behind_a_positive_count(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["retrieval"]["search_results"] = []
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("length does not match `total_results`", output)
        self.assertIn("zero-context questions: **1**", output)
        self.assertIn("1 question(s) had zero retrieved context", output)

    def test_empty_search_result_payloads_are_not_context(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["retrieval"]["search_results"] = [{}] * 6
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("without non-empty `memory` text", output)
        self.assertIn("zero-context questions: **1**", output)

    def test_boolean_result_count_is_not_accepted_as_an_integer(self) -> None:
        self.write_aggregate()
        self.write_records([5, 6, 7])
        record = self.read_record(1)
        record["retrieval"]["total_results"] = True
        self.write_record(1, record)

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("`retrieval.total_results` is missing", output)

    def test_all_zero_context_records_are_not_scoreable(self) -> None:
        self.write_aggregate()
        self.write_records([0, 0, 0])

        status, output = self.run_summary()

        self.assertEqual(status, 1)
        self.assertIn("3 question(s) had zero retrieved context", output)


if __name__ == "__main__":
    unittest.main()

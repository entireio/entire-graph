#!/usr/bin/env python3
"""Summarise one nightly LoCoMo run and decide whether it is scoreable.

This exists because a nightly benchmark's failure mode is not "the score moved".
It is "the harness broke and produced a plausible-looking number anyway" — the
class documented in TokensNotes 5 and in LOCOMO-COMPARISON.md 8, where every
withdrawn headline in this campaign died of a harness bug rather than a real
regression.

So the gate here checks COMPLETENESS and INTEGRITY, and deliberately does NOT
fail on a score delta. The measured run-to-run noise floor is 0.65pp (two
identical mem0 runs scored 93.83 and 93.57) and an identical entire-graph config
re-run 26 hours apart drifted 2.21pt (LOCOMO-COMPARISON.md 7). A nightly job
that alerts on a 1-point move would be alerting on noise every few nights, and
the alert would be ignored by the end of the first week.

Exit codes:
  0  run is complete and scoreable (whatever the score is)
  1  run is NOT scoreable — incomplete, malformed, empty, dropped, or missing data
"""

from __future__ import annotations

import argparse
import glob
import hashlib
import json
import math
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path

EXPECTED_QUESTIONS = 1540
EXPECTED_BENCHMARK = "locomo"
EXPECTED_MODEL = "gpt-5.6-sol"
EXPECTED_PROVIDER = "azure_ai"
EXPECTED_TOP_K = 200
EXPECTED_LLM_ENV = {"LLM_TIMEOUT": "600"}
ALLOWED_ENTIRE_ENV = frozenset({"ENTIRE_CORPUS_ROOT"})
ALLOWED_MEM0_ENV = frozenset({"MEM0_HOST"})
LOCK_NAME = "requirements-lock-py312.txt"
REQUIRED_CODE_HASHES = frozenset(
    {
        "benchmarks/locomo/run.py",
        "benchmarks/locomo/prompts.py",
        "benchmarks/common/entire_client.py",
        "benchmarks/common/entra_auth.py",
        "benchmarks/common/llm_client.py",
        "benchmarks/common/mem0_client.py",
        "benchmarks/common/metrics.py",
        "benchmarks/common/runmeta.py",
        "benchmarks/common/utils.py",
        LOCK_NAME,
    }
)

# Below this, the cause is a broken harness rather than a regression: no
# published arm in this comparison has ever scored under 77, and the arms that
# looked catastrophic always turned out to be ports that never invoked their own
# tool. Treated as an integrity failure so it is loud rather than logged.
IMPLAUSIBLE_BELOW = 60.0


@dataclass(frozen=True)
class QuestionRecordAudit:
    """Integrity facts collected from the per-question result files."""

    files: int
    scored: int
    correct: int
    dropped: int
    zero_context: int
    malformed: tuple[str, ...]


def is_finite_number(value: object) -> bool:
    """Return true for finite JSON numbers, excluding booleans and huge ints."""
    if type(value) not in (int, float):
        return False
    try:
        return math.isfinite(float(value))
    except OverflowError:
        return False


def last_argv_option(argv: list[str], option: str) -> str | None:
    """Return argparse's effective (last) value for one scalar option."""
    value = None
    for index, item in enumerate(argv):
        if item == option:
            value = argv[index + 1] if index + 1 < len(argv) else None
        elif item.startswith(option + "="):
            value = item.partition("=")[2]
    return value


def find_requirements_lock(results_dir: str) -> Path | None:
    """Find the reconstructed harness lock by walking up from its results dir."""
    current = Path(results_dir).resolve()
    # The workflow uses <harness>/results/locomo. Bound the search to that
    # context so an unrelated lock near the filesystem root cannot satisfy it.
    for directory in (current, *current.parents[:2]):
        candidate = directory / LOCK_NAME
        if candidate.is_file():
            return candidate
    return None


def md5_file(path: Path) -> str:
    digest = hashlib.md5(usedforsecurity=False)
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate_code_provenance(env: dict, results_dir: str) -> list[str]:
    """Validate captured source hashes and bind the lock hash to the real file."""
    failures: list[str] = []
    code_md5 = env.get("code_md5")
    if not isinstance(code_md5, dict) or not code_md5:
        return ["metadata `env_capture.code_md5` is missing or malformed"]

    missing = sorted(REQUIRED_CODE_HASHES - set(code_md5))
    if missing:
        failures.append(f"metadata `code_md5` is missing required entries: {missing}")

    invalid = sorted(
        str(name)
        for name, digest in code_md5.items()
        if not isinstance(name, str)
        or not isinstance(digest, str)
        or re.fullmatch(r"[0-9a-f]{32}", digest) is None
    )
    if invalid:
        failures.append(f"metadata `code_md5` has invalid MD5 values for: {invalid}")

    lock_path = find_requirements_lock(results_dir)
    if lock_path is None:
        failures.append(
            f"could not find `{LOCK_NAME}` in the reconstructed harness above "
            f"{Path(results_dir).resolve()}"
        )
        return failures

    harness_root = lock_path.parent
    for name in sorted(REQUIRED_CODE_HASHES):
        if name not in code_md5 or name in invalid:
            continue
        source_path = harness_root / name
        if not source_path.is_file():
            failures.append(
                f"required reconstructed harness file `{name}` does not exist under "
                f"{harness_root}"
            )
            continue
        try:
            actual_md5 = md5_file(source_path)
        except OSError as exc:
            failures.append(f"could not hash reconstructed harness file {source_path}: {exc}")
            continue
        recorded_md5 = code_md5[name]
        if recorded_md5 == actual_md5:
            continue
        if name == LOCK_NAME:
            failures.append(
                f"metadata `code_md5[{LOCK_NAME!r}]` is {recorded_md5!r}, but "
                f"the reconstructed lock at {source_path} hashes to {actual_md5}"
            )
        else:
            failures.append(
                f"metadata `code_md5[{name!r}]` is {recorded_md5!r}, but the "
                f"reconstructed harness file at {source_path} hashes to {actual_md5}"
            )
    return failures


def find_result(results_dir: str, run_id: str) -> dict | None:
    """Return the result file whose metadata.run_id matches, newest first.

    Matching on metadata rather than on filename mtime is deliberate: a stale
    diagnostic file sitting in the same directory has repeatedly been mistaken
    for a finished run when selection was done by timestamp.
    """
    best = None
    for path in sorted(glob.glob(os.path.join(results_dir, "locomo_results_*.json"))):
        try:
            with open(path) as fh:
                doc = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        if not isinstance(doc, dict):
            continue
        metadata = doc.get("metadata")
        if not isinstance(metadata, dict) or metadata.get("run_id") != run_id:
            continue
        stamp = str(metadata.get("timestamp", ""))
        if best is None or stamp >= best[0]:
            best = (stamp, path, doc)
    return {"path": best[1], "doc": best[2]} if best else None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--results-dir", required=True)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--summary", help="path to write a GitHub step summary")
    args = ap.parse_args()

    lines: list[str] = ["## LoCoMo nightly — entire-graph", ""]
    failures: list[str] = []

    found = find_result(args.results_dir, args.run_id)
    if found is None:
        failures.append(
            f"no result file with `metadata.run_id == {args.run_id}` in {args.results_dir} — "
            "the run did not reach the scoring stage"
        )
        lines.append(f"**NOT SCOREABLE** — {failures[0]}")
        write(args.summary, lines)
        for f in failures:
            print(f"::error::{f}")
        return 1

    doc = found["doc"]
    meta = doc.get("metadata")
    if not isinstance(meta, dict):
        # find_result currently guarantees this, but keep the validation local
        # so a future selection change cannot turn malformed metadata into a
        # successful score.
        meta = {}
        failures.append("result metadata is missing or malformed")

    metrics = doc.get("metrics_by_cutoff")
    top_200 = metrics.get("top_200") if isinstance(metrics, dict) else None
    overall = top_200.get("overall") if isinstance(top_200, dict) else None
    if not isinstance(overall, dict):
        overall = {}
        failures.append("aggregate `metrics_by_cutoff.top_200.overall` is missing or malformed")

    total = overall.get("total")
    correct = overall.get("correct")
    accuracy = overall.get("accuracy")
    total_valid = type(total) is int and total >= 0
    correct_valid = type(correct) is int and correct >= 0
    accuracy_valid = is_finite_number(accuracy)

    if not total_valid:
        failures.append("aggregate `total` is missing or is not a non-negative integer")
    if not correct_valid:
        failures.append("aggregate `correct` is missing or is not a non-negative integer")
    elif total_valid and correct > total:
        failures.append(f"aggregate `correct` ({correct}) exceeds `total` ({total})")
    if not accuracy_valid:
        failures.append("aggregate `accuracy` is missing or is not a finite number")
    elif not 0.0 <= float(accuracy) <= 100.0:
        failures.append(f"aggregate `accuracy` ({accuracy}) is outside the 0..100 range")

    expected_text_metadata = {
        "benchmark": EXPECTED_BENCHMARK,
        "project_name": args.run_id,
        "answerer_model": EXPECTED_MODEL,
        "judge_model": EXPECTED_MODEL,
        "provider": EXPECTED_PROVIDER,
    }
    for field, expected in expected_text_metadata.items():
        actual = meta.get(field)
        if actual != expected:
            failures.append(
                f"metadata `{field}` is {actual!r}; expected {expected!r} for this nightly"
            )
    top_k = meta.get("top_k")
    if type(top_k) is not int or top_k != EXPECTED_TOP_K:
        failures.append(
            f"metadata `top_k` is {top_k!r}; expected {EXPECTED_TOP_K} for this nightly"
        )
    top_k_cutoffs = meta.get("top_k_cutoffs")
    if top_k_cutoffs != ["top_200"]:
        failures.append(
            f"metadata `top_k_cutoffs` is {top_k_cutoffs!r}; expected ['top_200'] "
            "for this nightly"
        )
    metadata_total = meta.get("total_questions")
    if type(metadata_total) is not int or metadata_total < 0:
        failures.append(
            "metadata `total_questions` is missing or is not a non-negative integer"
        )

    lines += [
        f"| field | value |",
        f"| --- | --- |",
        (
            f"| score (top_200) | **{accuracy:.2f}** |"
            if accuracy_valid
            else "| score (top_200) | missing or invalid |"
        ),
        (
            "| correct | "
            f"{correct if correct_valid else 'missing or invalid'}/"
            f"{total if total_valid else 'missing or invalid'} |"
        ),
        f"| run id | `{meta.get('run_id')}` |",
        f"| timestamp | `{meta.get('timestamp')}` |",
        f"| answerer / judge | `{meta.get('answerer_model')}` / `{meta.get('judge_model')}` |",
        f"| top_k | {top_k} |",
        f"| artifact | `{os.path.basename(found['path'])}` |",
        "",
    ]

    # --- integrity gate -----------------------------------------------------
    if total_valid and total != EXPECTED_QUESTIONS:
        failures.append(
            f"incomplete corpus: scored {total} questions, expected {EXPECTED_QUESTIONS}. "
            "A partial run is not comparable to a published row."
        )

    env = meta.get("env_capture")
    if not isinstance(env, dict):
        env = {}
        failures.append("metadata `env_capture` is missing or malformed")
    if env.get("fair_mode") is not True:
        failures.append(
            "FAIR_MODE was not active — the arm-asymmetry guard did not run, so this "
            "run cannot be published even if the number looks right"
        )
    asym = env.get("asymmetric_settings_active")
    if not isinstance(asym, dict):
        failures.append(
            "metadata `env_capture.asymmetric_settings_active` is missing or malformed"
        )
    elif asym:
        failures.append(f"arm-asymmetric settings were active: {asym}")
    captured_env = env.get("env")
    if not isinstance(captured_env, dict):
        failures.append("metadata `env_capture.env` is missing or malformed")
    else:
        llm_env = {
            name: value
            for name, value in captured_env.items()
            if isinstance(name, str) and name.startswith("LLM_")
        }
        if llm_env != EXPECTED_LLM_ENV:
            failures.append(
                f"captured LLM controls are {llm_env!r}; expected "
                f"{EXPECTED_LLM_ENV!r} for this nightly"
            )

        corpus_root = captured_env.get("ENTIRE_CORPUS_ROOT")
        if not isinstance(corpus_root, str) or not corpus_root.strip():
            failures.append(
                "captured `ENTIRE_CORPUS_ROOT` is missing or empty; the nightly "
                "launcher must use its explicit benchmark state root"
            )

        unexpected_retrieval_env = {
            name: value
            for name, value in captured_env.items()
            if isinstance(name, str)
            and (
                name.startswith("EG_")
                or name.startswith("HARNESS_")
                or (name.startswith("ENTIRE_") and name not in ALLOWED_ENTIRE_ENV)
                or (name.startswith("MEM0_") and name not in ALLOWED_MEM0_ENV)
            )
        }
        if unexpected_retrieval_env:
            failures.append(
                "non-baseline retrieval controls were active: "
                f"{unexpected_retrieval_env!r}"
            )

    captured_argv = env.get("argv")
    if not isinstance(captured_argv, list) or not all(
        isinstance(item, str) for item in captured_argv
    ):
        failures.append("metadata `env_capture.argv` is missing or malformed")
    else:
        backend = last_argv_option(captured_argv, "--backend")
        if backend != "entire":
            failures.append(
                f"captured command selected backend {backend!r}; expected 'entire' "
                "for this nightly"
            )
    failures.extend(validate_code_provenance(env, args.results_dir))

    # Dropped searches: retry-exhausted retrievals scored as capability misses.
    # This is the defect patch 0003 exists to surface; if it fires, the number
    # measures infrastructure, not retrieval.
    audit = audit_question_records(args.results_dir, args.run_id)
    lines.append(f"- per-question records: **{audit.files}/{EXPECTED_QUESTIONS}**")
    lines.append(f"- valid top_200 judgments: **{audit.scored}/{audit.files}**")
    lines.append(f"- dropped searches: **{audit.dropped}**")
    lines.append(f"- zero-context questions: **{audit.zero_context}**")
    if audit.files != EXPECTED_QUESTIONS:
        failures.append(
            f"incomplete per-question records: found {audit.files}, expected "
            f"{EXPECTED_QUESTIONS}. Drop and context accounting is not complete."
        )
    if audit.malformed:
        preview = "; ".join(audit.malformed[:5])
        if len(audit.malformed) > 5:
            preview += f"; and {len(audit.malformed) - 5} more"
        failures.append(
            f"{len(audit.malformed)} per-question record(s) were malformed: {preview}"
        )
    if audit.dropped > 0:
        failures.append(
            f"{audit.dropped} question(s) had a retry-exhausted search. Published rows "
            "require zero drops."
        )
    if audit.zero_context > 0:
        failures.append(
            f"{audit.zero_context} question(s) had zero retrieved context. The "
            "entire-graph nightly requires evidence for every question."
        )

    if total_valid and total != audit.files:
        failures.append(
            f"aggregate `total` ({total}) does not match {audit.files} per-question records"
        )
    if type(metadata_total) is int and metadata_total != audit.files:
        failures.append(
            f"metadata `total_questions` ({metadata_total}) does not match "
            f"{audit.files} per-question records"
        )
    all_records_scored = audit.scored == audit.files
    if all_records_scored and correct_valid and correct != audit.correct:
        failures.append(
            f"aggregate `correct` ({correct}) does not match {audit.correct} "
            "correct per-question judgments"
        )
    if all_records_scored and accuracy_valid and audit.files > 0:
        expected_accuracy = audit.correct / audit.files * 100
        if not math.isclose(
            float(accuracy), expected_accuracy, rel_tol=0.0, abs_tol=1e-9
        ):
            failures.append(
                f"aggregate `accuracy` ({accuracy}) does not match per-question "
                f"accuracy ({expected_accuracy:.12g})"
            )

    if accuracy_valid and 0.0 <= float(accuracy) < IMPLAUSIBLE_BELOW:
        failures.append(
            f"score {accuracy:.2f} is below the {IMPLAUSIBLE_BELOW} plausibility floor — "
            "treat as a broken harness until proven otherwise, not as a regression"
        )

    lines.append("")
    if failures:
        lines.append("### NOT SCOREABLE")
        lines += [f"- {f}" for f in failures]
    else:
        lines.append("### Scoreable")
        lines.append(
            "Integrity checks passed. Note the score is NOT compared against a "
            "threshold: run-to-run noise here is 0.65pp, and an identical config "
            "has drifted 2.21pt between runs, so a single night's move is not "
            "evidence of a regression. Compare trends across runs, not neighbours."
        )

    write(args.summary, lines)
    for f in failures:
        print(f"::error::{f}")
    return 1 if failures else 0


def audit_question_records(results_dir: str, run_id: str) -> QuestionRecordAudit:
    """Audit completeness, parseability, drops, and retrieved context.

    This gate is specific to the entire-graph nightly, whose validity checklist
    requires evidence for every question. The generic comparison documents a
    disclosed empty-context exception for supermemory, but that exception does
    not apply here. Missing or malformed fields fail closed rather than silently
    being treated as false/zero.
    """
    pattern = os.path.join(
        results_dir, f"predicted_{glob.escape(run_id)}", "conv*_q*.json"
    )
    files = sorted(glob.glob(pattern))
    scored = 0
    correct = 0
    dropped = 0
    zero_context = 0
    malformed: list[str] = []
    for path in files:
        record_name = os.path.basename(path)
        try:
            with open(path) as fh:
                rec = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            malformed.append(f"{record_name}: unreadable JSON ({exc})")
            continue
        if not isinstance(rec, dict):
            malformed.append(f"{record_name}: root is not an object")
            continue

        record_errors: list[str] = []
        if rec.get("question_id") != Path(path).stem:
            record_errors.append("`question_id` does not match the per-question filename")

        total_results: object = None
        retrieval = rec.get("retrieval")
        if not isinstance(retrieval, dict):
            record_errors.append("`retrieval` is missing or not an object")
        else:
            search_dropped = retrieval.get("search_dropped")
            if type(search_dropped) is not bool:
                record_errors.append(
                    "`retrieval.search_dropped` is missing or not boolean"
                )
            elif search_dropped:
                dropped += 1

            total_results = retrieval.get("total_results")
            if type(total_results) is not int or total_results < 0:
                record_errors.append(
                    "`retrieval.total_results` is missing or not a non-negative integer"
                )

            search_results = retrieval.get("search_results")
            if not isinstance(search_results, list):
                record_errors.append(
                    "`retrieval.search_results` is missing or not a list"
                )
                zero_by_results = False
            else:
                if type(total_results) is int and total_results >= 0 and (
                    len(search_results) != total_results
                ):
                    record_errors.append(
                        "`retrieval.search_results` length does not match `total_results`"
                    )
                usable_contexts = sum(
                    1
                    for item in search_results
                    if isinstance(item, dict)
                    and isinstance(item.get("memory"), str)
                    and item["memory"].strip()
                )
                invalid_contexts = len(search_results) - usable_contexts
                if invalid_contexts:
                    record_errors.append(
                        "`retrieval.search_results` contains "
                        f"{invalid_contexts} item(s) without non-empty `memory` text"
                    )
                zero_by_results = not search_results or usable_contexts == 0
            zero_by_count = type(total_results) is int and total_results == 0
            if zero_by_count or zero_by_results:
                zero_context += 1

        cutoff_results = rec.get("cutoff_results")
        top_200 = (
            cutoff_results.get("top_200")
            if isinstance(cutoff_results, dict)
            else None
        )
        if not isinstance(top_200, dict):
            record_errors.append("`cutoff_results.top_200` is missing or not an object")
        else:
            judgment = top_200.get("judgment")
            if judgment not in ("CORRECT", "WRONG"):
                record_errors.append(
                    "`cutoff_results.top_200.judgment` must be `CORRECT` or `WRONG`"
                )
            score = top_200.get("score")
            score_valid = is_finite_number(score) and float(score) in (0.0, 1.0)
            if not score_valid:
                record_errors.append(
                    "`cutoff_results.top_200.score` must be the number 0 or 1"
                )
            # The judge's rationale is the only field that separates a real
            # verdict from a judge failure. When every structured judge attempt
            # fails the harness falls back to an empty payload, which serializes
            # with exactly the shape of a legitimate negative verdict -- a
            # generated answer, judgment WRONG, score 0 -- and no rationale.
            # Accepting that shape counted a judge outage as a scored wrong
            # answer, so the outage depressed accuracy while the run still
            # declared itself scoreable, which is the failure this gate exists
            # to catch. Require the payload the judge only writes when it ran.
            reason = top_200.get("reason")
            reason_valid = isinstance(reason, str) and bool(reason.strip())
            if not reason_valid:
                record_errors.append(
                    "`cutoff_results.top_200.reason` must be a non-empty string "
                    "recording the judge's verdict"
                )
            if judgment in ("CORRECT", "WRONG") and score_valid and reason_valid:
                expected_judgment = "CORRECT" if float(score) == 1.0 else "WRONG"
                if judgment != expected_judgment:
                    record_errors.append(
                        "`cutoff_results.top_200` score and judgment disagree"
                    )
                else:
                    scored += 1
                    if judgment == "CORRECT":
                        correct += 1

            generated_answer = top_200.get("generated_answer")
            if not isinstance(generated_answer, str) or not generated_answer.strip():
                record_errors.append(
                    "`cutoff_results.top_200.generated_answer` must be a non-empty string"
                )

            memories_evaluated = top_200.get("memories_evaluated")
            if type(memories_evaluated) is not int or memories_evaluated < 0:
                record_errors.append(
                    "`cutoff_results.top_200.memories_evaluated` must be a "
                    "non-negative integer"
                )
            elif type(total_results) is int and total_results >= 0:
                expected_memories = min(total_results, EXPECTED_TOP_K)
                if memories_evaluated != expected_memories:
                    record_errors.append(
                        "`cutoff_results.top_200.memories_evaluated` does not match "
                        "the retrieved top-200 context"
                    )

        if record_errors:
            malformed.append(f"{record_name}: {', '.join(record_errors)}")

    return QuestionRecordAudit(
        files=len(files),
        scored=scored,
        correct=correct,
        dropped=dropped,
        zero_context=zero_context,
        malformed=tuple(malformed),
    )


def write(path: str | None, lines: list[str]) -> None:
    text = "\n".join(lines) + "\n"
    print(text)
    if path:
        with open(path, "a") as fh:
            fh.write(text)


if __name__ == "__main__":
    sys.exit(main())

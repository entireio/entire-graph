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
  1  run is NOT scoreable — incomplete, dropped searches, or missing artifact
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import sys

EXPECTED_QUESTIONS = 1540

# Below this, the cause is a broken harness rather than a regression: no
# published arm in this comparison has ever scored under 77, and the arms that
# looked catastrophic always turned out to be ports that never invoked their own
# tool. Treated as an integrity failure so it is loud rather than logged.
IMPLAUSIBLE_BELOW = 60.0


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
        if doc.get("metadata", {}).get("run_id") != run_id:
            continue
        stamp = str(doc.get("metadata", {}).get("timestamp", ""))
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
    meta = doc.get("metadata", {})
    overall = doc.get("metrics_by_cutoff", {}).get("top_200", {}).get("overall", {})

    total = overall.get("total", 0)
    correct = overall.get("correct", 0)
    accuracy = overall.get("accuracy")

    lines += [
        f"| field | value |",
        f"| --- | --- |",
        f"| score (top_200) | **{accuracy:.2f}** |" if accuracy is not None else "| score | missing |",
        f"| correct | {correct}/{total} |",
        f"| run id | `{meta.get('run_id')}` |",
        f"| timestamp | `{meta.get('timestamp')}` |",
        f"| answerer / judge | `{meta.get('answerer_model')}` / `{meta.get('judge_model')}` |",
        f"| top_k | {meta.get('top_k')} |",
        f"| artifact | `{os.path.basename(found['path'])}` |",
        "",
    ]

    # --- integrity gate -----------------------------------------------------
    if total != EXPECTED_QUESTIONS:
        failures.append(
            f"incomplete corpus: scored {total} questions, expected {EXPECTED_QUESTIONS}. "
            "A partial run is not comparable to a published row."
        )

    env = meta.get("env_capture", {}) or {}
    if env.get("fair_mode") is not True:
        failures.append(
            "FAIR_MODE was not active — the arm-asymmetry guard did not run, so this "
            "run cannot be published even if the number looks right"
        )
    asym = env.get("asymmetric_settings_active")
    if asym:
        failures.append(f"arm-asymmetric settings were active: {asym}")

    # Dropped searches: retry-exhausted retrievals scored as capability misses.
    # This is the defect patch 0003 exists to surface; if it fires, the number
    # measures infrastructure, not retrieval.
    dropped = count_dropped(args.results_dir, args.run_id)
    if dropped is None:
        lines.append("_per-question records not found; drop accounting skipped_")
    else:
        lines.append(f"- dropped searches: **{dropped}**")
        if dropped > 0:
            failures.append(
                f"{dropped} question(s) had a retry-exhausted search. Published rows "
                "require zero drops."
            )

    if accuracy is not None and accuracy < IMPLAUSIBLE_BELOW:
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


def count_dropped(results_dir: str, run_id: str) -> int | None:
    pattern = os.path.join(results_dir, f"predicted_{run_id}", "conv*_q*.json")
    files = glob.glob(pattern)
    if not files:
        return None
    dropped = 0
    for path in files:
        try:
            with open(path) as fh:
                rec = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        if rec.get("retrieval", {}).get("search_dropped"):
            dropped += 1
    return dropped


def write(path: str | None, lines: list[str]) -> None:
    text = "\n".join(lines) + "\n"
    print(text)
    if path:
        with open(path, "a") as fh:
            fh.write(text)


if __name__ == "__main__":
    sys.exit(main())

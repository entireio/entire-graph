#!/usr/bin/env python3
"""Contracts for consumers migrated from raw evidence to canonical records."""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
CANONICAL = HERE.parent / "evidence" / "canonical-v1" / "runs"
sys.path.insert(0, str(HERE))

from canonical_evidence import load_run_and_rows

SUMMARY_CASES = {
    "compiler-quality-v1": ("summarize_compiler_quality.py", "19e02665a56293465b96ab372dbbac960c19dc08a2757362ea00733a61ef1065"),
    "extraction-paired-v1": ("summarize_extraction.py", "07f8c276c70238113605fdf39f7b2d9cdd6f7f790989d17c16f51cf8bce827e1"),
    "extraction-paired-v2": ("summarize_extraction.py", "a8723a3672d4a1fd0e9e33efd26f69e1bf5047496d336a176b8cb5ac90229708"),
    "relation-profile-v1": ("summarize_relation_profile.py", "e385677c2a28306367f65041657c8212f3569641d8a3b72ce74332f861a79899"),
}

CORRECTNESS_PROJECTION_DIGESTS = {
    "correctness-05ad9842-20260906": "4e422bea0a7cf6279b057526502e56eb1ff0c55f3d55e51e3e73feccc9998cc1",
    "correctness-0c9e80f5-20260906": "3a05cd8518ad9cb451d7a62abb48113e1a47a202f71428ffc8ca940bdfcb1291",
    "correctness-1c0b8e24-20260906": "52877728fc9f9ed374cc43165860ddc2e4b231b40cd9fc9969b03c322528e8bf",
    "correctness-6cf92c9c-20260906": "fed9ef67be7c26767f28576bf81ff73267b7789f6b8793b4f683aff37addf65a",
    "correctness-d793b2be-20260906": "a2cc98a45e883a595fea8379d2eba28aff8c31d51ae978155e4077852a05d034",
}


def digest(value: object) -> str:
    payload = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()


class CanonicalConsumerTest(unittest.TestCase):
    def test_summary_outputs_match_retained_legacy_outputs(self) -> None:
        for run_id, (script_name, expected) in SUMMARY_CASES.items():
            with self.subTest(run_id=run_id):
                completed = subprocess.run(
                    [sys.executable, str(HERE / script_name), str(CANONICAL / f"{run_id}.json")],
                    check=True,
                    capture_output=True,
                )
                self.assertEqual(hashlib.sha256(completed.stdout).hexdigest(), expected)

    def test_correctness_projections_preserve_ordered_public_observations(self) -> None:
        for run_id, expected in CORRECTNESS_PROJECTION_DIGESTS.items():
            with self.subTest(run_id=run_id):
                run, rows = load_run_and_rows(CANONICAL / f"{run_id}.json")
                self.assertEqual(len(rows), 5)
                self.assertEqual([row["response_order"] for row in rows], list(range(5)))
                self.assertEqual(digest(rows), expected)
                self.assertEqual(
                    run["redactions"],
                    [
                        {
                            "path": f"/cases/{index}/observed/response/repo_root",
                            "reason": "local filesystem path omitted",
                        }
                        for index in range(5)
                    ]
                    + [{"path": "/source_records/verification.json/command", "reason": "local filesystem path omitted"}]
                    + (
                        [{"path": "/source_records/verification.json/source_archive_local", "reason": "local filesystem path omitted"}]
                        if run_id in {"correctness-1c0b8e24-20260906", "correctness-6cf92c9c-20260906"}
                        else []
                    ),
                )


if __name__ == "__main__":
    unittest.main()

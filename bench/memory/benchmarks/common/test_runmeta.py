"""Tests for benchmark run metadata capture."""

from __future__ import annotations

import hashlib
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

try:
    from benchmarks.common import runmeta
except ModuleNotFoundError:
    from bench.memory.benchmarks.common import runmeta


class CodeHashesTest(unittest.TestCase):
    @patch.dict(
        "os.environ",
        {"LLM_TIMEOUT": "600", "LLM_REASONING_EFFORT": "low"},
        clear=True,
    )
    def test_env_snapshot_includes_llm_controls(self) -> None:
        self.assertEqual(
            runmeta.env_snapshot(),
            {"LLM_REASONING_EFFORT": "low", "LLM_TIMEOUT": "600"},
        )

    def test_hashes_entra_helper_and_dependency_lock(self) -> None:
        with tempfile.TemporaryDirectory() as tempdir:
            harness = Path(tempdir)
            benchmarks = harness / "benchmarks"
            common = benchmarks / "common"
            common.mkdir(parents=True)
            (common / "entra_auth.py").write_text("auth helper\n", encoding="utf-8")
            (harness / "requirements-lock-py312.txt").write_text(
                "locked dependency\n", encoding="utf-8"
            )

            hashes = runmeta.code_hashes(benchmarks)

        self.assertEqual(
            hashes["benchmarks/common/entra_auth.py"],
            hashlib.md5(b"auth helper\n").hexdigest(),
        )
        self.assertEqual(
            hashes["requirements-lock-py312.txt"],
            hashlib.md5(b"locked dependency\n").hexdigest(),
        )


if __name__ == "__main__":
    unittest.main()

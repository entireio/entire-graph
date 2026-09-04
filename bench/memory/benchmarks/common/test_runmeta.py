"""Tests for benchmark run metadata capture."""

from __future__ import annotations

import hashlib
import re
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


# Synthetic credential material. Every value below is fabricated for the test;
# no real credential is ever constructed, printed, or asserted on.
FAKE_KEY = "sk-FAKETESTKEY-0000000000000000"
FAKE_KEY_2 = "sk-FAKETESTKEY-1111111111111111"
FAKE_PASSWORD = "FAKETESTPASSWORD-2222"


class RedactArgvTest(unittest.TestCase):
    """The provenance block is published; argv must not carry a credential.

    Without redaction `runmeta.capture()["argv"]` is `list(sys.argv)`, so the
    documented `--mem0-api-key` option writes the key verbatim into every result
    artifact.
    """

    def test_known_secret_option_keeps_its_name_and_loses_its_value(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["/h/.venv/bin/python", "--mem0-api-key", FAKE_KEY]),
            ["python", "--mem0-api-key", "<redacted>"],
        )

    def test_known_secret_option_in_equals_form_loses_its_value(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["run.py", f"--mem0-api-key={FAKE_KEY}"]),
            ["run.py", "--mem0-api-key=<redacted>"],
        )

    def test_unknown_option_and_its_value_are_both_removed(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["run.py", "--api-key", FAKE_KEY, "--token", FAKE_KEY_2]),
            ["run.py", "<redacted>", "<redacted>", "<redacted>", "<redacted>"],
        )

    def test_environment_prefix_and_bare_positional_are_removed(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["run.py", f"AZURE_AI_API_KEY={FAKE_KEY}", FAKE_KEY_2]),
            ["run.py", "<redacted>", "<redacted>"],
        )

    def test_url_userinfo_is_stripped_from_an_allowlisted_value(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(
                ["run.py", "--mem0-host", f"https://admin:{FAKE_PASSWORD}@mem0.local/v1"]
            ),
            ["run.py", "--mem0-host", "https://<redacted>@mem0.local/v1"],
        )

    def test_no_synthetic_credential_survives_any_argv_shape(self) -> None:
        """The property that matters: nothing secret reaches the artifact."""
        argv = [
            "/home/runner/.venv/bin/python",
            "--mem0-api-key", FAKE_KEY,
            f"--mem0-api-key={FAKE_KEY}",
            "--api-key", FAKE_KEY,
            f"--api-key={FAKE_KEY}",
            "--API-KEY", FAKE_KEY,
            f"AZURE_AI_API_KEY={FAKE_KEY}",
            f"BEARER={FAKE_KEY}",
            FAKE_KEY,
            "-k", FAKE_KEY,
            "--mem0-host", f"https://admin:{FAKE_PASSWORD}@mem0.local/",
        ]
        rendered = " ".join(runmeta.redact_argv(argv))
        self.assertNotIn(FAKE_KEY, rendered)
        self.assertNotIn(FAKE_PASSWORD, rendered)

    def test_configuration_survives_redaction(self) -> None:
        """ci/summarize_run.py reads the arm back out of the captured argv."""
        argv = [
            "/h/.venv/bin/python", "--project-name", "full_entire",
            "--backend", "entire", "--provider", "azure_ai",
            "--answerer-model", "gpt-5.6-sol", "--top-k=200",
            "--max-workers", "3", "--question-workers", "10", "--rpm", "60",
            "--resume", "--run-id", "full_entire",
        ]
        self.assertEqual(
            runmeta.redact_argv(argv),
            ["python", "--project-name", "full_entire", "--backend", "entire",
             "--provider", "azure_ai", "--answerer-model", "gpt-5.6-sol",
             "--top-k=200", "--max-workers", "3", "--question-workers", "10",
             "--rpm", "60", "--resume", "--run-id", "full_entire"],
        )

    def test_capture_persists_the_redacted_command_line(self) -> None:
        argv = ["/h/.venv/bin/python", "--backend", "oss", "--mem0-api-key", FAKE_KEY]
        with patch.object(runmeta.sys, "argv", argv):
            captured = runmeta.capture()["argv"]
        self.assertNotIn(FAKE_KEY, " ".join(captured))
        self.assertEqual(captured, ["python", "--backend", "oss", "--mem0-api-key", "<redacted>"])


class FairModeGuardTest(unittest.TestCase):
    """FAIR_MODE=1 is the published-numbers guarantee.

    A behaviour-changing knob that escapes the guard lets a run stamp itself
    fair while one arm carries ingest or retrieval modifications the others do
    not.
    """

    def _assert_rejected(self, knob: str, value: str = "1") -> None:
        with patch.dict("os.environ", {"FAIR_MODE": "1", knob: value}, clear=True):
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(None)
            self.assertIn(knob, str(raised.exception))

    def test_rejects_ingest_granularity(self) -> None:
        self._assert_rejected("EG_INGEST_GRANULARITY", "session")

    def test_rejects_consolidation(self) -> None:
        self._assert_rejected("EG_CONSOLIDATE")

    def test_rejects_deep_retrieval(self) -> None:
        self._assert_rejected("EG_DEEP")

    def test_rejects_chrono_order(self) -> None:
        self._assert_rejected("EG_CHRONO_ORDER")

    def test_rejects_mem0_date_injection(self) -> None:
        self._assert_rejected("MEM0_DATE_INJECT")

    def test_rejects_bm25_scoring_knobs(self) -> None:
        self._assert_rejected("BM25_K1", "1.5")
        self._assert_rejected("BM25_B", "0.75")

    def test_rejects_an_unrecognised_entire_graph_knob(self) -> None:
        """EG_ is our own arm's namespace: unknown knobs fail closed."""
        self._assert_rejected("EG_KNOB_ADDED_AFTER_THIS_TEST_WAS_WRITTEN")

    def test_reports_every_active_knob_at_once(self) -> None:
        env = {"FAIR_MODE": "1", "EG_DEEP": "1", "MEM0_DATE_INJECT": "1"}
        with patch.dict("os.environ", env, clear=True):
            self.assertEqual(
                runmeta.asymmetry_report(), {"EG_DEEP": "1", "MEM0_DATE_INJECT": "1"}
            )

    def test_accepts_the_published_launcher_environment(self) -> None:
        """run_locomo.sh sets these on every fair run; none may trip the guard."""
        env = {
            "FAIR_MODE": "1",
            "LLM_TIMEOUT": "600",
            "MEM0_HOST": "http://localhost:18888",
            "ENTIRE_CORPUS_ROOT": "/state/full_entire/entire",
            "GRAPHIFY_STATE_ROOT": "/state/full_entire/graphify",
            "CMM_STATE_ROOT": "/state/full_entire/cmm",
            "AZURE_AI_ENDPOINT": "https://example.invalid",
        }
        with patch.dict("os.environ", env, clear=True):
            runmeta.assert_fair_mode(None)
            self.assertEqual(runmeta.asymmetry_report(), {})

    def test_exploratory_runs_are_untouched(self) -> None:
        with patch.dict("os.environ", {"EG_DEEP": "1"}, clear=True):
            runmeta.assert_fair_mode(None)


class AsymmetryCoverageTest(unittest.TestCase):
    """Fails when a new arm-scoped env knob is added without classifying it.

    Every knob the kit reads under an arm-owned prefix must be declared either
    behaviour-changing (ASYMMETRY_FLAGS) or infrastructure
    (SYMMETRIC_ARM_SETTINGS). Without this the guard silently goes stale each
    time an adapter gains a setting.
    """

    ARM_PREFIXES = (
        "EG_", "ENTIRE_", "MEM0_", "BM25_", "CMM_", "GRAPHIFY_",
        "COGNEE_", "LETTA_", "GRAPHITI_", "SUPERMEMORY_",
    )
    ENV_READ = re.compile(
        r"""os\.(?:getenv\(|environ\.get\(|environ\[)\s*["']([A-Z][A-Z0-9_]*)["']"""
    )

    def test_every_arm_scoped_knob_the_kit_reads_is_classified(self) -> None:
        kit = Path(runmeta.__file__).resolve().parents[2]
        if not (kit / "patches").is_dir():
            self.skipTest("not the kit checkout (reconstructed harness has no patches/)")
        sources = sorted(kit.rglob("*.py")) + sorted((kit / "patches").glob("*.patch"))
        found: dict[str, str] = {}
        for path in sources:
            for name in self.ENV_READ.findall(path.read_text(encoding="utf-8")):
                if name.startswith(self.ARM_PREFIXES):
                    found.setdefault(name, str(path.relative_to(kit)))

        classified = set(runmeta.ASYMMETRY_FLAGS) | runmeta.SYMMETRIC_ARM_SETTINGS
        unclassified = {k: v for k, v in sorted(found.items()) if k not in classified}
        self.assertEqual(
            unclassified,
            {},
            "arm-scoped env knobs are read but not classified. Add each to "
            "runmeta.ASYMMETRY_FLAGS if it can change what an arm ingests, "
            "retrieves or says, or to runmeta.SYMMETRIC_ARM_SETTINGS if it only "
            "says where a backend lives",
        )
        self.assertIn("EG_SESSION_EXPAND", classified)


if __name__ == "__main__":
    unittest.main()

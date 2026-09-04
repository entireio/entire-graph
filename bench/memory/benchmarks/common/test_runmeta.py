"""Tests for benchmark run metadata capture."""

from __future__ import annotations

import hashlib
import re
import tempfile
import types
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



# Shapes that each cost a review round before the value partition existed. They
# stay as named regression witnesses so the old bugs cannot come back quietly.
_HOST = runmeta._fingerprint("mem0.local")
HISTORICAL_URL_SHAPES = {
    "userinfo": (f"https://admin:{FAKE_PASSWORD}@mem0.local/v1",
                 f"https://<redacted>@{_HOST}/<redacted>"),
    "query": (f"https://mem0.local/v1?token={FAKE_KEY}",
              f"https://{_HOST}/<redacted>?<redacted>"),
    "fragment": (f"https://mem0.local/v1#{FAKE_KEY}",
                 f"https://{_HOST}/<redacted>#<redacted>"),
    "path_segment": (f"https://mem0.local/hooks/{FAKE_KEY}",
                     f"https://{_HOST}/<redacted>"),
    "at_sign_in_password": (f"https://admin:pa@ss{FAKE_PASSWORD}@mem0.local/",
                            f"https://<redacted>@{_HOST}/"),
    "file_uri": (f"file:///hooks/{FAKE_KEY}", "file:<redacted>"),
    "mailto_uri": (f"mailto:admin:{FAKE_PASSWORD}@mem0.local", "mailto:<redacted>"),
    "secret_in_hostname": (f"https://{FAKE_KEY}.service.example/",
                           f"https://{runmeta._fingerprint(FAKE_KEY + '.service.example')}/"),
}

# Every value-taking option, with a hostile value, in every argv shape.
HOSTILE_VALUES = {
    "--top-k": FAKE_KEY, "--max-workers": FAKE_KEY, "--question-workers": FAKE_KEY,
    "--max-questions": FAKE_KEY, "--rpm": FAKE_KEY, "--seed": FAKE_KEY,
    "--per-type": FAKE_KEY, "--conversations": FAKE_KEY, "--categories": FAKE_KEY,
    "--top-k-cutoffs": FAKE_KEY, "--backend": FAKE_KEY, "--mode": FAKE_KEY,
    "--mem0-host": f"https://admin:{FAKE_PASSWORD}@mem0.local/hooks/{FAKE_KEY}?t={FAKE_KEY_2}",
    "--dataset-path": f"/secrets/{FAKE_KEY}/locomo10.json",
    "--output-dir": f"/out/{FAKE_KEY}",
    "--judge-provider": FAKE_KEY,
    "--mem0-api-key": FAKE_KEY, "--project-name": FAKE_KEY, "--run-id": FAKE_KEY,
    "--answerer-model": FAKE_KEY, "--judge-model": FAKE_KEY, "--provider": FAKE_KEY,
    "--question-types": FAKE_KEY,
}

_FINGERPRINT_RE = re.compile(r"sha256:[0-9a-f]{12}$")
# A URL is recorded as scheme + fingerprinted authority + a marker for each
# component that was dropped. The authority is fingerprinted because a hostname
# is free-form: a tenant id or token can live in one.
_URL_LOCATION_RE = re.compile(
    r"[A-Za-z][A-Za-z0-9+.-]*:"
    r"(<redacted>|//(<redacted>@)?sha256:[0-9a-f]{12}"
    r"(/<redacted>|/)?(\?<redacted>)?(#<redacted>)?)$"
)


class RedactArgvTest(unittest.TestCase):
    """The provenance block is published; argv must not carry a credential.

    Values are recorded only within the closed domain of their own option, so
    the guarantee is total rather than a list of shapes someone anticipated.
    """

    def assert_closed(self, token: str) -> None:
        """Every recorded token comes from a set fixed before the run."""
        if token in runmeta._ARGV_SAFE_OPTS or token == "<redacted>":
            return
        if _FINGERPRINT_RE.fullmatch(token) or _URL_LOCATION_RE.fullmatch(token):
            return
        if runmeta._INT_RE.fullmatch(token) or runmeta._INT_LIST_RE.fullmatch(token):
            return
        if token in runmeta.BACKEND_CHOICES or token in runmeta.MODE_CHOICES:
            return
        name, sep, value = token.partition("=")
        if sep and name in runmeta._ARGV_SAFE_OPTS:
            return self.assert_closed(value)
        self.fail(f"free-form token reached the artifact: {token!r}")

    def test_no_value_outside_its_option_domain_is_ever_recorded(self) -> None:
        """Totality: every option, hostile value, every argv shape."""
        argv = ["/home/runner/.venv/bin/python"]
        for option, value in HOSTILE_VALUES.items():
            argv += [option, value, f"{option}={value}"]
        for shape, _ in HISTORICAL_URL_SHAPES.values():
            argv += ["--mem0-host", shape]
        argv += ["--API-KEY", FAKE_KEY, f"AZURE_AI_API_KEY={FAKE_KEY}", FAKE_KEY,
                 "-k", FAKE_KEY, "--debug", "-1", "--", "-"]

        redacted = runmeta.redact_argv(argv)

        rendered = " ".join(redacted)
        self.assertNotIn(FAKE_KEY, rendered)
        self.assertNotIn(FAKE_KEY_2, rendered)
        self.assertNotIn(FAKE_PASSWORD, rendered)
        for token in redacted[1:]:
            with self.subTest(token=token):
                self.assert_closed(token)

    def test_historical_url_shapes_stay_fixed(self) -> None:
        """One named witness per review round that found a bypass."""
        for name, (shape, expected) in HISTORICAL_URL_SHAPES.items():
            with self.subTest(shape=name):
                self.assertEqual(
                    runmeta.redact_argv(["run.py", "--mem0-host", shape]),
                    ["run.py", "--mem0-host", expected],
                )

    def test_a_windows_drive_path_never_reaches_the_artifact(self) -> None:
        """Was a `_scrub_value` special case; paths are now fingerprinted."""
        recorded = runmeta.redact_argv(["run.py", "--output-dir", "C:\\results\\locomo"])[2]
        self.assertRegex(recorded, _FINGERPRINT_RE)

    def test_negative_numbers_reach_their_domain_check(self) -> None:
        """argparse reads `-1` as a value, not as a new option."""
        argv = ["run.py", "--seed", "-1", "--max-questions", "-1"]
        self.assertEqual(runmeta.redact_argv(argv), argv)

    def test_a_negative_number_is_not_a_licence_to_consume_any_token(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["run.py", "--top-k", f"--mem0-api-key={FAKE_KEY}"]),
            ["run.py", "--top-k", "--mem0-api-key=<redacted>"],
        )
        self.assertEqual(
            runmeta.redact_argv(["run.py", "--debug", "-1"]),
            ["run.py", "--debug", "<redacted>"],
        )

    def test_the_published_launcher_keeps_every_closed_domain_value(self) -> None:
        """run_locomo.sh: nothing in a closed domain is lost."""
        self.assertEqual(
            runmeta.redact_argv([
                "/h/.venv/bin/python", "--project-name", "full_entire",
                "--backend", "entire", "--top-k", "200", "--top-k-cutoffs", "200",
                "--max-workers", "3", "--question-workers", "10", "--rpm", "60",
                "--resume", "--run-id", "full_entire",
            ]),
            ["python", "--project-name", "<redacted>", "--backend", "entire",
             "--top-k", "200", "--top-k-cutoffs", "200", "--max-workers", "3",
             "--question-workers", "10", "--rpm", "60", "--resume",
             "--run-id", "<redacted>"],
        )

    def test_backend_survives_for_the_only_downstream_reader(self) -> None:
        """ci/summarize_run.py reads the arm back out, in both argv forms."""
        for argv in (["run.py", "--backend", "entire"], ["run.py", "--backend=oss"]):
            with self.subTest(argv=argv):
                self.assertEqual(runmeta.redact_argv(argv), argv)

    def test_a_value_outside_a_closed_enum_is_dropped(self) -> None:
        self.assertEqual(
            runmeta.redact_argv(["run.py", "--backend", "not-an-arm"]),
            ["run.py", "--backend", "<redacted>"],
        )

    def test_identity_options_keep_their_name_and_lose_their_value(self) -> None:
        """Recoverable from `metadata`; see FAIR-CONFIG.md B7."""
        for option in ("--project-name", "--run-id", "--answerer-model",
                       "--judge-model", "--provider", "--mem0-api-key"):
            with self.subTest(option=option):
                self.assertEqual(
                    runmeta.redact_argv(["run.py", option, FAKE_KEY]),
                    ["run.py", option, "<redacted>"],
                )

    def test_paths_are_recorded_as_comparable_fingerprints(self) -> None:
        first = runmeta.redact_argv(["run.py", "--dataset-path", "/data/locomo10.json"])[2]
        again = runmeta.redact_argv(["run.py", "--dataset-path", "/data/locomo10.json"])[2]
        other = runmeta.redact_argv(["run.py", "--dataset-path", "/data/other.json"])[2]
        self.assertRegex(first, _FINGERPRINT_RE)
        self.assertEqual(first, again)
        self.assertNotEqual(first, other)

    def test_a_host_is_comparable_but_not_readable(self) -> None:
        """A hostname is free-form, so it is fingerprinted rather than kept."""
        same = [runmeta.redact_argv(["run.py", "--mem0-host", url])[2]
                for url in ("http://localhost:18888", "http://localhost:18888")]
        other = runmeta.redact_argv(["run.py", "--mem0-host", "http://elsewhere:18888"])[2]
        self.assertEqual(*same)
        self.assertNotEqual(same[0], other)
        self.assertNotIn("localhost", same[0])
        self.assertRegex(same[0], _URL_LOCATION_RE)

    def test_capture_persists_the_redacted_command_line(self) -> None:
        argv = ["/h/.venv/bin/python", "--backend", "oss", "--mem0-api-key", FAKE_KEY]
        with patch.object(runmeta.sys, "argv", argv):
            captured = runmeta.capture()["argv"]
        self.assertNotIn(FAKE_KEY, " ".join(captured))
        self.assertEqual(captured, ["python", "--backend", "oss", "--mem0-api-key", "<redacted>"])


class EnumSyncTest(unittest.TestCase):
    """A domain that drifts from the runner blanks the artifact silently.

    An unrecognised value records as `<redacted>`, so a backend added to
    `run.py` without being added here would make the provenance go quiet exactly
    when the configuration is unusual -- the same provenance-loss class that
    produced the negative-number and drive-letter defects.
    """

    def test_backend_choices_match_patch_0003(self) -> None:
        patch_path = (
            Path(runmeta.__file__).resolve().parents[2]
            / "patches"
            / "0003-locomo-run-backends-search-retry-drop-accounting-runmeta.patch"
        )
        if not patch_path.is_file():
            self.skipTest("not the kit checkout (reconstructed harness has no patches/)")
        text = patch_path.read_text(encoding="utf-8")
        match = re.search(r'\+\s*parser\.add_argument\("--backend".*?choices=\[([^\]]*)\]',
                          text, re.S)
        self.assertIsNotNone(match, "patch 0003 no longer declares --backend choices")
        declared = set(re.findall(r'"([^"]+)"', match.group(1)))

        self.assertEqual(
            declared,
            set(runmeta.BACKEND_CHOICES),
            "runmeta.BACKEND_CHOICES has drifted from the --backend choices in "
            "patch 0003; a value outside the set records as <redacted>",
        )


class ImplementationProvenanceTest(unittest.TestCase):
    """FAIR_MODE permits pointing an arm at a different build, and PATH does so
    with no env var at all. `code_hashes()` covers the harness, not the binary
    the harness drives, so what actually ran is bound here instead.
    """

    def setUp(self) -> None:
        runmeta._DIGEST_CACHE.clear()

    def test_an_overridden_backend_binary_is_bound_by_content(self) -> None:
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "entire-graph"
            binary.write_bytes(b"#!/bin/sh\nexit 0\n")
            with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": str(binary)}, clear=True):
                recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]

            self.assertEqual(recorded["path"], str(binary))
            self.assertEqual(recorded["source"], "env")
            self.assertEqual(recorded["resolved_via"], "literal")
            self.assertEqual(
                recorded["digest"],
                "sha256:" + hashlib.sha256(b"#!/bin/sh\nexit 0\n").hexdigest(),
            )

    def test_a_different_build_produces_a_different_digest(self) -> None:
        """Two runs of the 'same' arm are only comparable if this differs."""
        digests = []
        for body in (b"build one\n", b"build two\n"):
            with tempfile.TemporaryDirectory() as tempdir:
                binary = Path(tempdir) / "entire-graph"
                binary.write_bytes(body)
                runmeta._DIGEST_CACHE.clear()
                with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": str(binary)}, clear=True):
                    digests.append(runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]["digest"])
        self.assertNotEqual(*digests)

    def test_a_path_resolved_binary_is_bound_too(self) -> None:
        """The override is not the only unbound route; PATH is the default one."""
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "entire-graph"
            binary.write_bytes(b"from PATH\n")
            with patch.dict("os.environ", {}, clear=True), \
                    patch.object(runmeta.shutil, "which",
                                 lambda name: str(binary) if name == "entire-graph" else None):
                recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]

        self.assertEqual(recorded["source"], "default")
        self.assertEqual(recorded["resolved_via"], "PATH")
        self.assertEqual(
            recorded["digest"], "sha256:" + hashlib.sha256(b"from PATH\n").hexdigest()
        )

    def test_a_command_name_override_is_resolved_through_path(self) -> None:
        """The adapters exec a bare name via PATH; hashing the name records
        `unreadable` for a build that really ran."""
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "entire-graph"
            binary.write_bytes(b"named on PATH\n")
            with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": "entire-graph"}, clear=True), \
                    patch.object(runmeta.shutil, "which",
                                 lambda name: str(binary) if name == "entire-graph" else None):
                recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]

        self.assertEqual(recorded["path"], str(binary))
        self.assertEqual(recorded["resolved_via"], "PATH")
        self.assertEqual(
            recorded["digest"], "sha256:" + hashlib.sha256(b"named on PATH\n").hexdigest()
        )

    def test_an_imported_clients_own_default_is_bound(self) -> None:
        """An arm that never sets its env var still runs a specific build."""
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "cmm"
            binary.write_bytes(b"default build\n")
            module = types.SimpleNamespace(_DEFAULT_BIN=str(binary))
            key = f"{runmeta.__package__}.cmm_client"
            with patch.dict("sys.modules", {key: module}), \
                    patch.dict("os.environ", {}, clear=True):
                recorded = runmeta.implementation_provenance()["CMM_BIN"]

        self.assertEqual(recorded["source"], "default")
        self.assertEqual(
            recorded["digest"], "sha256:" + hashlib.sha256(b"default build\n").hexdigest()
        )

    def test_an_unreadable_binary_is_recorded_as_unreadable(self) -> None:
        with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": "/nonexistent/entire-graph"}, clear=True):
            recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]
        self.assertTrue(recorded["digest"].startswith("unreadable:"))

    def test_capture_carries_the_block(self) -> None:
        with patch.dict("os.environ", {}, clear=True):
            self.assertIn("implementations", runmeta.capture())


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

    def test_a_secret_named_knob_is_fingerprinted_not_printed(self) -> None:
        """The report is persisted AND interpolated into the exception text."""
        env = {"FAIR_MODE": "1", "EG_API_KEY": FAKE_KEY}
        with patch.dict("os.environ", env, clear=True):
            report = runmeta.asymmetry_report()
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(None)
        self.assertNotIn(FAKE_KEY, str(report))
        self.assertNotIn(FAKE_KEY, str(raised.exception))
        self.assertIn("EG_API_KEY", str(raised.exception))
        self.assertTrue(report["EG_API_KEY"].startswith("sha256:"))

    def test_rejects_a_backend_override_that_disagrees_with_the_flag(self) -> None:
        """`backend = os.getenv("MEM0_BACKEND", args.backend)` outranks the flag,
        so the artifact could name one arm in argv while another one ran."""
        class Args:
            backend = "entire"
            user_profile = False

        with patch.dict("os.environ", {"FAIR_MODE": "1", "MEM0_BACKEND": "oss"}, clear=True):
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(Args())
        self.assertIn("MEM0_BACKEND=oss", str(raised.exception))

    def test_allows_a_backend_override_that_agrees_with_the_flag(self) -> None:
        class Args:
            backend = "entire"
            user_profile = False

        with patch.dict("os.environ", {"FAIR_MODE": "1", "MEM0_BACKEND": "entire"}, clear=True):
            runmeta.assert_fair_mode(Args())

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

        classified = (set(runmeta.ASYMMETRY_FLAGS) | runmeta.SYMMETRIC_ARM_SETTINGS
                      | runmeta.ARM_SELECTION_SETTINGS)
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

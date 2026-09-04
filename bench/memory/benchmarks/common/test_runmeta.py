"""Tests for benchmark run metadata capture."""

from __future__ import annotations

import hashlib
import re
import subprocess
import tempfile
import time
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

    # Two runs whose cmm/graphify/bm25 configuration differs materially. Only
    # infrastructure knobs differ: the asymmetry knobs among these namespaces
    # are reported separately by asymmetry_report(), so if the env block cannot
    # separate these two, nothing in the artifact can.
    _RUN_A = {
        "FAIR_MODE": "1",
        "LLM_TIMEOUT": "600",
        "MEM0_HOST": "http://localhost:18888",
        "ENTIRE_CORPUS_ROOT": "/state/A/entire",
        "CMM_STATE_ROOT": "/state/A/cmm",
        "GRAPHIFY_STATE_ROOT": "/state/A/graphify",
        "GRAPHIFY_BRIDGE": "/opt/bridge-a.py",
        "BM25_STATE_ROOT": "/state/A/bm25",
    }
    _RUN_B = dict(
        _RUN_A,
        CMM_STATE_ROOT="/state/B/cmm",
        GRAPHIFY_STATE_ROOT="/state/B/graphify",
        GRAPHIFY_BRIDGE="/opt/bridge-b.py",
        BM25_STATE_ROOT="/state/B/bm25",
    )

    def test_every_arm_namespace_reaches_the_env_block(self) -> None:
        """BM25_, CMM_ and GRAPHIFY_ were the arm namespaces left uncaptured.

        Capture is asserted by key, not by value: the state roots and the bridge
        are paths, so the value partition records them as fingerprints. The
        property that matters here is that every namespace reaches the block at
        all, which the companion test then leans on to separate two runs.
        """
        with patch.dict("os.environ", self._RUN_A, clear=True):
            snapshot = runmeta.env_snapshot()
        self.assertEqual(set(snapshot), set(self._RUN_A))
        for name, recorded in snapshot.items():
            with self.subTest(name=name):
                self.assertTrue(
                    recorded == self._RUN_A[name] or recorded.startswith("sha256:"),
                    f"{name} is neither its declared-domain value nor a fingerprint",
                )

    def test_configurations_that_differ_serialize_differently(self) -> None:
        with patch.dict("os.environ", self._RUN_A, clear=True):
            first = runmeta.env_snapshot()
        with patch.dict("os.environ", self._RUN_B, clear=True):
            second = runmeta.env_snapshot()
        self.assertNotEqual(
            first,
            second,
            "two runs with different cmm/graphify/bm25 state serialize the same "
            "env block, so the artifact cannot say which configuration ran",
        )

    def test_a_secret_named_arm_knob_is_never_emitted(self) -> None:
        """Widening capture must not widen what is emitted in cleartext.

        Stronger than the fingerprint this originally asserted: a credential is
        redacted outright, because a 12-hex digest of a low-entropy secret is
        recoverable by hashing a guessed candidate list.
        """
        with patch.dict("os.environ", {"CMM_API_KEY": FAKE_KEY}, clear=True):
            snapshot = runmeta.env_snapshot()
        self.assertNotIn(FAKE_KEY, str(snapshot))
        self.assertEqual(snapshot["CMM_API_KEY"], "<redacted>")

    def test_two_dirty_checkouts_are_distinguishable(self) -> None:
        """`commit=X, dirty=true` is the same string for two different
        uncommitted implementations at one checkout."""
        with tempfile.TemporaryDirectory() as tempdir:
            root = Path(tempdir)
            def git(*args):
                subprocess.run(("git",) + args, cwd=root, capture_output=True, check=True)
            git("init", "-q")
            git("config", "user.email", "t@t")
            git("config", "user.name", "t")
            (root / "impl.py").write_text("original\n", encoding="utf-8")
            git("add", "-A")
            git("commit", "-qm", "base")

            clean = runmeta.git_state(root)
            (root / "impl.py").write_text("variant one\n", encoding="utf-8")
            first = runmeta.git_state(root)
            (root / "impl.py").write_text("variant two\n", encoding="utf-8")
            second = runmeta.git_state(root)

        self.assertNotIn("dirty_digest", clean)
        self.assertEqual(first["commit"], second["commit"])
        self.assertTrue(first["dirty"] and second["dirty"])
        self.assertNotEqual(
            first["dirty_digest"], second["dirty_digest"],
            "two different uncommitted implementations serialize identically",
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

            self.assertEqual(recorded["path"], runmeta._fingerprint(str(binary)))
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

        self.assertEqual(recorded["path"], runmeta._fingerprint(str(binary)))
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

    def test_the_graphify_bridge_is_bound(self) -> None:
        """GraphifyClient executes the bridge; an overridden one changes all of
        its ingest and search behaviour."""
        with tempfile.TemporaryDirectory() as tempdir:
            bridge = Path(tempdir) / "bridge.py"
            bridge.write_bytes(b"modified bridge\n")
            with patch.dict("os.environ", {"GRAPHIFY_BRIDGE": str(bridge)}, clear=True):
                recorded = runmeta.implementation_provenance()["GRAPHIFY_BRIDGE"]

        self.assertEqual(recorded["source"], "env")
        self.assertEqual(
            recorded["digest"], "sha256:" + hashlib.sha256(b"modified bridge\n").hexdigest()
        )

    def test_a_computed_client_default_is_called(self) -> None:
        """The bridge default is a function, not a constant."""
        with tempfile.TemporaryDirectory() as tempdir:
            bridge = Path(tempdir) / "bridge.py"
            bridge.write_bytes(b"default bridge\n")
            module = types.SimpleNamespace(_default_bridge_path=lambda: str(bridge))
            key = f"{runmeta.__package__}.graphify_client"
            with patch.dict("sys.modules", {key: module}), \
                    patch.dict("os.environ", {}, clear=True):
                recorded = runmeta.implementation_provenance()["GRAPHIFY_BRIDGE"]

        self.assertEqual(recorded["source"], "default")
        self.assertEqual(
            recorded["digest"], "sha256:" + hashlib.sha256(b"default bridge\n").hexdigest()
        )

    def test_the_default_source_checkout_is_bound(self) -> None:
        """An arm that sets no override still imports a specific checkout."""
        module = types.SimpleNamespace(_DEFAULT_SOURCE="/repos/graphify")
        key = f"{runmeta.__package__}.graphify_client"
        with patch.dict("sys.modules", {key: module}), \
                patch.dict("os.environ", {}, clear=True):
            recorded = runmeta.implementation_provenance()["GRAPHIFY_SOURCE"]

        self.assertEqual(recorded["path"], runmeta._fingerprint("/repos/graphify"))
        self.assertEqual(recorded["source"], "default")
        self.assertIn("commit", recorded)

    def test_a_rebuilt_binary_is_not_reported_as_the_original(self) -> None:
        """capture() runs at four metadata sites, hours apart; a binary can be
        rebuilt between them while the adapters keep invoking it."""
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "entire-graph"
            binary.write_bytes(b"build one\n")
            with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": str(binary)}, clear=True):
                first = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]["digest"]
                time.sleep(0.01)
                binary.write_bytes(b"build two, rebuilt mid-run\n")
                second = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]["digest"]

        self.assertNotEqual(first, second, "the artifact claimed the original build")
        self.assertEqual(
            second,
            "sha256:" + hashlib.sha256(b"build two, rebuilt mid-run\n").hexdigest(),
        )

    def test_an_unchanged_binary_is_hashed_once(self) -> None:
        """The cache still has to earn its keep: ~80MB, four capture sites."""
        with tempfile.TemporaryDirectory() as tempdir:
            binary = Path(tempdir) / "entire-graph"
            binary.write_bytes(b"stable build\n")
            with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": str(binary)}, clear=True):
                runmeta.implementation_provenance()
                entries = len(runmeta._DIGEST_CACHE)
                runmeta.implementation_provenance()
                self.assertEqual(len(runmeta._DIGEST_CACHE), entries)

    def test_a_resolved_path_is_not_disclosed(self) -> None:
        """A path can carry a username; env_snapshot fingerprints these same
        variables, so the inventory must not publish them verbatim."""
        secret_path = f"/home/{FAKE_KEY}/bin/entire-graph"
        with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": secret_path}, clear=True):
            recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]
        self.assertNotIn(FAKE_KEY, str(recorded))
        self.assertRegex(recorded["path"], r"^sha256:[0-9a-f]{12}$")

    def test_an_unreadable_binary_is_recorded_as_unreadable(self) -> None:
        with patch.dict("os.environ", {"ENTIRE_GRAPH_BIN": "/nonexistent/entire-graph"}, clear=True):
            recorded = runmeta.implementation_provenance()["ENTIRE_GRAPH_BIN"]
        self.assertTrue(recorded["digest"].startswith("unreadable:"))

    def test_capture_carries_the_block(self) -> None:
        with patch.dict("os.environ", {}, clear=True):
            self.assertIn("implementations", runmeta.capture())


class EnvValueTest(unittest.TestCase):
    """Env values are recorded by class, not by what the name looks like.

    The name-only rule was inverted in practice: it passed `NEO4J_AUTH`,
    `NEO4J_URI`, `REDIS_URL`, `LETTA_PG_URI`, `MEM0_HOST` and
    `AZURE_AI_ENDPOINT` verbatim while fingerprinting a version string.
    """

    CREDENTIAL_CARRIERS = {
        "NEO4J_AUTH": f"neo4j/{FAKE_PASSWORD}",
        "NEO4J_URI": f"bolt://neo4j:{FAKE_PASSWORD}@graph.local:7687",
        "REDIS_URL": f"redis://:{FAKE_PASSWORD}@cache.local:6379/0",
        "LETTA_PG_URI": f"postgresql://letta:{FAKE_PASSWORD}@db.local/letta",
        "COGNEE_DB_CONNECTION": f"postgres://c:{FAKE_PASSWORD}@db/c",
        "MEM0_HOST": f"https://admin:{FAKE_PASSWORD}@mem0.local/",
        "AZURE_AI_ENDPOINT": f"https://{FAKE_KEY}.services.ai.azure.com/",
        "OPENAI_BASE_URL": f"https://{FAKE_KEY}.internal/v1",
        "ANTHROPIC_BASE_URL": f"https://gw.local/?auth={FAKE_KEY}",
        "SUPERMEMORY_URL": f"https://sm.local/{FAKE_KEY}",
        "QDRANT_URL": f"https://{FAKE_KEY}.qdrant.cloud:6333",
        "ENTIRE_CORPUS_ROOT": f"/home/{FAKE_KEY}/memarms/state",
        "EG_UNRECOGNISED_KNOB": FAKE_KEY,
    }

    def test_no_captured_value_carries_secret_material(self) -> None:
        with patch.dict("os.environ", self.CREDENTIAL_CARRIERS, clear=True):
            captured = runmeta.env_snapshot()

        self.assertEqual(len(captured), len(self.CREDENTIAL_CARRIERS))
        rendered = " ".join(f"{k}={v}" for k, v in captured.items())
        self.assertNotIn(FAKE_KEY, rendered)
        self.assertNotIn(FAKE_PASSWORD, rendered)

    def test_a_credential_named_variable_is_redacted_not_fingerprinted(self) -> None:
        """A 12-hex digest of `neo4j/password` is brute-forceable; (b) and (c)
        must not collapse into one class."""
        for name in ("NEO4J_AUTH", "MEM0_API_KEY", "SUPERMEMORY_TOKEN"):
            with self.subTest(name=name):
                with patch.dict("os.environ", {name: FAKE_PASSWORD}, clear=True):
                    self.assertEqual(runmeta.env_snapshot()[name], "<redacted>")

    def test_a_value_with_no_declared_domain_is_a_comparable_fingerprint(self) -> None:
        with patch.dict("os.environ", {"MEM0_HOST": "http://localhost:18888"}, clear=True):
            first = runmeta.env_snapshot()["MEM0_HOST"]
        with patch.dict("os.environ", {"MEM0_HOST": "http://localhost:18888"}, clear=True):
            again = runmeta.env_snapshot()["MEM0_HOST"]
        with patch.dict("os.environ", {"MEM0_HOST": "http://elsewhere:18888"}, clear=True):
            other = runmeta.env_snapshot()["MEM0_HOST"]
        self.assertRegex(first, r"^sha256:[0-9a-f]{12}$")
        self.assertEqual(first, again)
        self.assertNotEqual(first, other)

    def test_a_closed_domain_value_stays_readable(self) -> None:
        env = {"LLM_TIMEOUT": "600", "FAIR_MODE": "1", "MEM0_BACKEND": "entire",
               "EG_INGEST_GRANULARITY": "turn+session", "BM25_K1": "1.5"}
        with patch.dict("os.environ", env, clear=True):
            self.assertEqual(runmeta.env_snapshot(), env)

    def test_the_api_version_stays_readable(self) -> None:
        """A genuine gain: the name contains `API`, the value is provenance."""
        with patch.dict("os.environ", {"AZURE_AI_API_VERSION": "2024-05-01-preview"}, clear=True):
            self.assertEqual(
                runmeta.env_snapshot()["AZURE_AI_API_VERSION"], "2024-05-01-preview"
            )

    def test_a_value_outside_its_declared_domain_is_not_recorded(self) -> None:
        with patch.dict("os.environ", {"LLM_TIMEOUT": FAKE_KEY}, clear=True):
            self.assertNotIn(FAKE_KEY, runmeta.env_snapshot()["LLM_TIMEOUT"])

    def test_the_fair_mode_exception_text_carries_no_value(self) -> None:
        """The second door: this text lands in CI logs, read by more people
        than the artifact. Both doors go through `_env_value`."""
        env = {"FAIR_MODE": "1", "EG_ENDPOINT": f"https://{FAKE_KEY}.host/?t={FAKE_KEY}"}
        with patch.dict("os.environ", env, clear=True):
            report = runmeta.asymmetry_report()
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(None)
        self.assertNotIn(FAKE_KEY, str(raised.exception))
        self.assertNotIn(FAKE_KEY, str(report))
        self.assertIn("EG_ENDPOINT", str(raised.exception))


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
        """`session` is the client default; `turn+session` is the deviation."""
        self._assert_rejected("EG_INGEST_GRANULARITY", "turn+session")

    def test_rejects_consolidation(self) -> None:
        self._assert_rejected("EG_CONSOLIDATE")

    def test_rejects_deep_retrieval(self) -> None:
        self._assert_rejected("EG_DEEP")

    def test_rejects_chrono_order(self) -> None:
        self._assert_rejected("EG_CHRONO_ORDER")

    def test_rejects_mem0_date_injection(self) -> None:
        self._assert_rejected("MEM0_DATE_INJECT")

    def test_rejects_bm25_scoring_knobs(self) -> None:
        """Non-default values only: the client's own defaults are 1.2 and 0.75,
        and setting a knob to its default changes nothing."""
        self._assert_rejected("BM25_K1", "1.5")
        self._assert_rejected("BM25_B", "0.9")

    def test_rejects_an_unrecognised_entire_graph_knob(self) -> None:
        """EG_ is our own arm's namespace: unknown knobs fail closed."""
        self._assert_rejected("EG_KNOB_ADDED_AFTER_THIS_TEST_WAS_WRITTEN")

    def test_reports_every_active_knob_at_once(self) -> None:
        env = {"FAIR_MODE": "1", "EG_DEEP": "1", "MEM0_DATE_INJECT": "1"}
        with patch.dict("os.environ", env, clear=True):
            self.assertEqual(
                runmeta.asymmetry_report(), {"EG_DEEP": "1", "MEM0_DATE_INJECT": "1"}
            )

    def test_a_secret_named_knob_is_redacted_not_printed(self) -> None:
        """The report is persisted AND interpolated into the exception text."""
        env = {"FAIR_MODE": "1", "EG_API_KEY": FAKE_KEY}
        with patch.dict("os.environ", env, clear=True):
            report = runmeta.asymmetry_report()
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(None)
        self.assertNotIn(FAKE_KEY, str(report))
        self.assertNotIn(FAKE_KEY, str(raised.exception))
        self.assertIn("EG_API_KEY", str(raised.exception))
        self.assertEqual(report["EG_API_KEY"], "<redacted>")

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

    def test_a_free_form_backend_override_is_not_printed(self) -> None:
        """The mismatch message lands in CI logs; it goes through _env_value."""
        class Args:
            backend = "entire"
            user_profile = False

        env = {"FAIR_MODE": "1", "MEM0_BACKEND": f"https://{FAKE_KEY}.host/"}
        with patch.dict("os.environ", env, clear=True):
            with self.assertRaises(SystemExit) as raised:
                runmeta.assert_fair_mode(Args())
        self.assertNotIn(FAKE_KEY, str(raised.exception))
        self.assertIn("MEM0_BACKEND", str(raised.exception))

    def test_a_knob_explicitly_switched_off_does_not_abort(self) -> None:
        """`EG_DEEP=0` is how an operator disables a feature; aborting for it
        punished the correct way of saying "off"."""
        # BM25_STEM is deliberately absent: its client default is "1" and only
        # the exact string "0" disables it, so "False" is a deviation, not an
        # "off" -- covered by test_a_default_on_knob_deviates_at_zero.
        for name, value in (("EG_DEEP", "0"), ("MEM0_DATE_INJECT", "false"),
                            ("EG_SESSION_EXPAND", "0"), ("EG_CONSOLIDATE", "0")):
            with self.subTest(knob=f"{name}={value}"):
                with patch.dict("os.environ", {"FAIR_MODE": "1", name: value}, clear=True):
                    runmeta.assert_fair_mode(None)
                    self.assertEqual(runmeta.asymmetry_report(), {})

    def test_a_default_on_knob_deviates_at_zero(self) -> None:
        """`BM25_STEM`/`BM25_STOPWORDS` are disabled only by the exact string
        "0", and `BM25_K1` defaults to 1.2, so reading `0` as "off" let a real
        arm change through the guard."""
        for name, value in (("BM25_STEM", "0"), ("BM25_STOPWORDS", "0"),
                            ("BM25_K1", "0"), ("BM25_B", "0"),
                            ("CMM_MEM_BUDGET_MB", "0"), ("CMM_TIMEOUT", "1")):
            with self.subTest(knob=f"{name}={value}"):
                with patch.dict("os.environ", {"FAIR_MODE": "1", name: value}, clear=True):
                    with self.assertRaises(SystemExit):
                        runmeta.assert_fair_mode(None)

    def test_setting_a_knob_to_its_own_default_is_not_a_deviation(self) -> None:
        """Explicitly writing the default changes nothing, so it must not abort
        a fair run -- `EG_INGEST_GRANULARITY=session` is the published config."""
        for name, value in (("BM25_K1", "1.2"), ("BM25_B", "0.75"),
                            ("BM25_STEM", "1"), ("CMM_TIMEOUT", "900"),
                            ("EG_INGEST_GRANULARITY", "session")):
            with self.subTest(knob=f"{name}={value}"):
                with patch.dict("os.environ", {"FAIR_MODE": "1", name: value}, clear=True):
                    runmeta.assert_fair_mode(None)
                    self.assertEqual(runmeta.asymmetry_report(), {})

    def test_a_knob_switched_on_still_aborts(self) -> None:
        for name, value in (("EG_DEEP", "1"), ("MEM0_DATE_INJECT", "true"),
                            ("EG_SESSION_EXPAND", "2")):
            with self.subTest(knob=f"{name}={value}"):
                with patch.dict("os.environ", {"FAIR_MODE": "1", name: value}, clear=True):
                    with self.assertRaises(SystemExit):
                        runmeta.assert_fair_mode(None)

    def test_an_unrecognised_knob_still_fails_closed_when_zero(self) -> None:
        """No declared domain means no way to know `0` is off."""
        with patch.dict("os.environ", {"FAIR_MODE": "1", "EG_UNKNOWN": "0"}, clear=True):
            with self.assertRaises(SystemExit):
                runmeta.assert_fair_mode(None)

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

    # runmeta owns the definition: the same tuple decides what the env block
    # captures, so a namespace cannot be arm-scoped for the classification guard
    # and invisible to provenance at the same time.
    ARM_PREFIXES = runmeta.ARM_PREFIXES
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

    GETENV_DEFAULT = re.compile(
        r"""os\.getenv\(\s*["']([A-Z][A-Z0-9_]*)["']\s*,\s*["']([^"']*)["']"""
    )

    def test_declared_knob_defaults_match_the_clients(self) -> None:
        """`_is_active` compares against the default, so a stale default is a
        silent fairness hole rather than a visible error."""
        kit = Path(runmeta.__file__).resolve().parents[2]
        if not (kit / "patches").is_dir():
            self.skipTest("not the kit checkout (reconstructed harness has no patches/)")
        sources = sorted(kit.rglob("*.py")) + sorted((kit / "patches").glob("*.patch"))
        found: dict[str, str] = {}
        for path in sources:
            if path.name == "runmeta.py" or path.name.startswith("test_"):
                continue
            for name, default in self.GETENV_DEFAULT.findall(path.read_text(encoding="utf-8")):
                found.setdefault(name, default)

        drifted = {
            name: (default, runmeta.ENV_KNOB_DEFAULTS[name])
            for name, default in sorted(found.items())
            if name in runmeta.ENV_KNOB_DEFAULTS
            and runmeta.ENV_KNOB_DEFAULTS[name] != default
        }
        self.assertEqual(
            drifted, {},
            "runmeta.ENV_KNOB_DEFAULTS disagrees with the client's own "
            "os.getenv default (source value, declared value)",
        )
        self.assertEqual(runmeta.ENV_KNOB_DEFAULTS.get("BM25_STEM"), "1")

    def test_every_arm_scoped_knob_declares_a_value_class(self) -> None:
        """The env analogue of the argv enum-sync guard.

        A knob with no declared domain falls through to a fingerprint, so the
        artifact goes quiet exactly when the configuration is unusual. Declaring
        it either way is cheap; discovering the silence later is not.
        """
        kit = Path(runmeta.__file__).resolve().parents[2]
        if not (kit / "patches").is_dir():
            self.skipTest("not the kit checkout (reconstructed harness has no patches/)")
        sources = sorted(kit.rglob("*.py")) + sorted((kit / "patches").glob("*.patch"))
        found: dict[str, str] = {}
        for path in sources:
            for name in self.ENV_READ.findall(path.read_text(encoding="utf-8")):
                if name.startswith(self.ARM_PREFIXES):
                    found.setdefault(name, str(path.relative_to(kit)))

        declared = set(runmeta.ENV_VALUE_DOMAINS) | runmeta.ENV_DERIVED_VALUES
        undeclared = {k: v for k, v in sorted(found.items()) if k not in declared}
        self.assertEqual(
            undeclared,
            {},
            "arm-scoped env knobs are read but declare no value class. Add each "
            "to runmeta.ENV_VALUE_DOMAINS with its closed domain, or to "
            "runmeta.ENV_DERIVED_VALUES if it is a path, host or free text that "
            "can only be recorded as a fingerprint",
        )

    def test_every_classified_knob_reaches_the_env_block(self) -> None:
        """Classifying a knob is not enough -- it has to be recorded.

        `CMM_STATE_ROOT`, `GRAPHIFY_BRIDGE` and friends were classified
        infrastructure, which excludes them from `asymmetry_report()`, while
        their namespaces were absent from `_CAPTURE_PREFIXES`, which excluded
        them from the env block. They reached no part of the artifact. This
        keeps capture total as the classification tables grow.
        """
        classified = (set(runmeta.ASYMMETRY_FLAGS) | runmeta.SYMMETRIC_ARM_SETTINGS
                      | runmeta.ARM_SELECTION_SETTINGS)
        uncaptured = sorted(
            k for k in classified if not k.startswith(runmeta._CAPTURE_PREFIXES)
        )
        self.assertEqual(
            uncaptured,
            [],
            "classified arm knobs that env_snapshot() would drop. Add their "
            "namespace to runmeta.ARM_PREFIXES so the run artifact records "
            "which configuration actually ran",
        )

    def test_the_arm_namespaces_are_captured_verbatim_from_one_list(self) -> None:
        for prefix in runmeta.ARM_PREFIXES:
            self.assertIn(prefix, runmeta._CAPTURE_PREFIXES)


if __name__ == "__main__":
    unittest.main()

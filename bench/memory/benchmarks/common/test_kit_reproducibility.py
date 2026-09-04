"""Reproducibility guards for the vendored adapters in this kit.

Every test here protects a property that, when it broke, made the kit runnable
on exactly one machine or made an infrastructure failure look like a benchmark
result. They need no third-party package: the three adapters under test import
only the standard library.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from bench.memory.benchmarks.common import cmm_client, entire_client, graphify_client
from bench.memory.benchmarks.common.cmm_client import CmmClient
from bench.memory.benchmarks.common.entire_client import (
    EntireMemoryClient,
    make_memory_client,
)
from bench.memory.benchmarks.common.graphify_client import GraphifyClient

_COMMON = Path(__file__).resolve().parent
_KIT = _COMMON.parents[1]

# The BM25 exclusion list of `src/mcp/mcp.c::bm25_search`, before and after
# `patches/0005-cmm-v0.9.0-markdown-sections.patch`. Spelled out here rather
# than imported from the adapter so that the adapter's build fingerprints are
# checked against the patch itself, not against a copy of themselves.
_STOCK_EXCLUSION = b"'File','Folder','Module','Section','Variable','Project'"
_PATCHED_EXCLUSION = b"'File','Folder','Module','Variable','Project'"


def _clean_env(**overrides: str) -> dict:
    """os.environ with every adapter variable removed, then ``overrides`` applied."""
    env = {
        k: v
        for k, v in os.environ.items()
        if not k.startswith(("GRAPHIFY_", "CMM_", "ENTIRE_"))
    }
    env.update(overrides)
    return env


class AdapterDefaultsArePortableTest(unittest.TestCase):
    """No adapter default may encode one contributor's directory layout."""

    def test_no_adapter_default_points_into_a_home_directory(self) -> None:
        offenders = []
        for path in sorted(_COMMON.glob("*_client.py")):
            for lineno, line in enumerate(
                path.read_text(encoding="utf-8").splitlines(), start=1
            ):
                if re.search(r'["\'](?:/home/|/Users/)[^"\']*["\']', line):
                    offenders.append(f"{path.name}:{lineno}: {line.strip()}")
        self.assertEqual(
            [],
            offenders,
            "adapter defaults must be portable or required-and-validated; "
            "an absolute path under someone's home directory is neither:\n"
            + "\n".join(offenders),
        )


class GraphifyRequiredConfigurationTest(unittest.TestCase):
    def test_missing_interpreter_env_fails_with_the_variable_to_set(self) -> None:
        with patch.dict(os.environ, _clean_env(), clear=True):
            with self.assertRaises(RuntimeError) as ctx:
                GraphifyClient()
        self.assertIn("GRAPHIFY_PYTHON", str(ctx.exception))

    def test_unusable_interpreter_path_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            bogus = os.path.join(tmp, "no-such-python")
            with patch.dict(
                os.environ, _clean_env(GRAPHIFY_PYTHON=bogus), clear=True
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    GraphifyClient()
        self.assertIn("GRAPHIFY_PYTHON", str(ctx.exception))

    def test_missing_source_env_fails_with_the_variable_to_set(self) -> None:
        with patch.dict(
            os.environ, _clean_env(GRAPHIFY_PYTHON=sys.executable), clear=True
        ):
            with self.assertRaises(RuntimeError) as ctx:
                GraphifyClient()
        self.assertIn("GRAPHIFY_SOURCE", str(ctx.exception))

    def test_validated_configuration_constructs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            source = os.path.join(tmp, "graphify")
            os.makedirs(source)
            with patch.dict(
                os.environ,
                _clean_env(
                    GRAPHIFY_PYTHON=sys.executable,
                    GRAPHIFY_SOURCE=source,
                    GRAPHIFY_STATE_ROOT=os.path.join(tmp, "state"),
                ),
                clear=True,
            ):
                client = GraphifyClient()
        self.assertEqual(sys.executable, client.python)
        self.assertEqual(source, client.source)


class CmmBinaryResolutionTest(unittest.TestCase):
    """The cmm arm publishes a PATCHED build, so it must never run an unverified one.

    The published row is `cmm (patched, Markdown-Section)`: `patches/0005` drops
    `'Section'` from the two BM25 exclusion lists in `src/mcp/mcp.c`. The shipped
    v0.9.0 build retrieves *nothing* on a prose corpus, so resolving a bare name
    on PATH could attribute a shipped build's score to the patched row -- a
    silently wrong number, which is the one failure this kit exists to prevent.
    Every accepted binary is therefore fingerprinted against the build the
    operator declared, and anything else aborts at construction.
    """

    @staticmethod
    def _binary(directory: str, marker: bytes, name: str = "codebase-memory-mcp") -> str:
        path = os.path.join(directory, name)
        with open(path, "wb") as handle:
            handle.write(b"#!/bin/sh\n# fake cmm build\n# " + marker + b"\nexit 0\n")
        os.chmod(path, 0o755)
        return path

    def _patched(self, directory: str, name: str = "codebase-memory-mcp") -> str:
        return self._binary(directory, _PATCHED_EXCLUSION, name)

    def _stock(self, directory: str, name: str = "codebase-memory-mcp") -> str:
        return self._binary(directory, _STOCK_EXCLUSION, name)

    @staticmethod
    def _state(tmp: str) -> str:
        return os.path.join(tmp, "state")

    def test_a_binary_on_path_is_never_selected_implicitly(self) -> None:
        """Even a perfectly good binary on PATH must be named, not discovered."""
        with tempfile.TemporaryDirectory() as tmp:
            self._patched(tmp)
            with patch.dict(
                os.environ,
                _clean_env(PATH=tmp, CMM_STATE_ROOT=self._state(tmp)),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BIN", str(ctx.exception))

    def test_the_shipped_unpatched_build_is_refused(self) -> None:
        """The regression: a stock v0.9.0 binary must not score as the patched row."""
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._stock(tmp)
            with patch.dict(
                os.environ,
                _clean_env(CMM_BIN=binary, CMM_STATE_ROOT=self._state(tmp)),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        message = str(ctx.exception)
        self.assertIn("CMM_UNPATCHED_BINARY", message)
        self.assertIn("Markdown-Section", message)
        self.assertIn("CMM_BUILD", message)

    def test_the_patched_build_is_accepted_and_labelled(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._patched(tmp)
            with patch.dict(
                os.environ,
                _clean_env(CMM_BIN=binary, CMM_STATE_ROOT=self._state(tmp)),
                clear=True,
            ):
                client = CmmClient()
        self.assertEqual(binary, client.binary)
        self.assertEqual("patched", client.build)

    def test_a_binary_carrying_neither_fingerprint_is_refused(self) -> None:
        """An unidentifiable build fails loudly; it never falls back to trusted."""
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._binary(tmp, b"some other program entirely")
            with patch.dict(
                os.environ,
                _clean_env(CMM_BIN=binary, CMM_STATE_ROOT=self._state(tmp)),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_UNVERIFIED_BINARY", str(ctx.exception))

    def test_the_shipped_build_runs_only_when_it_is_declared(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._stock(tmp)
            with patch.dict(
                os.environ,
                _clean_env(
                    CMM_BIN=binary,
                    CMM_BUILD="stock",
                    CMM_STATE_ROOT=self._state(tmp),
                ),
                clear=True,
            ):
                client = CmmClient()
        self.assertEqual("stock", client.build)

    def test_declaring_the_shipped_build_still_rejects_a_patched_binary(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._patched(tmp)
            with patch.dict(
                os.environ,
                _clean_env(
                    CMM_BIN=binary,
                    CMM_BUILD="stock",
                    CMM_STATE_ROOT=self._state(tmp),
                ),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BUILD", str(ctx.exception))

    def test_an_unknown_build_declaration_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._patched(tmp)
            with patch.dict(
                os.environ,
                _clean_env(
                    CMM_BIN=binary,
                    CMM_BUILD="probably-patched",
                    CMM_STATE_ROOT=self._state(tmp),
                ),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BUILD", str(ctx.exception))

    def test_the_fingerprint_survives_a_chunk_boundary(self) -> None:
        """A real binary is megabytes; the scan must not miss a straddling marker."""
        with tempfile.TemporaryDirectory() as tmp:
            binary = os.path.join(tmp, "codebase-memory-mcp")
            marker = _PATCHED_EXCLUSION
            with open(binary, "wb") as handle:
                handle.write(b"\x00" * ((1 << 20) - (len(marker) // 2)))
                handle.write(marker)
                handle.write(b"\x00" * (1 << 20))
            os.chmod(binary, 0o755)
            with patch.dict(
                os.environ,
                _clean_env(CMM_BIN=binary, CMM_STATE_ROOT=self._state(tmp)),
                clear=True,
            ):
                client = CmmClient()
        self.assertEqual("patched", client.build)

    def test_a_named_bare_binary_is_resolved_on_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._patched(tmp)
            with patch.dict(
                os.environ,
                _clean_env(
                    PATH=tmp,
                    CMM_BIN="codebase-memory-mcp",
                    CMM_STATE_ROOT=self._state(tmp),
                ),
                clear=True,
            ):
                client = CmmClient()
        self.assertEqual(binary, client.binary)

    def test_binary_absent_from_path_fails_at_construction(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with patch.dict(
                os.environ,
                _clean_env(
                    PATH=tmp,
                    CMM_BIN="codebase-memory-mcp",
                    CMM_STATE_ROOT=self._state(tmp),
                ),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BIN", str(ctx.exception))

    def test_the_build_fingerprints_are_the_patch_0005_exclusion_lists(self) -> None:
        """The fingerprints must track the patch, not the other way round."""
        patch_text = (
            _KIT / "patches" / "0005-cmm-v0.9.0-markdown-sections.patch"
        ).read_text(encoding="utf-8")
        removed = [
            line for line in patch_text.splitlines()
            if line.startswith("-") and not line.startswith("---")
        ]
        added = [
            line for line in patch_text.splitlines()
            if line.startswith("+") and not line.startswith("+++")
        ]
        self.assertTrue(
            all(_STOCK_EXCLUSION.decode() in line for line in removed),
            "patch 0005 no longer removes exactly the shipped exclusion list",
        )
        self.assertTrue(
            any(_PATCHED_EXCLUSION.decode() in line for line in added),
            "patch 0005 no longer introduces the patched exclusion list",
        )
        self.assertEqual(_STOCK_EXCLUSION, cmm_client._STOCK_MARKER)
        self.assertEqual(_PATCHED_EXCLUSION, cmm_client._PATCHED_MARKER)

    def test_explicit_binary_that_does_not_exist_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with patch.dict(
                os.environ,
                _clean_env(CMM_BIN=os.path.join(tmp, "nope", "cmm")),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BIN", str(ctx.exception))


class UnvendoredBackendTest(unittest.TestCase):
    def test_backends_without_a_vendored_adapter_raise_an_actionable_error(self) -> None:
        for backend, (module_name, _cls) in sorted(
            entire_client._UNVENDORED_BACKENDS.items()
        ):
            with self.subTest(backend=backend):
                self.assertFalse(
                    (_COMMON / f"{module_name}.py").exists(),
                    f"{module_name}.py is now vendored; drop it from "
                    "_UNVENDORED_BACKENDS and give it a real construction test",
                )
                with self.assertRaises(RuntimeError) as ctx:
                    make_memory_client(backend)
                message = str(ctx.exception)
                self.assertIn(f"{module_name}.py", message)
                self.assertIn("does not include", message)
                self.assertNotIsInstance(ctx.exception, ModuleNotFoundError)


class EntireSearchFailureIsNotAnEmptyResultTest(unittest.IsolatedAsyncioTestCase):
    """A failed `entire-graph search` must reach the harness's drop accounting.

    Returning [] recorded binary, timeout, index and validation failures as
    retrieval-quality misses, which is a silently wrong score rather than a
    drop -- and the same fault was already loud on every other arm.
    """

    def _client(self, tmp: str, proc: subprocess.CompletedProcess) -> EntireMemoryClient:
        client = EntireMemoryClient(corpus_root=os.path.join(tmp, "corpus-root"))
        client._buffers["u"] = [(0, [{"role": "user", "content": "hello"}])]
        client._built.add("u")
        client._session_dates["u"] = {}
        client._run = lambda *a, **k: proc  # type: ignore[method-assign]
        return client

    def setUp(self) -> None:
        # The adapter logs the full failure for post-run diagnosis; keep that
        # out of the test output without weakening the assertions.
        silence = patch.object(entire_client.logger, "error")
        silence.start()
        self.addCleanup(silence.stop)

    async def test_nonzero_exit_raises(self) -> None:
        proc = subprocess.CompletedProcess(
            args=["entire-graph"], returncode=2, stdout="", stderr="index missing"
        )
        with tempfile.TemporaryDirectory() as tmp:
            client = self._client(tmp, proc)
            with self.assertRaises(RuntimeError) as ctx:
                await client.search("q", "u", top_k=5)
        self.assertIn("rc=2", str(ctx.exception))
        self.assertIn("index missing", str(ctx.exception))

    async def test_non_json_output_raises(self) -> None:
        proc = subprocess.CompletedProcess(
            args=["entire-graph"], returncode=0, stdout="panic: not json", stderr=""
        )
        with tempfile.TemporaryDirectory() as tmp:
            client = self._client(tmp, proc)
            with self.assertRaises(RuntimeError) as ctx:
                await client.search("q", "u", top_k=5)
        self.assertIn("non-JSON", str(ctx.exception))

    async def test_a_genuinely_empty_result_is_still_an_empty_list(self) -> None:
        proc = subprocess.CompletedProcess(
            args=["entire-graph"],
            returncode=0,
            stdout=json.dumps({"results": []}),
            stderr="",
        )
        with tempfile.TemporaryDirectory() as tmp:
            client = self._client(tmp, proc)
            self.assertEqual([], await client.search("q", "u", top_k=5))


if __name__ == "__main__":
    unittest.main()

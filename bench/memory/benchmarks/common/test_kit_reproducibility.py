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
    @staticmethod
    def _fake_binary(directory: str, name: str = "codebase-memory-mcp") -> str:
        path = os.path.join(directory, name)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("#!/bin/sh\nexit 0\n")
        os.chmod(path, 0o755)
        return path

    def test_default_is_a_bare_name_resolved_on_path(self) -> None:
        self.assertEqual("codebase-memory-mcp", cmm_client._DEFAULT_BIN)
        with tempfile.TemporaryDirectory() as tmp:
            binary = self._fake_binary(tmp)
            with patch.dict(
                os.environ,
                _clean_env(PATH=tmp, CMM_STATE_ROOT=os.path.join(tmp, "state")),
                clear=True,
            ):
                client = CmmClient()
        self.assertEqual(binary, client.binary)

    def test_binary_absent_from_path_fails_at_construction(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with patch.dict(
                os.environ,
                _clean_env(PATH=tmp, CMM_STATE_ROOT=os.path.join(tmp, "state")),
                clear=True,
            ):
                with self.assertRaises(RuntimeError) as ctx:
                    CmmClient()
        self.assertIn("CMM_BIN", str(ctx.exception))

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

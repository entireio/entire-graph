"""Behavioural tests for the search-retry wrapper shipped in patch 0003.

`benchmarks/locomo/run.py` is upstream code and is not vendored here (see
UPSTREAM.md), so the patch is the only in-repo artifact carrying this logic.
The test therefore lifts the wrapper straight out of the patch text and runs
it, which is what a fresh application of the patch will execute.

What it protects: a search that fails without raising must still be counted as
a drop. The upstream `Mem0Client._search_oss` / `_search_cloud` wrap every
attempt in `except Exception`, log ``SEARCH failed after N attempts``, and
``return []`` on the last one. An uncounted drop is a silently wrong
denominator -- an infrastructure failure recorded as a capability miss on the
primary Mem0 arms.
"""

from __future__ import annotations

import asyncio
import logging
import os
import unittest
from pathlib import Path
from unittest.mock import patch

PATCH_PATH = (
    Path(__file__).resolve().parents[1]
    / "patches"
    / "0003-locomo-run-backends-search-retry-drop-accounting-runmeta.patch"
)
BLOCK_START = "+_RETRY_MAX = "
BLOCK_END = "+    return [], True"


def load_wrapper() -> dict:
    """Execute the retry wrapper exactly as patch 0003 adds it to run.py."""
    lines = PATCH_PATH.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith(BLOCK_START))
    end = next(i for i, line in enumerate(lines) if line == BLOCK_END)
    added = []
    for line in lines[start : end + 1]:
        if not line.startswith("+"):
            raise AssertionError(f"unexpected non-added line in the wrapper: {line!r}")
        added.append(line[1:])
    namespace: dict = {"os": os, "asyncio": asyncio}
    exec("\n".join(added) + "\n", namespace)  # noqa: S102 - the artifact under test
    return namespace


class Mem0Client:
    """Stands in for the upstream client: returns [] instead of raising."""

    def __init__(self, script) -> None:
        self.script = list(script)
        self.calls = 0

    async def search(self, query, user_id, top_k=200, score_debug=False):
        self.calls += 1
        value = self.script.pop(0) if self.script else []
        if isinstance(value, Exception):
            raise value
        return value


class RaisingClient(Mem0Client):
    """Stands in for every other adapter, which raises on a failed search."""


# The wrapper identifies swallowing clients by class and module name, so the
# stand-in has to carry a non-Mem0 identity.
RaisingClient.__name__ = "EntireMemoryClient"
RaisingClient.__qualname__ = "EntireMemoryClient"
RaisingClient.__module__ = "benchmarks.common.entire_client"


async def _no_sleep(*args, **kwargs) -> None:
    return None


class SearchRetryDropAccountingTest(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.wrapper = load_wrapper()
        cls.logger = logging.getLogger("test_locomo_search_retry_patch")
        cls.logger.addHandler(logging.NullHandler())
        cls.logger.propagate = False

    async def search(self, client):
        with patch("asyncio.sleep", new=_no_sleep):
            return await self.wrapper["_search_with_retry"](
                client, "question?", "user_1", 200, False, self.logger
            )

    async def test_exhausted_search_returning_empty_is_counted_as_a_drop(self) -> None:
        """The non-raising failure shape: [] from a client that swallows."""
        client = Mem0Client([[], [], [], [], [], []])
        results, dropped = await self.search(client)
        self.assertEqual(results, [])
        self.assertTrue(dropped, "a non-raising search failure was not counted as a drop")
        self.assertGreater(client.calls, 1, "the empty result was never retried")

    async def test_a_search_that_recovers_on_retry_is_not_a_drop(self) -> None:
        client = Mem0Client([[], [{"memory": "m"}]])
        results, dropped = await self.search(client)
        self.assertEqual(len(results), 1)
        self.assertFalse(dropped)
        self.assertEqual(client.calls, 2)

    async def test_a_successful_search_is_never_a_drop(self) -> None:
        client = Mem0Client([[{"memory": "m"}]])
        results, dropped = await self.search(client)
        self.assertEqual(len(results), 1)
        self.assertFalse(dropped)
        self.assertEqual(client.calls, 1)

    async def test_a_raising_search_is_still_counted_as_a_drop(self) -> None:
        client = Mem0Client([TimeoutError("timed out")] * 8)
        results, dropped = await self.search(client)
        self.assertEqual(results, [])
        self.assertTrue(dropped)

    async def test_empty_from_a_raising_client_is_a_real_empty_result(self) -> None:
        """Clients that raise on failure (README 3.2) keep their [] meaningful."""
        client = RaisingClient([[]])
        results, dropped = await self.search(client)
        self.assertEqual(results, [])
        self.assertFalse(dropped)
        self.assertEqual(client.calls, 1, "a non-swallowing client must not be retried")


if __name__ == "__main__":
    unittest.main()

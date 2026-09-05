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

import argparse
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
BLOCK_START = "+def _positive_int"
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
    namespace: dict = {"os": os, "asyncio": asyncio, "argparse": argparse}
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

    async def test_an_exhausted_search_is_counted_as_a_drop(self) -> None:
        """Patch 0002 makes the client raise SEARCH_EXHAUSTED rather than
        returning [], so the drop comes from an explicit signal."""
        client = Mem0Client([RuntimeError("SEARCH_EXHAUSTED after 5 attempts for user=u")] * 4)
        results, dropped = await self.search(client)
        self.assertEqual(results, [])
        self.assertTrue(dropped, "an exhausted search was not counted as a drop")

    async def test_an_exhausted_search_is_not_retried_again(self) -> None:
        """The client already spent its own retries; retrying here would add
        35s per query and buy nothing."""
        client = Mem0Client([RuntimeError("SEARCH_EXHAUSTED after 5 attempts for user=u")] * 4)
        await self.search(client)
        self.assertEqual(client.calls, 1)

    async def test_a_genuine_zero_match_is_not_a_drop(self) -> None:
        """`[]` is a valid retrieval result. Inferring a drop from emptiness
        would retry a valid query and then count it against the denominator."""
        client = Mem0Client([[]])
        results, dropped = await self.search(client)
        self.assertEqual(results, [])
        self.assertFalse(dropped)
        self.assertEqual(client.calls, 1, "a valid empty retrieval was retried")

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

    async def test_every_client_signals_failure_the_same_way(self) -> None:
        """mem0 raises SEARCH_EXHAUSTED, the others BUFFER_MISSING (README 3.2);
        neither is distinguished from the other by this wrapper."""
        for error in ("SEARCH_EXHAUSTED after 5 attempts for user=u",
                      "BUFFER_MISSING for conversation 3"):
            with self.subTest(error=error.split()[0]):
                _, dropped = await self.search(Mem0Client([RuntimeError(error)] * 4))
                self.assertTrue(dropped)


class PositiveIntTest(unittest.TestCase):
    """Zero is never a benign default for these: it turns the retry loop into
    zero searches and a semaphore into a permanent block, and both fail
    silently as a fully-dropped or a hung run."""

    @classmethod
    def setUpClass(cls) -> None:
        # Held in the namespace dict: assigning a plain function to a class
        # attribute would bind it as a method and pass self as the first arg.
        cls.wrapper = load_wrapper()

    def test_accepts_counts_of_at_least_one(self) -> None:
        self.assertEqual(self.wrapper["_positive_int"]("10"), 10)
        self.assertEqual(self.wrapper["_positive_int"]("1"), 1)

    def test_rejects_zero_and_negatives_and_non_integers(self) -> None:
        for raw in ("0", "-3", "abc", ""):
            with self.subTest(raw=raw):
                with self.assertRaises(argparse.ArgumentTypeError):
                    self.wrapper["_positive_int"](raw)

    def test_both_worker_flags_are_validated(self) -> None:
        """Each caps a semaphore; --max-workers had the identical deadlock."""
        added = [l[1:].strip() for l in PATCH_PATH.read_text(encoding="utf-8").splitlines()
                 if l.startswith("+") and "add_argument(" in l]
        for flag in ("--max-workers", "--question-workers"):
            with self.subTest(flag=flag):
                declared = [l for l in added if l.startswith(f'parser.add_argument("{flag}"')]
                self.assertEqual(len(declared), 1, f"{flag} is not declared once")
                self.assertIn("type=_positive_int", declared[0])

    def test_a_zero_retry_budget_is_rejected_at_import(self) -> None:
        """Zero retries runs no search and marks every question dropped."""
        added = [l[1:] for l in PATCH_PATH.read_text(encoding="utf-8").splitlines()
                 if l.startswith("+")]
        self.assertTrue(
            any(l.strip().startswith("_RETRY_MAX = _positive_int(") for l in added),
            "the retry budget is not validated",
        )
        self.assertTrue(
            any("raise SystemExit(" in l and "HARNESS_SEARCH_RETRIES" in l for l in added),
            "an invalid retry budget does not abort the run",
        )


class Patch0002Test(unittest.TestCase):
    """Patch 0002 supplies the explicit failure signal patch 0003 relies on."""

    PATCH = (
        Path(__file__).resolve().parents[1]
        / "patches"
        / "0002-mem0_client-optional-date-injection.patch"
    )

    def test_both_swallow_sites_raise_instead_of_returning_empty(self) -> None:
        text = self.PATCH.read_text(encoding="utf-8")
        removed = [l for l in text.splitlines() if l.strip() == "-                    return []"]
        added = [l for l in text.splitlines() if "SEARCH_EXHAUSTED" in l and l.startswith("+")]
        self.assertEqual(
            len(removed), 2,
            "_search_oss and _search_cloud must both stop returning [] on exhaustion",
        )
        self.assertEqual(len(added), 2, "both sites must raise SEARCH_EXHAUSTED")


if __name__ == "__main__":
    unittest.main()

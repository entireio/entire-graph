"""Guards for the two kit artefacts that are not importable Python.

``patches/`` is applied to a freshly cloned upstream harness on every CI run, so
a defect in the patch *text* is a defect in every reconstructed harness.
``run_locomo.sh`` is the launcher those runs go through.
"""

import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from bench.memory.benchmarks.common import entire_client

_KIT = Path(__file__).resolve().parents[1]
_PATCHES = _KIT / "patches"
_LAUNCHER = _KIT / "run_locomo.sh"

_HUNK = re.compile(r"^@@ -(\d+),(\d+) \+(\d+),(\d+) @@")


def _launcher_arms(variable: str) -> tuple[str, ...]:
    """The arms named by a launcher list variable, e.g. ``BUNDLED_ARMS``."""
    text = _LAUNCHER.read_text(encoding="utf-8")
    match = re.search(rf'^{variable}="([^"]*)"$', text, re.M)
    if match is None:
        raise AssertionError(f"run_locomo.sh no longer defines {variable}")
    return tuple(match.group(1).split())


def _run_launcher(*args: str, adapters: dict[str, str] | None = None):
    """Run the launcher in a throwaway harness checkout.

    ``adapters`` writes ``benchmarks/common/<name>`` before launching, standing
    in for an adapter module an operator has supplied to their own checkout.
    """
    with tempfile.TemporaryDirectory() as tmp:
        if adapters:
            common = Path(tmp) / "benchmarks" / "common"
            common.mkdir(parents=True)
            for name, body in adapters.items():
                (common / name).write_text(body, encoding="utf-8")
        env = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": tmp,
            "AZURE_AI_ENDPOINT": "https://example.invalid",
            "BENCH_STATE_ROOT": os.path.join(tmp, "state"),
        }
        return subprocess.run(
            ["bash", str(_LAUNCHER), *args],
            cwd=tmp,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )


def _hunks(patch_text: str):
    """Yield ``(header_match, body_lines)`` for every hunk in a unified diff."""
    header = None
    body: list[str] = []
    for line in patch_text.splitlines():
        match = _HUNK.match(line)
        if match:
            if header is not None:
                yield header, body
            header, body = match, []
        elif header is not None:
            if line[:1] in (" ", "+", "-", "\\") or line == "":
                body.append(line)
            else:
                yield header, body
                header, body = None, []
    if header is not None:
        yield header, body


def _post_image(patch_text: str) -> str:
    """The lines the patch leaves behind (context + additions)."""
    kept = []
    for _header, body in _hunks(patch_text):
        kept.extend(line[1:] for line in body if line[:1] in (" ", "+"))
    return "\n".join(kept)


class PatchIntegrityTest(unittest.TestCase):
    def test_every_hunk_header_matches_its_body(self) -> None:
        """A hand-edited hunk with stale counts is rejected by ``git apply``."""
        checked = 0
        for patch in sorted(_PATCHES.glob("*.patch")):
            text = patch.read_text(encoding="utf-8")
            for header, body in _hunks(text):
                checked += 1
                old = sum(1 for line in body if line[:1] in (" ", "-"))
                new = sum(1 for line in body if line[:1] in (" ", "+"))
                self.assertEqual(
                    (int(header.group(2)), int(header.group(4))),
                    (old, new),
                    f"{patch.name}: hunk '{header.group(0)}' declares counts that "
                    f"do not match its body (actual -{old} +{new})",
                )
        self.assertGreater(checked, 0, "no hunks found; the patch set moved")


class LlmTimeoutPropagationTest(unittest.TestCase):
    """``LLM_TIMEOUT`` must reach the SDK client of every provider.

    ``self.timeout`` is derived from the environment, but each ``_init_*``
    initializer configures its SDK client with the value it is *handed*. When
    the dispatch passed the raw ``timeout`` argument, the documented 600s
    override never reached the OpenAI or classic-Azure clients and they kept
    aborting at the 120s default.
    """

    PATCH = _PATCHES / "0001-llm_client-azure-ai-provider-timeouts-reasoning.patch"

    def test_every_provider_initializer_receives_the_resolved_timeout(self) -> None:
        post = _post_image(self.PATCH.read_text(encoding="utf-8"))
        self.assertIn(
            'self.timeout = float(os.getenv("LLM_TIMEOUT", timeout))',
            post,
            "the env override that the dispatch must forward is gone",
        )
        dispatch = re.findall(r"^\s*self\._init_(\w+)\((.*)\)\s*$", post, re.M)
        forwarding = [
            (name, args) for name, args in dispatch if name != "anthropic"
        ]
        self.assertEqual(
            3,
            len(forwarding),
            f"expected openai/azure/azure_ai dispatch, found {forwarding}",
        )
        for name, args in forwarding:
            with self.subTest(initializer=name):
                self.assertNotIn(
                    " timeout,",
                    f" {args}",
                    f"_init_{name} is handed the raw timeout argument, so "
                    "LLM_TIMEOUT never reaches its SDK client",
                )
                self.assertIn("self.timeout", args)


@unittest.skipIf(shutil.which("bash") is None, "bash is required")
class LauncherResumeTest(unittest.TestCase):
    """``resume`` must not be offered where it cannot work.

    entire/graphify/cmm/bm25 buffer ingested turns in memory and materialize at
    first search. A resumed run sees completed ingestion checkpoints, skips
    every ``add()``, and raises BUFFER_MISSING -- so the advertised resume path
    either fails obscurely or scores a partial corpus as a complete run.
    """

    IN_PROCESS_BUFFERED = ("entire", "graphify", "cmm", "bm25")

    def _run(self, *args: str):
        return _run_launcher(*args)

    def test_resume_is_refused_for_in_process_buffered_arms(self) -> None:
        for arm in self.IN_PROCESS_BUFFERED:
            with self.subTest(arm=arm):
                proc = self._run(arm, "resume")
                self.assertEqual(4, proc.returncode, proc.stderr)
                self.assertIn("cannot resume", proc.stderr)

    def test_a_mistyped_resume_argument_is_refused(self) -> None:
        proc = self._run("entire", "--resume")
        self.assertEqual(2, proc.returncode, proc.stderr)
        self.assertIn("exactly 'resume'", proc.stderr)

    def test_arms_without_the_buffer_are_not_refused(self) -> None:
        """Server-backed Mem0 arms keep their memory outside this process."""
        proc = self._run("oss", "resume")
        self.assertNotEqual(4, proc.returncode)
        self.assertNotIn("cannot resume", proc.stderr)

    def test_a_normal_run_is_not_refused(self) -> None:
        proc = self._run("entire")
        self.assertNotIn("cannot resume", proc.stderr)
        self.assertNotEqual(4, proc.returncode)


@unittest.skipIf(shutil.which("bash") is None, "bash is required")
class LauncherArmAgreementTest(unittest.TestCase):
    """The launcher must admit exactly the arms the harness can construct.

    ``make_memory_client`` refuses cognee/graphiti/letta/supermemory with a
    named RuntimeError because this kit vendors no adapter for them, yet the
    launcher took any string at all -- so ``run_locomo.sh cognee`` passed every
    check and died inside the harness instead. They do not belong in the
    in-process-buffer refusal: absent an adapter they never construct, so they
    never reach BUFFER_MISSING, and naming that as the reason would be false.
    """

    UNVENDORED = ("cognee", "graphiti", "letta", "supermemory")

    def test_the_launcher_arm_lists_match_the_factory(self) -> None:
        self.assertEqual(
            list(entire_client._BUNDLED_BACKENDS),
            list(_launcher_arms("BUNDLED_ARMS")),
            "run_locomo.sh and make_memory_client disagree about which arms are "
            "bundled; the launcher would accept an arm the factory refuses",
        )
        self.assertEqual(
            sorted(entire_client._UNVENDORED_BACKENDS),
            sorted(_launcher_arms("UNVENDORED_ARMS")),
            "run_locomo.sh and make_memory_client disagree about which arms need "
            "an adapter the operator must supply",
        )

    def test_the_launcher_admits_exactly_the_harness_backend_choices(self) -> None:
        """``--backend`` is the third place this set is written down."""
        post = _post_image(
            (
                _PATCHES
                / "0003-locomo-run-backends-search-retry-drop-accounting-runmeta.patch"
            ).read_text(encoding="utf-8")
        )
        match = re.search(r'--backend".*?choices=\[(.*?)\]', post, re.S)
        self.assertIsNotNone(match, "patch 0003 no longer sets --backend choices")
        self.assertEqual(
            sorted(re.findall(r'"([^"]+)"', match.group(1))),
            sorted(_launcher_arms("BUNDLED_ARMS") + _launcher_arms("UNVENDORED_ARMS")),
        )

    def test_an_unvendored_arm_is_refused_with_the_factorys_reason(self) -> None:
        for arm in self.UNVENDORED:
            for args in ((arm,), (arm, "resume")):
                with self.subTest(args=args):
                    proc = _run_launcher(*args)
                    self.assertEqual(5, proc.returncode, proc.stderr)
                    self.assertIn(f"benchmarks/common/{arm}_client.py", proc.stderr)
                    self.assertNotIn("buffers ingestion in-process", proc.stderr)

    def test_an_unknown_arm_is_refused_before_the_harness_starts(self) -> None:
        proc = _run_launcher("mem0")
        self.assertEqual(5, proc.returncode, proc.stderr)
        self.assertIn("unknown arm", proc.stderr)

    def test_a_supplied_adapter_module_is_accepted(self) -> None:
        """The factory says "supply that adapter module yourself"; honour it."""
        proc = _run_launcher(
            "cognee", adapters={"cognee_client.py": "CogneeClient = object\n"}
        )
        self.assertNotEqual(5, proc.returncode, proc.stderr)
        self.assertNotIn("REFUSING", proc.stderr)

    def test_a_supplied_buffered_adapter_still_cannot_resume(self) -> None:
        proc = _run_launcher(
            "cognee",
            "resume",
            adapters={
                "cognee_client.py": 'raise RuntimeError("BUFFER_MISSING: no buffer")\n'
            },
        )
        self.assertEqual(4, proc.returncode, proc.stderr)
        self.assertIn("cannot resume", proc.stderr)

    def test_a_supplied_server_backed_adapter_can_resume(self) -> None:
        proc = _run_launcher(
            "cognee", "resume", adapters={"cognee_client.py": "CogneeClient = object\n"}
        )
        self.assertNotEqual(4, proc.returncode, proc.stderr)
        self.assertNotIn("cannot resume", proc.stderr)


if __name__ == "__main__":
    unittest.main()
